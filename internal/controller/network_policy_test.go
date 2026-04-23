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

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestBuildRunNetworkPolicy_Shape(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	np := buildRunNetworkPolicy(run)

	if np.Name != "hr-1-egress" {
		t.Errorf("name = %q, want hr-1-egress", np.Name)
	}
	if np.Namespace != "team-a" {
		t.Errorf("namespace = %q, want team-a", np.Namespace)
	}
	if np.Spec.PodSelector.MatchLabels["paddock.dev/run"] != "hr-1" {
		t.Errorf("podSelector = %+v, want paddock.dev/run=hr-1", np.Spec.PodSelector.MatchLabels)
	}

	// Egress-only policy — we don't restrict ingress in v0.3.
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("policyTypes = %v, want [Egress]", np.Spec.PolicyTypes)
	}

	// Expected three rules: kube-dns, TCP 443, TCP 80.
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("egress rules = %d, want 3 (DNS + 443 + 80)", len(np.Spec.Egress))
	}

	// First rule covers DNS with UDP + TCP 53 to kube-dns.
	dns := np.Spec.Egress[0]
	if len(dns.To) != 1 || dns.To[0].PodSelector == nil ||
		dns.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
		t.Errorf("DNS peer = %+v, want kube-dns podSelector", dns.To)
	}
	if len(dns.Ports) != 2 {
		t.Fatalf("DNS ports = %d, want 2 (UDP+TCP 53)", len(dns.Ports))
	}
	sawUDP, sawTCP := false, false
	for _, p := range dns.Ports {
		if p.Protocol != nil && *p.Protocol == corev1.ProtocolUDP {
			sawUDP = true
		}
		if p.Protocol != nil && *p.Protocol == corev1.ProtocolTCP {
			sawTCP = true
		}
	}
	if !sawUDP || !sawTCP {
		t.Errorf("DNS must allow both UDP and TCP 53; got %+v", dns.Ports)
	}

	// Second + third rules: TCP 443 and TCP 80 to 0.0.0.0/0.
	for i, wantPort := range []int32{443, 80} {
		rule := np.Spec.Egress[i+1]
		if len(rule.To) != 1 || rule.To[0].IPBlock == nil ||
			rule.To[0].IPBlock.CIDR != "0.0.0.0/0" {
			t.Errorf("rule[%d] peer = %+v, want 0.0.0.0/0 ipBlock", i+1, rule.To)
		}
		if len(rule.Ports) != 1 ||
			rule.Ports[0].Port == nil ||
			rule.Ports[0].Port.IntValue() != int(wantPort) {
			t.Errorf("rule[%d] port = %+v, want TCP %d", i+1, rule.Ports, wantPort)
		}
	}
}

// TestNetworkPolicyEnforced verifies the decision table across modes.
func TestNetworkPolicyEnforced(t *testing.T) {
	cases := []struct {
		mode        NetworkPolicyEnforceMode
		autoEnabled bool
		want        bool
	}{
		{NetworkPolicyEnforceOff, false, false},
		{NetworkPolicyEnforceOff, true, false}, // explicit off wins
		{NetworkPolicyEnforceOn, false, true},
		{NetworkPolicyEnforceOn, true, true},
		{NetworkPolicyEnforceAuto, false, false},
		{NetworkPolicyEnforceAuto, true, true},
		{"", false, false}, // empty treated as off
	}
	for _, c := range cases {
		r := &HarnessRunReconciler{
			NetworkPolicyEnforce:     c.mode,
			NetworkPolicyAutoEnabled: c.autoEnabled,
		}
		if got := r.networkPolicyEnforced(); got != c.want {
			t.Errorf("mode=%q auto=%v -> %v, want %v", c.mode, c.autoEnabled, got, c.want)
		}
	}
}
