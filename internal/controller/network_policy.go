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
	"context"
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// NetworkPolicyEnforceMode selects whether the controller emits a
// per-run NetworkPolicy. Auto defers to a CNI probe at manager startup;
// on always emits; off never does. See ADR-0013 §7.4.
type NetworkPolicyEnforceMode string

const (
	NetworkPolicyEnforceAuto NetworkPolicyEnforceMode = "auto"
	NetworkPolicyEnforceOn   NetworkPolicyEnforceMode = "on"
	NetworkPolicyEnforceOff  NetworkPolicyEnforceMode = "off"
)

// openCIDR is the IPv4 'allow everything' CIDR used as the base of
// public-internet egress rules; the linked except list narrows it.
const openCIDR = "0.0.0.0/0"

// runNetworkPolicyName returns the per-run NP's name.
func runNetworkPolicyName(runName string) string {
	return runName + "-egress"
}

// networkPolicyConfig carries the cluster-specific values needed to
// shape per-run NetworkPolicy egress. Cluster pod/service CIDRs are
// excluded from the public-internet allow rules so a hostile agent
// in cooperative mode cannot bypass HTTPS_PROXY to reach in-cluster
// targets, the kube API server, the broker, or co-tenant pods.
type networkPolicyConfig struct {
	// ClusterPodCIDR is the cluster's pod CIDR (e.g. 10.244.0.0/16).
	// Empty string is permitted (the exclude list just won't include
	// this entry); operators in clusters with non-default CIDRs should
	// set the manager flag.
	ClusterPodCIDR string
	// ClusterServiceCIDR is the cluster's service CIDR.
	ClusterServiceCIDR string
	// BrokerNamespace is the namespace where the broker is deployed
	// (default `paddock-system`). Used to construct the broker egress
	// allow rule for run-pod and seed-pod NetworkPolicies — without
	// this rule, in-cluster broker calls would be blocked by the
	// cluster-CIDR exclusion in F-19/F-45's egress rules.
	BrokerNamespace string
	// BrokerPort is the broker's TLS service port (default 8443).
	BrokerPort int32
	// APIServerIPs is the set of IPv4 addresses the controller's
	// kubeconfig resolves the kube-apiserver to. Each entry becomes a
	// /32 ipBlock peer in the per-run NP's apiserver allow rule
	// (TCP/443). Empty disables the rule. F-41 / Phase 2d.
	APIServerIPs []net.IP
}

// rfc1918AndLinkLocalCIDRs are the always-excluded ranges. RFC1918
// covers private networks; 169.254.0.0/16 covers link-local including
// the cloud metadata service at 169.254.169.254.
var rfc1918AndLinkLocalCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
}

// buildExceptCIDRs returns the deny-list to set on `to.ipBlock.except`
// for run-pod public-internet egress rules. Always-excluded private
// + link-local ranges plus any configured cluster CIDRs that aren't
// already covered.
func buildExceptCIDRs(cfg networkPolicyConfig) []string {
	exc := make([]string, 0, len(rfc1918AndLinkLocalCIDRs)+2)
	exc = append(exc, rfc1918AndLinkLocalCIDRs...)
	if cfg.ClusterPodCIDR != "" {
		exc = append(exc, cfg.ClusterPodCIDR)
	}
	if cfg.ClusterServiceCIDR != "" {
		exc = append(exc, cfg.ClusterServiceCIDR)
	}
	return exc
}

// proxyDeniedCIDRs returns the comma-separated CIDR list passed to the
// proxy as --deny-cidr. F-22 layer 2 (post-resolution + transparent
// origIP filter).
func proxyDeniedCIDRs(cfg networkPolicyConfig) string {
	parts := append([]string{}, rfc1918AndLinkLocalCIDRs...)
	if cfg.ClusterPodCIDR != "" {
		parts = append(parts, cfg.ClusterPodCIDR)
	}
	if cfg.ClusterServiceCIDR != "" {
		parts = append(parts, cfg.ClusterServiceCIDR)
	}
	return strings.Join(parts, ",")
}

// buildBrokerEgressRule returns the egress rule that allows traffic
// from the run/seed pod to the broker. Without this rule, NP
// enforcement combined with the cluster-CIDR exclusion (F-19/F-45)
// would block the proxy sidecar's broker calls.
//
// Empty BrokerNamespace returns nil — operators without an in-cluster
// broker (or without a configured namespace) get no broker rule.
func buildBrokerEgressRule(cfg networkPolicyConfig) *networkingv1.NetworkPolicyEgressRule {
	if cfg.BrokerNamespace == "" {
		return nil
	}
	tcp := corev1.ProtocolTCP
	port := cfg.BrokerPort
	if port == 0 {
		port = 8443
	}
	brokerPort := intstr.FromInt32(port)
	return &networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": cfg.BrokerNamespace,
					},
				},
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app.kubernetes.io/component": "broker",
						"app.kubernetes.io/name":      "paddock",
					},
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: &tcp, Port: &brokerPort},
		},
	}
}

