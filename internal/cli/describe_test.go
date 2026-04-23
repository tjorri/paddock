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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestDescribeTemplate_RunnableHint(t *testing.T) {
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
	if err := runDescribeTemplateFor(context.Background(), c, ns, &buf, "claude-code"); err != nil {
		t.Fatalf("describe: %v", err)
	}
	got := buf.String()
	wants := []string{
		"Name:       claude-code",
		"Harness:    claude-code",
		"Requires:",
		"ANTHROPIC_API_KEY",
		"GITHUB_TOKEN",
		"api.anthropic.com",
		"github.com",
		"Runnable in my-team:  yes",
		"allow-claude",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("describe output missing %q; got:\n%s", w, got)
		}
	}
}

func TestDescribeTemplate_ShortfallHint(t *testing.T) {
	ns := testNamespace
	tpl := claudeCodeTemplate(ns)
	// No BrokerPolicy → runnable: no, with scaffold hint.
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).WithObjects(tpl).Build()

	var buf bytes.Buffer
	if err := runDescribeTemplateFor(context.Background(), c, ns, &buf, "claude-code"); err != nil {
		t.Fatalf("describe: %v", err)
	}
	got := buf.String()
	wants := []string{
		"Runnable in my-team:  no",
		"missing credential ANTHROPIC_API_KEY",
		"missing credential GITHUB_TOKEN",
		"missing egress api.anthropic.com:443",
		"missing egress github.com:443",
		"kubectl paddock policy scaffold claude-code -n my-team",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("describe output missing %q; got:\n%s", w, got)
		}
	}
}
