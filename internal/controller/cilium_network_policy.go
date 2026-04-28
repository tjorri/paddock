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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// CiliumNetworkPolicyGVK is the GroupVersionKind for cilium.io/v2
// CiliumNetworkPolicy. Used to construct unstructured.Unstructured
// objects without taking a Go-type dependency on the Cilium API.
var CiliumNetworkPolicyGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2",
	Kind:    "CiliumNetworkPolicy",
}

// buildCiliumEgressPolicy mirrors buildEgressNetworkPolicy's rule set
// in CNP shape. The CNP differs from the standard NP in three places:
//   - selector and rule shapes use Cilium matchers (toEntities,
//     toEndpoints, toCIDR) instead of NetworkPolicyPeer;
//   - the kube-apiserver rule uses toEntities: [kube-apiserver,
//     remote-node] instead of an ipBlock allow-list (Phase 2d's
//     standard-NP ipBlock rule does not enforce against host-network
//     destinations on Cilium);
//   - selector key names use Cilium's k8s:* prefix in matchLabels for
//     namespace and pod selectors.
//
// The function returns *unstructured.Unstructured so the controller
// does not take a Go-type dependency on cilium.io/v2.
//
// Note: the CNP path intentionally does NOT consume cfg.APIServerIPs.
// The toEntities rule covers the kube-apiserver regardless of how its
// IP set rotates. Standard-NP path retains the ipBlock allow-list as
// a defence-in-depth fallback.
func buildCiliumEgressPolicy(
	selector metav1.LabelSelector,
	name, namespace string,
	labels map[string]string,
	cfg networkPolicyConfig,
) *unstructured.Unstructured {
	rules := []interface{}{
		// DNS to kube-dns.
		map[string]interface{}{
			"toEndpoints": []interface{}{
				map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"k8s:io.kubernetes.pod.namespace": "kube-system",
						"k8s-app":                         "kube-dns",
					},
				},
			},
			"toPorts": []interface{}{
				map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{"port": "53", "protocol": "UDP"},
						map[string]interface{}{"port": "53", "protocol": "TCP"},
					},
				},
			},
		},
		// Public-internet 443/80 with cluster-CIDR exclusions encoded
		// as toCIDRSet entries with `except`. Mirrors buildExceptCIDRs.
		map[string]interface{}{
			"toCIDRSet": ciliumPublicCIDRSet(cfg),
			"toPorts": []interface{}{
				map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{"port": "443", "protocol": "TCP"},
						map[string]interface{}{"port": "80", "protocol": "TCP"},
					},
				},
			},
		},
		// Loopback allow — required so iptables nat OUTPUT REDIRECT
		// from agent traffic on TCP 80/443 to the proxy at
		// 127.0.0.1:15001 is not dropped by Cilium-with-KPR's
		// CiliumNetworkPolicy enforcement (Issue #79). No `toPorts`
		// block: Cilium CNP requires a non-empty `port` value when
		// `toPorts` is set, so we omit it and accept the slightly
		// broader "any L4 to loopback" semantics. Pod-local loopback
		// is reachable only from within the same netns, so the wider
		// match has no security cost.
		map[string]interface{}{
			"toCIDR": []interface{}{"127.0.0.0/8"},
		},
	}
	if cfg.BrokerNamespace != "" {
		port := cfg.BrokerPort
		if port == 0 {
			port = 8443
		}
		rules = append(rules, map[string]interface{}{
			"toEndpoints": []interface{}{
				map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"k8s:io.kubernetes.pod.namespace": cfg.BrokerNamespace,
						"app.kubernetes.io/component":     "broker",
						"app.kubernetes.io/name":          "paddock",
					},
				},
			},
			"toPorts": []interface{}{
				map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{
							"port":     fmt.Sprintf("%d", port),
							"protocol": "TCP",
						},
					},
				},
			},
		})
	}
	// kube-apiserver / remote-node entity rule (the heart of the CNP variant).
	rules = append(rules, map[string]interface{}{
		"toEntities": []interface{}{"kube-apiserver", "remote-node"},
	})

	cnp := &unstructured.Unstructured{}
	cnp.SetGroupVersionKind(CiliumNetworkPolicyGVK)
	cnp.SetName(name)
	cnp.SetNamespace(namespace)
	cnp.SetLabels(labels)
	endpointSelector := map[string]interface{}{
		"matchLabels": stringMapToInterface(selector.MatchLabels),
	}
	_ = unstructured.SetNestedField(cnp.Object, endpointSelector, "spec", "endpointSelector")
	_ = unstructured.SetNestedSlice(cnp.Object, rules, "spec", "egress")
	return cnp
}

func stringMapToInterface(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ciliumPublicCIDRSet(cfg networkPolicyConfig) []interface{} {
	excepts := buildExceptCIDRs(cfg)
	exceptIface := make([]interface{}, 0, len(excepts))
	for _, e := range excepts {
		exceptIface = append(exceptIface, e)
	}
	return []interface{}{
		map[string]interface{}{
			"cidr":   "0.0.0.0/0",
			"except": exceptIface,
		},
	}
}

// buildRunCiliumNetworkPolicy renders the CNP variant of the per-run
// egress policy. Selector matches the run pod label
// (paddock.dev/run=<name>); rule list mirrors buildRunNetworkPolicy.
func buildRunCiliumNetworkPolicy(run *paddockv1alpha1.HarnessRun, cfg networkPolicyConfig) *unstructured.Unstructured {
	return buildCiliumEgressPolicy(
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

// buildSeedCiliumNetworkPolicy mirrors buildSeedNetworkPolicy.
func buildSeedCiliumNetworkPolicy(ws *paddockv1alpha1.Workspace, cfg networkPolicyConfig) *unstructured.Unstructured {
	return buildCiliumEgressPolicy(
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
