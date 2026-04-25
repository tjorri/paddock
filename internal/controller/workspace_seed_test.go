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

package controller

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestBuildSeedNetworkPolicy_Shape(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
	}
	np := buildSeedNetworkPolicy(ws, cfg)

	// Expected name and namespace.
	if np.Name != seedNetworkPolicyName(ws) {
		t.Errorf("name = %q, want %q", np.Name, seedNetworkPolicyName(ws))
	}
	if np.Namespace != ws.Namespace {
		t.Errorf("namespace = %q, want %q", np.Namespace, ws.Namespace)
	}

	// Selector matches the seed Pod's labels (uses workspace label).
	if np.Spec.PodSelector.MatchLabels["paddock.dev/workspace"] != "ws-1" {
		t.Errorf("podSelector = %+v, want paddock.dev/workspace=ws-1",
			np.Spec.PodSelector.MatchLabels)
	}

	// Egress-only.
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("policyTypes = %v, want [Egress]", np.Spec.PolicyTypes)
	}

	// Three egress rules, same shape as run-pod NP: kube-dns, TCP 443
	// excluding cluster CIDRs, TCP 80 excluding cluster CIDRs.
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("egress rules = %d, want 3", len(np.Spec.Egress))
	}

	// Both public-internet rules must have non-empty Except list.
	for i, rule := range np.Spec.Egress[1:] {
		if len(rule.To) != 1 || rule.To[0].IPBlock == nil {
			t.Errorf("rule[%d] expected ipBlock; got %+v", i+1, rule.To)
			continue
		}
		if rule.To[0].IPBlock.CIDR != "0.0.0.0/0" {
			t.Errorf("rule[%d] CIDR = %q, want 0.0.0.0/0", i+1, rule.To[0].IPBlock.CIDR)
		}
		if len(rule.To[0].IPBlock.Except) == 0 {
			t.Errorf("rule[%d] except is empty; expected RFC1918 + cluster CIDRs", i+1)
		}
	}
}

func TestIsSSHURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"ssh scheme", "ssh://git@example.com/org/repo.git", true},
		{"scp-style", "git@example.com:org/repo.git", true},
		{"scp-style with port-less host", "deploy@host:repo", true},
		{"plain https", "https://example.com/org/repo.git", false},
		{"https with user info and port", "https://user@example.com:443/org/repo.git", false},
		{"https with user info only", "https://user@example.com/org/repo.git", false},
		{"http scheme", "http://example.com/repo.git", false},
		{"git scheme", "git://example.com/repo.git", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSSHURL(tc.url); got != tc.want {
				t.Fatalf("isSSHURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
