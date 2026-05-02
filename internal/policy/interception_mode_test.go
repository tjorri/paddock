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

package policy

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1 add: %v", err)
	}
	if err := paddockv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("paddock add: %v", err)
	}
	return s
}

func transparentPolicy(name string) *paddockv1alpha1.BrokerPolicy {
	return &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			Interception: &paddockv1alpha1.InterceptionSpec{
				Transparent: &paddockv1alpha1.TransparentInterception{},
			},
		},
	}
}

func cooperativePolicy(name string) *paddockv1alpha1.BrokerPolicy {
	return &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			Interception: &paddockv1alpha1.InterceptionSpec{
				CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
					Accepted: true,
					Reason:   "Cluster PSA=restricted; node-level proxy not available yet",
				},
			},
		},
	}
}

func unsetPolicy(name string) *paddockv1alpha1.BrokerPolicy {
	return &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
		},
	}
}

// Each row: the policy list + the namespace PSA label → expected decision.
func TestResolveInterceptionMode_Decision(t *testing.T) {
	cooperativeReason := "Cluster PSA=restricted; node-level proxy not available yet"

	cases := []struct {
		name                 string
		psaLabel             string
		policies             []*paddockv1alpha1.BrokerPolicy
		wantMode             paddockv1alpha1.InterceptionMode
		wantUnavail          bool
		wantAcceptanceReason string
		wantMatchedPolicy    string
	}{
		{name: "no policy, PSA permits → transparent", psaLabel: "", policies: nil, wantMode: paddockv1alpha1.InterceptionModeTransparent},
		{name: "policy transparent, PSA permits → transparent", psaLabel: PSALevelPrivileged, policies: []*paddockv1alpha1.BrokerPolicy{transparentPolicy("t")}, wantMode: paddockv1alpha1.InterceptionModeTransparent},
		{
			name:                 "policy cooperativeAccepted, PSA permits → cooperative",
			psaLabel:             PSALevelPrivileged,
			policies:             []*paddockv1alpha1.BrokerPolicy{cooperativePolicy("c")},
			wantMode:             paddockv1alpha1.InterceptionModeCooperative,
			wantAcceptanceReason: cooperativeReason,
			wantMatchedPolicy:    "c",
		},
		{name: "policy unset (default=transparent), PSA permits → transparent", psaLabel: "", policies: []*paddockv1alpha1.BrokerPolicy{unsetPolicy("u")}, wantMode: paddockv1alpha1.InterceptionModeTransparent},

		{name: "no policy, PSA blocks → unavailable (default transparent)", psaLabel: PSALevelRestricted, policies: nil, wantUnavail: true},
		{name: "policy transparent, PSA blocks → unavailable", psaLabel: PSALevelBaseline, policies: []*paddockv1alpha1.BrokerPolicy{transparentPolicy("t")}, wantUnavail: true},
		{
			name:                 "policy cooperativeAccepted, PSA blocks → cooperative",
			psaLabel:             PSALevelRestricted,
			policies:             []*paddockv1alpha1.BrokerPolicy{cooperativePolicy("c")},
			wantMode:             paddockv1alpha1.InterceptionModeCooperative,
			wantAcceptanceReason: cooperativeReason,
			wantMatchedPolicy:    "c",
		},

		{
			name:                 "two cooperativeAccepted policies, PSA blocks → cooperative (first policy wins)",
			psaLabel:             PSALevelRestricted,
			policies:             []*paddockv1alpha1.BrokerPolicy{cooperativePolicy("c1"), cooperativePolicy("c2")},
			wantMode:             paddockv1alpha1.InterceptionModeCooperative,
			wantAcceptanceReason: cooperativeReason,
			wantMatchedPolicy:    "c1",
		},
		{name: "mixed: one transparent, one cooperativeAccepted, PSA permits → transparent", psaLabel: PSALevelPrivileged, policies: []*paddockv1alpha1.BrokerPolicy{transparentPolicy("t"), cooperativePolicy("c")}, wantMode: paddockv1alpha1.InterceptionModeTransparent},
		{name: "mixed: one unset, one cooperativeAccepted, PSA blocks → unavailable", psaLabel: PSALevelRestricted, policies: []*paddockv1alpha1.BrokerPolicy{unsetPolicy("u"), cooperativePolicy("c")}, wantUnavail: true},
		{
			name:     "cooperativeAccepted with accepted=false treated as transparent",
			psaLabel: PSALevelPrivileged,
			policies: []*paddockv1alpha1.BrokerPolicy{{
				ObjectMeta: metav1.ObjectMeta{Name: "bp"},
				Spec: paddockv1alpha1.BrokerPolicySpec{
					AppliesToTemplates: []string{"*"},
					Interception: &paddockv1alpha1.InterceptionSpec{
						CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
							Accepted: false,
						},
					},
				},
			}},
			wantMode: paddockv1alpha1.InterceptionModeTransparent,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
			if c.psaLabel != "" {
				ns.Labels = map[string]string{PSAEnforceLabel: c.psaLabel}
			}
			cli := fake.NewClientBuilder().
				WithScheme(newTestScheme(t)).
				WithObjects(ns).
				Build()

			got, err := ResolveInterceptionMode(context.Background(), cli, "test", c.policies)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Unavailable != c.wantUnavail {
				t.Fatalf("Unavailable = %v, want %v (Reason=%q)", got.Unavailable, c.wantUnavail, got.Reason)
			}
			if c.wantUnavail {
				if got.Reason == "" {
					t.Errorf("Unavailable decision must carry a Reason; got empty")
				}
				return
			}
			if got.Mode != c.wantMode {
				t.Errorf("Mode = %q, want %q", got.Mode, c.wantMode)
			}
			if c.wantAcceptanceReason != "" && got.AcceptanceReason != c.wantAcceptanceReason {
				t.Errorf("AcceptanceReason = %q, want %q", got.AcceptanceReason, c.wantAcceptanceReason)
			}
			if c.wantMatchedPolicy != "" && got.MatchedPolicy != c.wantMatchedPolicy {
				t.Errorf("MatchedPolicy = %q, want %q", got.MatchedPolicy, c.wantMatchedPolicy)
			}
			if c.wantMode != paddockv1alpha1.InterceptionModeCooperative {
				// Non-cooperative decisions should not carry acceptance fields.
				if got.AcceptanceReason != "" {
					t.Errorf("AcceptanceReason = %q on non-cooperative decision; want empty", got.AcceptanceReason)
				}
				if got.MatchedPolicy != "" {
					t.Errorf("MatchedPolicy = %q on non-cooperative decision; want empty", got.MatchedPolicy)
				}
			}
		})
	}
}
