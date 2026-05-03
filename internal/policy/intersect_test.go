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

package policy_test

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/policy"
)

func buildClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func policyWith(name string, appliesTo []string, creds []paddockv1alpha1.CredentialGrant, egress []paddockv1alpha1.EgressGrant) *paddockv1alpha1.BrokerPolicy {
	return &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: appliesTo,
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: creds,
				Egress:      egress,
			},
		},
	}
}

func TestIntersect_AdmitsWhenAllRequirementsGranted(t *testing.T) {
	bp := policyWith("allow-claude", []string{"claude"},
		[]paddockv1alpha1.CredentialGrant{{
			Name: "ANTHROPIC_API_KEY",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "Static",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			},
		}},
		[]paddockv1alpha1.EgressGrant{{Host: "api.anthropic.com", Ports: []int32{443}}},
	)
	c := buildClient(t, bp)

	res, err := policy.Intersect(context.Background(), c, "ns", "claude", paddockv1alpha1.RequireSpec{
		Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "ANTHROPIC_API_KEY"}},
		Egress:      []paddockv1alpha1.EgressRequirement{{Host: "api.anthropic.com", Ports: []int32{443}}},
	})
	if err != nil {
		t.Fatalf("Intersect: %v", err)
	}
	if !res.Admitted {
		t.Fatalf("expected Admitted=true; missing creds=%v egress=%v", res.MissingCredentials, res.MissingEgress)
	}
	cov, ok := res.CoveredCredentials["ANTHROPIC_API_KEY"]
	if !ok || cov.Policy != "allow-claude" || cov.Provider != "Static" {
		t.Fatalf("coverage = %+v, want {allow-claude, Static}", cov)
	}
}

func TestIntersect_EmptyNamespaceRejects(t *testing.T) {
	c := buildClient(t)
	res, err := policy.Intersect(context.Background(), c, "ns", "claude", paddockv1alpha1.RequireSpec{
		Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
	})
	if err != nil {
		t.Fatalf("Intersect: %v", err)
	}
	if res.Admitted {
		t.Fatalf("expected Admitted=false in empty namespace")
	}
	if len(res.MatchedPolicies) != 0 {
		t.Fatalf("MatchedPolicies = %v, want empty", res.MatchedPolicies)
	}
	if len(res.MissingCredentials) != 1 {
		t.Fatalf("MissingCredentials = %d, want 1", len(res.MissingCredentials))
	}
}

func TestIntersect_PolicyDoesNotApplyToTemplate(t *testing.T) {
	bp := policyWith("allow-other", []string{"other"},
		[]paddockv1alpha1.CredentialGrant{{Name: "K", Provider: paddockv1alpha1.ProviderConfig{
			Kind: "Static", SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
		}}},
		nil,
	)
	c := buildClient(t, bp)

	res, _ := policy.Intersect(context.Background(), c, "ns", "claude", paddockv1alpha1.RequireSpec{
		Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
	})
	if res.Admitted {
		t.Fatalf("policy applied to wrong template shouldn't admit")
	}
	if len(res.MatchedPolicies) != 0 {
		t.Fatalf("MatchedPolicies = %v, want empty", res.MatchedPolicies)
	}
}

func TestIntersect_WildcardSelector(t *testing.T) {
	bp := policyWith("allow-any", []string{"*"},
		[]paddockv1alpha1.CredentialGrant{{Name: "K", Provider: paddockv1alpha1.ProviderConfig{
			Kind: "Static", SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
		}}},
		nil,
	)
	c := buildClient(t, bp)

	res, _ := policy.Intersect(context.Background(), c, "ns", "whatever", paddockv1alpha1.RequireSpec{
		Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
	})
	if !res.Admitted {
		t.Fatalf("wildcard selector should admit")
	}
}

