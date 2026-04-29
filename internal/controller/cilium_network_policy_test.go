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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildCiliumEgressPolicy_HasKubeApiserverEntities(t *testing.T) {
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
		BrokerNamespace:    "paddock-system",
		BrokerPort:         8443,
	}
	cnp := buildCiliumEgressPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"paddock.dev/run": "demo"}},
		"demo-egress",
		"tenant",
		map[string]string{"app.kubernetes.io/name": "paddock"},
		cfg,
	)
	if cnp.GetAPIVersion() != "cilium.io/v2" || cnp.GetKind() != "CiliumNetworkPolicy" {
		t.Fatalf("apiVersion/kind: %s/%s", cnp.GetAPIVersion(), cnp.GetKind())
	}
	if cnp.GetName() != "demo-egress" || cnp.GetNamespace() != "tenant" {
		t.Fatalf("name/namespace: %s/%s", cnp.GetName(), cnp.GetNamespace())
	}
	egress, _, err := unstructured.NestedSlice(cnp.Object, "spec", "egress")
	if err != nil {
		t.Fatalf("read egress: %v", err)
	}
	var foundEntities, foundDNS, foundLoopback bool
	for _, raw := range egress {
		rule, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if ents, has, _ := unstructured.NestedStringSlice(rule, "toEntities"); has {
			seen := map[string]bool{}
			for _, e := range ents {
				seen[e] = true
			}
			if seen["kube-apiserver"] && seen["remote-node"] {
				foundEntities = true
			}
		}
		if eps, has, _ := unstructured.NestedSlice(rule, "toEndpoints"); has && len(eps) > 0 {
			ep0, _ := eps[0].(map[string]interface{})
			if ml, _, _ := unstructured.NestedStringMap(ep0, "matchLabels"); ml["k8s-app"] == "kube-dns" {
				foundDNS = true
			}
		}
		if cidrs, has, _ := unstructured.NestedSlice(rule, "toCIDR"); has {
			for _, raw := range cidrs {
				if s, ok := raw.(string); ok && s == "127.0.0.0/8" {
					foundLoopback = true
				}
			}
		}
	}
	if !foundEntities {
		t.Errorf("missing toEntities: [kube-apiserver, remote-node]")
	}
	if !foundDNS {
		t.Errorf("missing kube-dns rule")
	}
	if !foundLoopback {
		t.Errorf("missing toCIDR 127.0.0.0/8 (loopback) rule")
	}
}

func TestBuildCiliumEgressPolicy_NoBrokerRuleWhenNamespaceEmpty(t *testing.T) {
	cfg := networkPolicyConfig{}
	cnp := buildCiliumEgressPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"x": "y"}},
		"x", "y", nil, cfg,
	)
	egress, _, _ := unstructured.NestedSlice(cnp.Object, "spec", "egress")
	for _, raw := range egress {
		rule, _ := raw.(map[string]interface{})
		if eps, has, _ := unstructured.NestedSlice(rule, "toEndpoints"); has && len(eps) > 0 {
			ep0, _ := eps[0].(map[string]interface{})
			if ml, _, _ := unstructured.NestedStringMap(ep0, "matchLabels"); ml["app.kubernetes.io/component"] == "broker" {
				t.Fatalf("broker rule emitted with empty namespace")
			}
		}
	}
}
