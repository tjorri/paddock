/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// testNamespace is the shared fixture namespace across CLI tests.
// Centralised so goconst lint stays quiet and the string only has to
// change once.
const testNamespace = "my-team"

// buildCLIScheme registers the paddock + core types so fake.Client
// can round-trip them. The CLI's package-level scheme is wired the
// same way in root.go's init().
func buildCLIScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("clientgo scheme: %v", err)
	}
	utilruntime.Must(paddockv1alpha1.AddToScheme(s))
	return s
}

// claudeCodeTemplate builds an in-ns HarnessTemplate with the same
// requires shape a real claude-code install would have — one llm
// credential + one gitforge credential + two egress hosts. Used
// across the CLI tests so the output shape stays realistic.
func claudeCodeTemplate(ns string) *paddockv1alpha1.HarnessTemplate {
	return &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "claude-code", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "claude-code",
			Image:   "ghcr.io/example/claude-code:v0.3.0",
			Command: []string{"/run"},
			Requires: paddockv1alpha1.RequireSpec{
				Credentials: []paddockv1alpha1.CredentialRequirement{
					{Name: "ANTHROPIC_API_KEY", Purpose: paddockv1alpha1.CredentialPurposeLLM},
					{Name: "GITHUB_TOKEN", Purpose: paddockv1alpha1.CredentialPurposeGitForge},
				},
				Egress: []paddockv1alpha1.EgressRequirement{
					{Host: "api.anthropic.com", Ports: []int32{443}},
					{Host: "github.com", Ports: []int32{443}},
				},
			},
		},
	}
}

func TestPolicyScaffold_EmitsApplyableYAML(t *testing.T) {
	ns := testNamespace
	tpl := claudeCodeTemplate(ns)
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).WithObjects(tpl).Build()

	var buf bytes.Buffer
	if err := runPolicyScaffoldFor(context.Background(), c, ns, &buf, "claude-code", scaffoldOptions{}); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	got := buf.String()
	// Strip leading comment line so the remainder is valid YAML.
	body := stripLeadingComments(got)
	var bp paddockv1alpha1.BrokerPolicy
	if err := yaml.Unmarshal([]byte(body), &bp); err != nil {
		t.Fatalf("output is not apply-able YAML: %v\n%s", err, got)
	}
	if bp.Spec.AppliesToTemplates[0] != "claude-code" {
		t.Errorf("appliesToTemplates[0] = %q, want claude-code", bp.Spec.AppliesToTemplates[0])
	}
	if len(bp.Spec.Grants.Credentials) != 2 {
		t.Fatalf("credential grants = %d, want 2", len(bp.Spec.Grants.Credentials))
	}
	// llm → AnthropicAPI, gitforge → GitHubApp.
	byName := map[string]paddockv1alpha1.CredentialGrant{}
	for _, g := range bp.Spec.Grants.Credentials {
		byName[g.Name] = g
	}
	if byName["ANTHROPIC_API_KEY"].Provider.Kind != "AnthropicAPI" {
		t.Errorf("ANTHROPIC_API_KEY.provider = %q, want AnthropicAPI", byName["ANTHROPIC_API_KEY"].Provider.Kind)
	}
	if byName["GITHUB_TOKEN"].Provider.Kind != "GitHubApp" {
		t.Errorf("GITHUB_TOKEN.provider = %q, want GitHubApp", byName["GITHUB_TOKEN"].Provider.Kind)
	}
	if len(bp.Spec.Grants.Egress) != 2 {
		t.Errorf("egress grants = %d, want 2", len(bp.Spec.Grants.Egress))
	}
	// Anthropic egress should recommend SubstituteAuth since we have an
	// llm cred; github egress should recommend it since we have a
	// gitforge cred.
	for _, e := range bp.Spec.Grants.Egress {
		if !e.SubstituteAuth {
			t.Errorf("egress %s should suggest substituteAuth=true", e.Host)
		}
	}
	// gitforge credential => scaffold must leave a TODO gitRepos entry.
	if len(bp.Spec.Grants.GitRepos) == 0 {
		t.Errorf("expected a TODO gitRepos entry for gitforge credential")
	}
	// TODO markers should be present so operators know to fill in.
	if !strings.Contains(got, "TODO") {
		t.Errorf("scaffold should include TODO placeholders; output:\n%s", got)
	}
}

