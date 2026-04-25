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
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	dnsPort := intstr.FromInt32(53)
	httpsPort := intstr.FromInt32(443)
	httpPort := intstr.FromInt32(80)
	openCIDR := "0.0.0.0/0"
	exceptCIDRs := buildExceptCIDRs(cfg)

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runNetworkPolicyName(run.Name),
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "harnessrun-egress",
				"paddock.dev/run":             run.Name,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"paddock.dev/run": run.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
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
			},
		},
	}
}

// ensureRunNetworkPolicy creates or updates the per-run NetworkPolicy
// when NetworkPolicyEnforce is "on" (or resolved-auto=on). Deletes any
// stale policy when enforcement flips off mid-run.
func (r *HarnessRunReconciler) ensureRunNetworkPolicy(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	if !r.networkPolicyEnforced() {
		return r.deleteRunNetworkPolicy(ctx, run)
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     r.ClusterPodCIDR,
		ClusterServiceCIDR: r.ClusterServiceCIDR,
	}
	desired := buildRunNetworkPolicy(run, cfg)
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desired.Name,
			Namespace: desired.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		if err := controllerutil.SetControllerReference(run, np, r.Scheme); err != nil {
			return err
		}
		np.Labels = desired.Labels
		np.Spec = desired.Spec
		return nil
	})
	if err != nil {
		return fmt.Errorf("upserting run NetworkPolicy: %w", err)
	}
	return nil
}

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