func TestIntersect_AdditiveUnion(t *testing.T) {
	creds := policyWith("creds", []string{"claude"},
		[]paddockv1alpha1.CredentialGrant{{Name: "K", Provider: paddockv1alpha1.ProviderConfig{
			Kind: "Static", SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
		}}},
		nil,
	)
	egress := policyWith("egress", []string{"claude"}, nil,
		[]paddockv1alpha1.EgressGrant{{Host: "api.anthropic.com", Ports: []int32{443}}},
	)
	c := buildClient(t, creds, egress)

	res, _ := policy.Intersect(context.Background(), c, "ns", "claude", paddockv1alpha1.RequireSpec{
		Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
		Egress:      []paddockv1alpha1.EgressRequirement{{Host: "api.anthropic.com", Ports: []int32{443}}},
	})
	if !res.Admitted {
		t.Fatalf("union of matching policies should cover; missing=%+v/%+v", res.MissingCredentials, res.MissingEgress)
	}
}

func TestIntersect_WildcardEgressHost(t *testing.T) {
	bp := policyWith("allow", []string{"claude"}, nil,
		[]paddockv1alpha1.EgressGrant{{Host: "*.anthropic.com", Ports: []int32{443}}},
	)
	c := buildClient(t, bp)

	res, _ := policy.Intersect(context.Background(), c, "ns", "claude", paddockv1alpha1.RequireSpec{
		Egress: []paddockv1alpha1.EgressRequirement{{Host: "api.anthropic.com", Ports: []int32{443}}},
	})
	if !res.Admitted {
		t.Fatalf("*.anthropic.com should cover api.anthropic.com; missing=%+v", res.MissingEgress)
	}

	// Apex domain is NOT covered by a *. prefix.
	res2, _ := policy.Intersect(context.Background(), c, "ns", "claude", paddockv1alpha1.RequireSpec{
		Egress: []paddockv1alpha1.EgressRequirement{{Host: "anthropic.com", Ports: []int32{443}}},
	})
	if res2.Admitted {
		t.Fatalf("*.anthropic.com should NOT cover anthropic.com (apex)")
	}
}

func TestIntersect_AnyPortGrant(t *testing.T) {
	bp := policyWith("allow", []string{"claude"}, nil,
		[]paddockv1alpha1.EgressGrant{{Host: "api.anthropic.com"}}, // empty Ports = any
	)
	c := buildClient(t, bp)

	res, _ := policy.Intersect(context.Background(), c, "ns", "claude", paddockv1alpha1.RequireSpec{
		Egress: []paddockv1alpha1.EgressRequirement{{Host: "api.anthropic.com", Ports: []int32{443, 8443}}},
	})
	if !res.Admitted {
		t.Fatalf("empty-ports grant should cover any required port")
	}
}

func TestIntersect_SpecificPortMismatch(t *testing.T) {
	bp := policyWith("allow", []string{"claude"}, nil,
		[]paddockv1alpha1.EgressGrant{{Host: "api.anthropic.com", Ports: []int32{443}}},
	)
	c := buildClient(t, bp)

	res, _ := policy.Intersect(context.Background(), c, "ns", "claude", paddockv1alpha1.RequireSpec{
		Egress: []paddockv1alpha1.EgressRequirement{{Host: "api.anthropic.com", Ports: []int32{8080}}},
	})
	if res.Admitted {
		t.Fatalf("specific-port mismatch should not admit")
	}
}