// buildEgressNetworkPolicy renders the shared defence-in-depth egress
// NetworkPolicy used by both per-run pods (HarnessRun reconciler) and
// per-workspace seed pods (Workspace reconciler). The selector,
// object identity, and ObjectMeta labels differ between the two
// callers; the egress rule list (DNS to kube-dns, TCP/443 + TCP/80
// to public-internet excluding cluster CIDRs, optional broker rule,
// optional apiserver rule) is identical. See findings F-19 and F-45.
func buildEgressNetworkPolicy(
	selector metav1.LabelSelector,
	name, namespace string,
	labels map[string]string,
	cfg networkPolicyConfig,
) *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	dnsPort := intstr.FromInt32(53)
	httpsPort := intstr.FromInt32(443)
	httpPort := intstr.FromInt32(80)
	exceptCIDRs := buildExceptCIDRs(cfg)

	rules := []networkingv1.NetworkPolicyEgressRule{
		{
			To: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": "kube-system",
						},
					},
					PodSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"k8s-app": "kube-dns"},
					},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &udp, Port: &dnsPort},
				{Protocol: &tcp, Port: &dnsPort},
			},
		},
		// TCP 443 — public-internet egress, excluding private +
		// link-local + cluster CIDRs (F-19).
		{
			To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: openCIDR, Except: exceptCIDRs}},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &httpsPort},
			},
		},
		// TCP 80 — same exclusions as 443.
		{
			To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: openCIDR, Except: exceptCIDRs}},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &httpPort},
			},
		},
		// Loopback allow — required so iptables nat OUTPUT REDIRECT from
		// agent traffic on TCP 80/443 to the proxy at 127.0.0.1:15001 is
		// not dropped by Cilium-with-KPR's NetworkPolicy enforcement
		// (Issue #79). On kindnet/Calico this is a no-op (loopback isn't
		// policed); on Cilium-with-KPR it's mandatory for transparent
		// mode to work.
		{
			To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: "127.0.0.0/8"}},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp},
			},
		},
	}
	if brokerRule := buildBrokerEgressRule(cfg); brokerRule != nil {
		rules = append(rules, *brokerRule)
	}
	// Apiserver allow rule. Sidecars (collector for AuditEvent emission,
	// adapter for status writes) need TCP/443 to the kube-apiserver.
	// Pod-wide because NetworkPolicy operates at pod level; the agent
	// container shares the network namespace with sidecars. F-38 (Phase
	// 2a) ensures the agent has automountServiceAccountToken=false, so
	// the apiserver rejects any request the agent might forge.
	if len(cfg.APIServerIPs) > 0 {
		apiPeers := make([]networkingv1.NetworkPolicyPeer, 0, len(cfg.APIServerIPs))
		for _, ip := range cfg.APIServerIPs {
			apiPeers = append(apiPeers, networkingv1.NetworkPolicyPeer{
				IPBlock: &networkingv1.IPBlock{CIDR: ip.String() + "/32"},
			})
		}
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{
			To: apiPeers,
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &httpsPort},
			},
		})
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: selector,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
}

// buildRunNetworkPolicy renders the defence-in-depth NetworkPolicy
// that rides alongside the proxy sidecar. The policy targets the run's
// Pod by label, permits:
//
//   - DNS (UDP+TCP 53) to kube-dns in kube-system — name resolution has
//     to work or the proxy cannot dial upstreams;
//   - TCP 443 and TCP 80 to public-internet destinations excluding
//     RFC1918, link-local, and the cluster's pod/service CIDRs. The
//     proxy sidecar's outbound to public hosts continues to work; an
//     agent that bypasses HTTPS_PROXY in cooperative mode cannot
//     reach in-cluster targets, the kube API server, the broker, or
//     the cloud metadata service. See finding F-19.
//   - No other egress. Ingress is left permissive.
//
// The host-level allowlist the proxy enforces (from BrokerPolicy grants)
// is not rendered into the NetworkPolicy as ipBlock rules — DNS-driven
// upstream IPs rotate. Per-FQDN egress is a CNI-specific feature
// (Cilium etc.) and is Phase 2b territory.
func buildRunNetworkPolicy(run *paddockv1alpha1.HarnessRun, cfg networkPolicyConfig) *networkingv1.NetworkPolicy {
	return buildEgressNetworkPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"paddock.dev/run": run.Name}},
		runNetworkPolicyName(run.Name),
		run.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "harnessrun-egress",
			"paddock.dev/run":             run.Name,
		},
		cfg,
	)
}

