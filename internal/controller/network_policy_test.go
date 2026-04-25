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
	"net"
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
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
	}
	np := buildRunNetworkPolicy(run, cfg)

	if np.Name != "hr-1-egress" {
		t.Errorf("name = %q, want hr-1-egress", np.Name)
	}
	if np.Namespace != "team-a" {
		t.Errorf("namespace = %q, want team-a", np.Namespace)
	}
	if np.Spec.PodSelector.MatchLabels["paddock.dev/run"] != "hr-1" {
		t.Errorf("podSelector = %+v, want paddock.dev/run=hr-1", np.Spec.PodSelector.MatchLabels)
	}

	// Egress-only policy.
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("policyTypes = %v, want [Egress]", np.Spec.PolicyTypes)
	}

	// Three rules: kube-dns, TCP 443 (with except list), TCP 80 (same).
	// (Broker rule is conditional — not added when BrokerNamespace is unset.)
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("egress rules = %d, want 3 (DNS + 443 + 80)", len(np.Spec.Egress))
	}

	// First rule: DNS to kube-dns with both UDP and TCP 53.
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

	// Second + third: TCP 443 and TCP 80 to 0.0.0.0/0 with non-empty
	// except list. (TestBuildRunNetworkPolicy_ExcludesPrivateAndClusterCIDRs
	// verifies the exact except contents.)
	for i, wantPort := range []int32{443, 80} {
		rule := np.Spec.Egress[i+1]
		if len(rule.To) != 1 || rule.To[0].IPBlock == nil {
			t.Errorf("rule[%d] peer = %+v, want ipBlock", i+1, rule.To)
			continue
		}
		if rule.To[0].IPBlock.CIDR != "0.0.0.0/0" {
			t.Errorf("rule[%d] CIDR = %q, want 0.0.0.0/0", i+1, rule.To[0].IPBlock.CIDR)
		}
		if len(rule.To[0].IPBlock.Except) == 0 {
			t.Errorf("rule[%d] except is empty; want RFC1918 + link-local + cluster CIDRs", i+1)
		}
		if len(rule.Ports) != 1 ||
			rule.Ports[0].Port == nil ||
			rule.Ports[0].Port.IntValue() != int(wantPort) {
			t.Errorf("rule[%d] port = %+v, want TCP %d", i+1, rule.Ports, wantPort)
		}
	}
}

func TestBuildRunNetworkPolicy_ExcludesPrivateAndClusterCIDRs(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
	}
	np := buildRunNetworkPolicy(run, cfg)

	// Three rules: kube-dns, TCP 443, TCP 80.
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("egress rules = %d, want 3 (DNS + 443 + 80)", len(np.Spec.Egress))
	}

	// Rules 2 and 3 are the public-internet rules; both should now have
	// 0.0.0.0/0 with an except list that contains RFC1918 + link-local +
	// configured cluster CIDRs.
	wantExcept := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"10.244.0.0/16",
		"10.96.0.0/12",
	}
	for i := 1; i <= 2; i++ {
		rule := np.Spec.Egress[i]
		if len(rule.To) != 1 || rule.To[0].IPBlock == nil {
			t.Fatalf("rule[%d] expected ipBlock peer; got %+v", i, rule.To)
		}
		got := rule.To[0].IPBlock
		if got.CIDR != "0.0.0.0/0" {
			t.Errorf("rule[%d] CIDR = %q, want 0.0.0.0/0", i, got.CIDR)
		}
		if !cidrSliceEqual(got.Except, wantExcept) {
			t.Errorf("rule[%d] except = %v, want %v", i, got.Except, wantExcept)
		}
	}
}

// cidrSliceEqual returns true when two CIDR slices contain the same
// entries regardless of order. Test helper.
func cidrSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		if seen[s] == 0 {
			return false
		}
		seen[s]--
	}
	return true
}

func TestBuildRunNetworkPolicy_BrokerEgressRule(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
		BrokerNamespace:    "paddock-system",
	}
	np := buildRunNetworkPolicy(run, cfg)

	// Now expect 4 rules: DNS + 443 + 80 + broker.
	if len(np.Spec.Egress) != 4 {
		t.Fatalf("egress rules = %d, want 4 (DNS + 443 + 80 + broker)", len(np.Spec.Egress))
	}

	// Find the broker rule (it's the one with paddock-system namespace selector
	// and the broker pod selector).
	var brokerRule *networkingv1.NetworkPolicyEgressRule
	for i := range np.Spec.Egress {
		rule := &np.Spec.Egress[i]
		if len(rule.To) == 1 &&
			rule.To[0].NamespaceSelector != nil &&
			rule.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "paddock-system" {
			brokerRule = rule
			break
		}
	}
	if brokerRule == nil {
		t.Fatalf("expected egress rule for broker namespace; found none")
	}

	if brokerRule.To[0].PodSelector == nil {
		t.Errorf("broker rule missing podSelector")
	}
	if len(brokerRule.Ports) != 1 ||
		brokerRule.Ports[0].Port == nil ||
		brokerRule.Ports[0].Port.IntValue() != 8443 {
		t.Errorf("broker rule ports = %+v, want TCP 8443", brokerRule.Ports)
	}
}

func TestBuildRunNetworkPolicy_NoBrokerRuleWhenNamespaceUnset(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
		// BrokerNamespace deliberately unset.
	}
	np := buildRunNetworkPolicy(run, cfg)

	// Expect 3 rules: DNS + 443 + 80 (no broker rule when namespace is empty).
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("egress rules = %d, want 3 (DNS + 443 + 80; no broker rule)", len(np.Spec.Egress))
	}
}

func TestBuildRunNetworkPolicy_APIServerEgressRule(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
		APIServerIPs:       []net.IP{net.ParseIP("10.96.0.1"), net.ParseIP("10.96.0.2")},
	}
	np := buildRunNetworkPolicy(run, cfg)

	// Expect 4 rules: DNS + 443 + 80 + apiserver. (No broker rule —
	// BrokerNamespace empty.) The apiserver rule is the new one and is
	// last; assert the shape.
	if len(np.Spec.Egress) != 4 {
		t.Fatalf("egress rules = %d, want 4 (DNS + 443 + 80 + apiserver)", len(np.Spec.Egress))
	}
	apiRule := np.Spec.Egress[3]
	if len(apiRule.To) != 2 {
		t.Fatalf("apiserver rule To = %d peers, want 2", len(apiRule.To))
	}
	gotCIDRs := map[string]bool{}
	for _, p := range apiRule.To {
		if p.IPBlock == nil {
			t.Fatalf("apiserver rule peer missing IPBlock: %+v", p)
		}
		gotCIDRs[p.IPBlock.CIDR] = true
	}
	if !gotCIDRs["10.96.0.1/32"] || !gotCIDRs["10.96.0.2/32"] {
		t.Errorf("apiserver CIDRs = %v, want includes 10.96.0.1/32 + 10.96.0.2/32", gotCIDRs)
	}
	if len(apiRule.Ports) != 1 ||
		apiRule.Ports[0].Port == nil ||
		apiRule.Ports[0].Port.IntValue() != 443 {
		t.Errorf("apiserver ports = %+v, want TCP/443", apiRule.Ports)
	}
}

func TestBuildRunNetworkPolicy_NoAPIServerRuleWhenIPsEmpty(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
		// APIServerIPs deliberately empty.
	}
	np := buildRunNetworkPolicy(run, cfg)
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("egress rules = %d, want 3 (DNS + 443 + 80; no apiserver rule)", len(np.Spec.Egress))
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