func TestIntersectMatches_Runs(t *testing.T) {
	t.Run("interact from one policy shell from another", func(t *testing.T) {
		pInteract := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "interact-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Interact: true,
					},
				},
			},
		}
		pShell := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "shell-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target: "runtime",
						},
					},
				},
			},
		}
		result := policy.IntersectMatches(
			[]*paddockv1alpha1.BrokerPolicy{pInteract, pShell},
			paddockv1alpha1.RequireSpec{},
		)
		if !result.RunsInteract {
			t.Errorf("RunsInteract = false, want true")
		}
		if result.Shell == nil {
			t.Fatalf("Shell = nil, want non-nil")
		}
		if result.Shell.Target != "runtime" {
			t.Errorf("Shell.Target = %q, want %q", result.Shell.Target, "runtime")
		}
	})

	t.Run("most permissive shell target wins (agent beats runtime)", func(t *testing.T) {
		pRuntime := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "runtime-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target: "runtime",
						},
					},
				},
			},
		}
		pAgent := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "agent-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target: "agent",
						},
					},
				},
			},
		}
		result := policy.IntersectMatches(
			[]*paddockv1alpha1.BrokerPolicy{pRuntime, pAgent},
			paddockv1alpha1.RequireSpec{},
		)
		if result.Shell == nil {
			t.Fatalf("Shell = nil, want non-nil")
		}
		if result.Shell.Target != "agent" {
			t.Errorf("Shell.Target = %q, want %q", result.Shell.Target, "agent")
		}
	})

	t.Run("allowedPhases empty wins over narrow", func(t *testing.T) {
		pNarrow := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "narrow-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target:        "runtime",
							AllowedPhases: []paddockv1alpha1.HarnessRunPhase{"Running", "Idle"},
						},
					},
				},
			},
		}
		pAny := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "any-phase-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target:        "runtime",
							AllowedPhases: nil, // empty = "any phase"
						},
					},
				},
			},
		}
		result := policy.IntersectMatches(
			[]*paddockv1alpha1.BrokerPolicy{pNarrow, pAny},
			paddockv1alpha1.RequireSpec{},
		)
		if result.Shell == nil {
			t.Fatalf("Shell = nil, want non-nil")
		}
		if result.Shell.AllowedPhases != nil {
			t.Errorf("AllowedPhases = %v, want nil (any phase wins)", result.Shell.AllowedPhases)
		}
	})

	t.Run("recordTranscript any-true wins", func(t *testing.T) {
		pFalse := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "no-transcript-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target:           "runtime",
							RecordTranscript: false,
						},
					},
				},
			},
		}
		pTrue := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "transcript-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target:           "runtime",
							RecordTranscript: true,
						},
					},
				},
			},
		}
		result := policy.IntersectMatches(
			[]*paddockv1alpha1.BrokerPolicy{pFalse, pTrue},
			paddockv1alpha1.RequireSpec{},
		)
		if result.Shell == nil {
			t.Fatalf("Shell = nil, want non-nil")
		}
		if !result.Shell.RecordTranscript {
			t.Errorf("RecordTranscript = false, want true (any-true wins)")
		}
	})

	t.Run("allowedPhases dedup union", func(t *testing.T) {
		pA := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "phases-a-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target:        "runtime",
							AllowedPhases: []paddockv1alpha1.HarnessRunPhase{"Running", "Idle"},
						},
					},
				},
			},
		}
		pB := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "phases-b-policy", Namespace: "ns"},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Runs: &paddockv1alpha1.GrantRunsCapabilities{
						Shell: &paddockv1alpha1.ShellCapability{
							Target:        "runtime",
							AllowedPhases: []paddockv1alpha1.HarnessRunPhase{"Idle", "Failed"},
						},
					},
				},
			},
		}
		result := policy.IntersectMatches(
			[]*paddockv1alpha1.BrokerPolicy{pA, pB},
			paddockv1alpha1.RequireSpec{},
		)
		if result.Shell == nil {
			t.Fatalf("Shell = nil, want non-nil")
		}
		want := map[paddockv1alpha1.HarnessRunPhase]struct{}{
			"Running": {},
			"Idle":    {},
			"Failed":  {},
		}
		got := make(map[paddockv1alpha1.HarnessRunPhase]struct{}, len(result.Shell.AllowedPhases))
		for _, ph := range result.Shell.AllowedPhases {
			got[ph] = struct{}{}
		}
		if len(got) != len(want) {
			t.Errorf("AllowedPhases = %v, want {Running, Idle, Failed}", result.Shell.AllowedPhases)
		}
		for ph := range want {
			if _, ok := got[ph]; !ok {
				t.Errorf("AllowedPhases missing %q; got %v", ph, result.Shell.AllowedPhases)
			}
		}
	})
}

func TestDescribeShortfall_NoPolicies(t *testing.T) {
	res := &policy.IntersectionResult{
		Admitted:           false,
		MissingCredentials: []policy.CredentialShortfall{{Name: "K"}},
	}
	out := policy.DescribeShortfall(res, "claude", "my-team")
	for _, want := range []string{"claude", "my-team", "K", "(none)", "scaffold claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("DescribeShortfall missing %q:\n%s", want, out)
		}
	}
}