// buildSeedNetworkPolicy mirrors buildRunNetworkPolicy for workspace
// seed Pods. Selector matches the workspace's seed Pod (labeled
// paddock.dev/workspace=<name>); egress shape is identical (delegates
// to buildEgressNetworkPolicy). See finding F-45.
func buildSeedNetworkPolicy(ws *paddockv1alpha1.Workspace, cfg networkPolicyConfig) *networkingv1.NetworkPolicy {
	return buildEgressNetworkPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"paddock.dev/workspace": ws.Name}},
		seedNetworkPolicyName(ws),
		ws.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "workspace-seed-egress",
			"paddock.dev/workspace":       ws.Name,
		},
		cfg,
	)
}

// ensureRunNetworkPolicy creates or updates the per-run NetworkPolicy
// based on the admission-time decision pinned in Status.NetworkPolicyEnforced
// (F-43 / Phase 2d). Reading from Status rather than the live flag means
// a controller-manager flag flip (--networkpolicy-enforce=on → off) does
// not delete the per-run NP out from under a running pod; new runs after
// the flip get the new behaviour; existing runs keep theirs.
func (r *HarnessRunReconciler) ensureRunNetworkPolicy(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	// Phase 2d (F-43): read the pinned admission-time decision from
	// Status, not the live flag. Flag flips on the manager affect new
	// runs only; existing runs retain the policy they were admitted
	// with. The pin is applied at the top of Reconcile via
	// pinNetworkPolicyEnforced, so by the time we get here Status is
	// authoritative.
	if run.Status.NetworkPolicyEnforced == nil || !*run.Status.NetworkPolicyEnforced {
		return nil
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     r.ClusterPodCIDR,
		ClusterServiceCIDR: r.ClusterServiceCIDR,
		BrokerNamespace:    r.BrokerNamespace,
		BrokerPort:         r.BrokerPort,
		APIServerIPs:       r.APIServerIPs,
	}
	desired := buildRunNetworkPolicy(run, cfg)
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desired.Name,
			Namespace: desired.Namespace,
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		if err := controllerutil.SetControllerReference(run, np, r.Scheme); err != nil {
			return err
		}
		np.Labels = desired.Labels
		np.Spec = desired.Spec
		return nil
	})
	if err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("upserting run NetworkPolicy: %w", err)
	}
	// F-43: a Create on a run that already has Status set (i.e. has been
	// reconciled before) means the NP was deleted out from under us
	// (typically by an operator). The CreateOrUpdate call already
	// re-created it — emit an audit so operators see the withdrawal.
	if op == controllerutil.OperationResultCreated && run.Status.ObservedGeneration > 0 {
		r.Audit.EmitNetworkPolicyEnforcementWithdrawn(ctx, run.Name, run.Namespace,
			fmt.Sprintf("per-run NetworkPolicy %s was missing on reconcile; re-created", desired.Name))
	}
	return nil
}

// deleteRunNetworkPolicy deletes the per-run NetworkPolicy if it exists.
// Called from the empty-`requires` branch in Reconcile to clean up any
// stale NP from a previous reconcile that did emit one (e.g., template
// edited to drop requires). Not called for the active flag-flip case
// — the pinned Status.NetworkPolicyEnforced field makes that
// unnecessary post-Phase-2d.
func (r *HarnessRunReconciler) deleteRunNetworkPolicy(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	var np networkingv1.NetworkPolicy
	key := client.ObjectKey{Namespace: run.Namespace, Name: runNetworkPolicyName(run.Name)}
	if err := r.Get(ctx, key, &np); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := r.Delete(ctx, &np); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// networkPolicyEnforced returns true when the controller should emit
// a per-run NetworkPolicy for new runs. "auto" resolves to "on" when
// the manager's CNI probe confirmed NetworkPolicy enforcement at
// startup; otherwise "auto" collapses to "off".
func (r *HarnessRunReconciler) networkPolicyEnforced() bool {
	switch r.NetworkPolicyEnforce {
	case NetworkPolicyEnforceOn:
		return true
	case NetworkPolicyEnforceAuto:
		return r.NetworkPolicyAutoEnabled
	default:
		return false
	}
}
