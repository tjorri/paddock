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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func policyWithDiscovery(name string, expiresAt time.Time) *paddockv1alpha1.BrokerPolicy {
	return &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
				Accepted:  true,
				Reason:    "Bootstrapping allowlist for new harness import",
				ExpiresAt: metav1.NewTime(expiresAt),
			},
		},
	}
}

func policyWithoutDiscovery(name string) *paddockv1alpha1.BrokerPolicy {
	return &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
		},
	}
}

func TestAnyDiscoveryActive(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		policies []*paddockv1alpha1.BrokerPolicy
		want     bool
	}{
		{name: "no policies", policies: nil, want: false},
		{
			name:     "one policy without discovery",
			policies: []*paddockv1alpha1.BrokerPolicy{policyWithoutDiscovery("a")},
			want:     false,
		},
		{
			name:     "one policy with active discovery",
			policies: []*paddockv1alpha1.BrokerPolicy{policyWithDiscovery("a", now.Add(time.Hour))},
			want:     true,
		},
		{
			name:     "one policy with expired discovery",
			policies: []*paddockv1alpha1.BrokerPolicy{policyWithDiscovery("a", now.Add(-time.Hour))},
			want:     false,
		},
		{
			name: "mixed: one without discovery, one with active discovery (any wins)",
			policies: []*paddockv1alpha1.BrokerPolicy{
				policyWithoutDiscovery("a"),
				policyWithDiscovery("b", now.Add(time.Hour)),
			},
			want: true,
		},
		{
			name: "mixed: one expired, one active (any wins on the active)",
			policies: []*paddockv1alpha1.BrokerPolicy{
				policyWithDiscovery("a", now.Add(-time.Hour)),
				policyWithDiscovery("b", now.Add(time.Hour)),
			},
			want: true,
		},
		{
			name: "all expired",
			policies: []*paddockv1alpha1.BrokerPolicy{
				policyWithDiscovery("a", now.Add(-time.Hour)),
				policyWithDiscovery("b", now.Add(-2*time.Hour)),
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AnyDiscoveryActive(c.policies, now)
			if got != c.want {
				t.Errorf("AnyDiscoveryActive = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAnyDiscoveryActive_AcceptedFalseTreatedAsInactive(t *testing.T) {
	// Defensive: admission rejects accepted=false, but if such a policy
	// somehow reaches the resolver (e.g. webhook bypassed), it must
	// behave as if discovery were absent.
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	bp := policyWithDiscovery("a", now.Add(time.Hour))
	bp.Spec.EgressDiscovery.Accepted = false
	if AnyDiscoveryActive([]*paddockv1alpha1.BrokerPolicy{bp}, now) {
		t.Error("AnyDiscoveryActive returned true for accepted=false; defensive check failed")
	}
}

func TestFilterUnexpired(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	a := policyWithoutDiscovery("a")
	b := policyWithDiscovery("b", now.Add(time.Hour))
	c := policyWithDiscovery("c", now.Add(-time.Hour))

	cases := []struct {
		name      string
		input     []*paddockv1alpha1.BrokerPolicy
		wantNames []string
	}{
		{name: "empty", input: nil, wantNames: nil},
		{name: "one without discovery", input: []*paddockv1alpha1.BrokerPolicy{a}, wantNames: []string{"a"}},
		{name: "one active discovery", input: []*paddockv1alpha1.BrokerPolicy{b}, wantNames: []string{"b"}},
		{name: "one expired discovery", input: []*paddockv1alpha1.BrokerPolicy{c}, wantNames: nil},
		{
			name:      "mixed: keeps non-discovery + active, drops expired",
			input:     []*paddockv1alpha1.BrokerPolicy{a, b, c},
			wantNames: []string{"a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterUnexpired(tc.input, now)
			gotNames := make([]string, 0, len(got))
			for _, p := range got {
				gotNames = append(gotNames, p.Name)
			}
			if len(gotNames) != len(tc.wantNames) {
				t.Fatalf("got %d policies, want %d (got=%v want=%v)",
					len(gotNames), len(tc.wantNames), gotNames, tc.wantNames)
			}
			for i := range gotNames {
				if gotNames[i] != tc.wantNames[i] {
					t.Errorf("policy[%d] = %s, want %s", i, gotNames[i], tc.wantNames[i])
				}
			}
		})
	}
}
