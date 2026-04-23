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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubeSystemNamespace is where Kubernetes distros place CNI
// DaemonSets. We look here to identify the CNI; nothing else in the
// controller reads this namespace.
const KubeSystemNamespace = "kube-system"

// cniNetworkPolicyKnownLabels lists label selectors for DaemonSets /
// Deployments whose presence in kube-system indicates a CNI that
// enforces NetworkPolicy. Matched against *pod* labels (the DaemonSet-
// managed pods carry the same k8s-app label as their owner).
//
// Ordered by prevalence in practice; the probe returns on first match.
var cniNetworkPolicyKnownLabels = []map[string]string{
	{"k8s-app": "calico-node"},
	{"k8s-app": "cilium"},
	{"app": "cilium"},
	{"name": "weave-net"},
	{"k8s-app": "kube-router"},
	{"app.kubernetes.io/name": "antrea-agent"},
}

// DetectNetworkPolicyCNI inspects kube-system for well-known CNI pods
// whose presence indicates NetworkPolicy is actually enforced. Returns
// (enforced, reason) where reason is either the matched selector
// (on enforcement) or a diagnostic ("no known NP-capable CNI" / error).
//
// Called once at manager startup by cmd/main.go when
// --networkpolicy-enforce=auto. Production installs usually set
// on / off explicitly; auto is for the default chart install where
// the operator hasn't yet decided.
//
// Uses a client.Reader (not client.Client) because cmd/main.go runs
// the probe before the manager's controller cache is primed.
func DetectNetworkPolicyCNI(ctx context.Context, c client.Reader) (bool, string, error) {
	for _, sel := range cniNetworkPolicyKnownLabels {
		var pods corev1.PodList
		if err := c.List(ctx, &pods,
			client.InNamespace(KubeSystemNamespace),
			client.MatchingLabels(sel),
			client.Limit(1),
		); err != nil {
			return false, "", fmt.Errorf("listing kube-system pods: %w", err)
		}
		if len(pods.Items) > 0 {
			return true, fmt.Sprintf("%v", sel), nil
		}
	}
	return false, "no known NetworkPolicy-capable CNI DaemonSet found in kube-system " +
		"(searched calico-node, cilium, weave-net, kube-router, antrea-agent)", nil
}
