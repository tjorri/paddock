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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func cniProbeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1 add: %v", err)
	}
	return s
}

func newCNIPod(name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: KubeSystemNamespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "x", Image: "x"}},
		},
	}
}

func TestDetectNetworkPolicyCNI(t *testing.T) {
	cases := []struct {
		name       string
		pods       []*corev1.Pod
		wantFound  bool
		wantReason string
	}{
		{
			name:       "no CNI pods -> off",
			pods:       nil,
			wantFound:  false,
			wantReason: "no known NetworkPolicy-capable CNI",
		},
		{
			name: "kindnet only -> off",
			pods: []*corev1.Pod{
				newCNIPod("kindnet-abc", map[string]string{"app": "kindnet"}),
			},
			wantFound:  false,
			wantReason: "no known NetworkPolicy-capable CNI",
		},
		{
			name: "calico -> on",
			pods: []*corev1.Pod{
				newCNIPod("calico-node-xyz", map[string]string{"k8s-app": "calico-node"}),
			},
			wantFound:  true,
			wantReason: "calico-node",
		},
		{
			name: "cilium (k8s-app label) -> on",
			pods: []*corev1.Pod{
				newCNIPod("cilium-0", map[string]string{"k8s-app": "cilium"}),
			},
			wantFound:  true,
			wantReason: "cilium",
		},
		{
			name: "cilium (app label) -> on",
			pods: []*corev1.Pod{
				newCNIPod("cilium-0", map[string]string{"app": "cilium"}),
			},
			wantFound:  true,
			wantReason: "cilium",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			objs := []runtime.Object{}
			for _, p := range c.pods {
				objs = append(objs, p)
			}
			cli := fake.NewClientBuilder().
				WithScheme(cniProbeScheme(t)).
				WithRuntimeObjects(objs...).
				Build()
			enforced, reason, err := DetectNetworkPolicyCNI(context.Background(), cli)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if enforced != c.wantFound {
				t.Errorf("enforced = %v, want %v; reason=%q", enforced, c.wantFound, reason)
			}
			if !strings.Contains(reason, c.wantReason) {
				t.Errorf("reason = %q, want substring %q", reason, c.wantReason)
			}
		})
	}
}