func TestPolicyScaffold_ProviderOverride(t *testing.T) {
	ns := testNamespace
	tpl := claudeCodeTemplate(ns)
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).WithObjects(tpl).Build()

	var buf bytes.Buffer
	if err := runPolicyScaffoldFor(context.Background(), c, ns, &buf, "claude-code",
		scaffoldOptions{provider: "Static"}); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	body := stripLeadingComments(buf.String())
	var bp paddockv1alpha1.BrokerPolicy
	if err := yaml.Unmarshal([]byte(body), &bp); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	for _, g := range bp.Spec.Grants.Credentials {
		if g.Provider.Kind != "Static" {
			t.Errorf("--provider=Static override ignored for %q: %q", g.Name, g.Provider.Kind)
		}
	}
}

func TestPolicyList_OutputShape(t *testing.T) {
	ns := testNamespace
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-claude", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"claude-code"},
			DenyMode:           paddockv1alpha1.BrokerPolicyDenyModeBlock,
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: []paddockv1alpha1.CredentialGrant{
					{Name: "X", Provider: paddockv1alpha1.ProviderConfig{Kind: "Static"}},
				},
				Egress: []paddockv1alpha1.EgressGrant{
					{Host: "api.anthropic.com", Ports: []int32{443}},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).WithObjects(bp).Build()

	var buf bytes.Buffer
	if err := runPolicyList(context.Background(), c, ns, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	got := buf.String()
	wants := []string{"NAME", "TEMPLATES", "CREDENTIALS", "EGRESS", "GIT-REPOS", "DENY-MODE", "AGE",
		"allow-claude", "claude-code", "block"}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("list output missing %q; got:\n%s", w, got)
		}
	}
}

func TestPolicyCheck_Admitted(t *testing.T) {
	ns := testNamespace
	tpl := claudeCodeTemplate(ns)
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-claude", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"claude-code"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: []paddockv1alpha1.CredentialGrant{
					{Name: "ANTHROPIC_API_KEY", Provider: paddockv1alpha1.ProviderConfig{Kind: "AnthropicAPI"}},
					{Name: "GITHUB_TOKEN", Provider: paddockv1alpha1.ProviderConfig{Kind: "GitHubApp"}},
				},
				Egress: []paddockv1alpha1.EgressGrant{
					{Host: "api.anthropic.com", Ports: []int32{443}},
					{Host: "github.com", Ports: []int32{443}},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).WithObjects(tpl, bp).Build()

	var buf bytes.Buffer
	if err := runPolicyCheckFor(context.Background(), c, ns, &buf, "claude-code"); err != nil {
		t.Fatalf("check: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "Runnable:   yes") {
		t.Errorf("expected Runnable: yes, got:\n%s", got)
	}
	if !strings.Contains(got, "ANTHROPIC_API_KEY") || !strings.Contains(got, "GITHUB_TOKEN") {
		t.Errorf("expected covered credentials listed, got:\n%s", got)
	}
}

func TestPolicyCheck_NotAdmitted(t *testing.T) {
	ns := testNamespace
	tpl := claudeCodeTemplate(ns)
	// No BrokerPolicy granting anything.
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).WithObjects(tpl).Build()

	var buf bytes.Buffer
	err := runPolicyCheckFor(context.Background(), c, ns, &buf, "claude-code")
	if err == nil {
		t.Fatalf("expected non-nil error when template is not runnable")
	}
	got := buf.String()
	if !strings.Contains(got, "Runnable:   no") {
		t.Errorf("expected Runnable: no, got:\n%s", got)
	}
	if !strings.Contains(got, "Missing credentials") || !strings.Contains(got, "Missing egress") {
		t.Errorf("expected missing-credentials + missing-egress lines, got:\n%s", got)
	}
	if !strings.Contains(got, "kubectl paddock policy scaffold") {
		t.Errorf("expected scaffold hint, got:\n%s", got)
	}
}

// stripLeadingComments drops the "# Scaffolded from …" header so the
// rest can be unmarshalled as YAML.
func stripLeadingComments(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	skipping := true
	for _, l := range lines {
		if skipping && (strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "") {
			continue
		}
		skipping = false
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
