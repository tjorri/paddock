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

// buildRunNetworkPolicy renders the defence-in-depth NetworkPolicy
// that rides alongside the proxy sidecar. The policy targets the run's
// Pod by label, permits:
//
//   - DNS (UDP+TCP 53) to kube-dns in kube-system — name resolution has
//     to work or the proxy cannot dial upstreams;
//   - TCP 443 and TCP 80 to any destination — the proxy sidecar's
//     outbound leg is the thing that actually connects, and hostname-
//     level policy is enforced by the proxy (M4+). The NetworkPolicy
//     narrows by port only, which is defence-in-depth against an
//     iptables-init bypass that somehow lands raw TCP.
//   - No other egress. Ingress is left permissive (Kubernetes default
//     allows everything not explicitly blocked); no one targets run
//     Pods on their cluster IP, so this is functionally a deny.
//
// The host-level allowlist the proxy enforces (from BrokerPolicy grants)
// could in principle be rendered into the NetworkPolicy as ipBlock
// rules, but DNS-driven upstream IPs rotate and re-rendering on
// resolution is a bigger engineering lift. Port-level is the right
// trade-off for v0.3.
func buildRunNetworkPolicy(run *paddockv1alpha1.HarnessRun) *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	dnsPort := intstr.FromInt32(53)
	httpsPort := intstr.FromInt32(443)
	httpPort := intstr.FromInt32(80)
	openCIDR := "0.0.0.0/0"

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
				// TCP 443 — where the proxy actually dials upstream,
				// and where every supported upstream (Anthropic, GitHub
				// Apps, OpenAI) terminates TLS.
				{
					To: []networkingv1.NetworkPolicyPeer{
						{IPBlock: &networkingv1.IPBlock{CIDR: openCIDR}},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &httpsPort},
					},
				},
				// TCP 80 — plain HTTP for git-clone fallbacks and the
				// rare upstream that still redirects 80→443 upstream.
				{
					To: []networkingv1.NetworkPolicyPeer{
						{IPBlock: &networkingv1.IPBlock{CIDR: openCIDR}},
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
// stale policy when enforcement flips off mid-run — the alternative
// leaves runs stuck after a chart downgrade.
func (r *HarnessRunReconciler) ensureRunNetworkPolicy(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	if !r.networkPolicyEnforced() {
		return r.deleteRunNetworkPolicy(ctx, run)
	}
	desired := buildRunNetworkPolicy(run)
	// Avoid hitting the API server for the CreateOrUpdate path when
	// we already have something with the correct shape. The spec we
	// produce is deterministic from run.Name so unchanged reconciles
	// no-op on the update.
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
