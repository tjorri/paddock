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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestReconcile_PinsNetworkPolicyEnforcedAtFirstReconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a", ResourceVersion: "1"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()

	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		ProxyBrokerConfig: ProxyBrokerConfig{
			NetworkPolicyEnforce: NetworkPolicyEnforceOn,
		},
	}

	// First reconcile-pass equivalent: caller invokes the pin helper.
	if err := r.pinNetworkPolicyEnforced(context.Background(), run); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if run.Status.NetworkPolicyEnforced == nil || *run.Status.NetworkPolicyEnforced != true {
		t.Fatalf("after pin Status.NetworkPolicyEnforced = %v, want pointer-to-true", run.Status.NetworkPolicyEnforced)
	}

	// Second reconcile-pass with flag flipped: pin must NOT change.
	r.NetworkPolicyEnforce = NetworkPolicyEnforceOff
	if err := r.pinNetworkPolicyEnforced(context.Background(), run); err != nil {
		t.Fatalf("pin (idempotent): %v", err)
	}
	if run.Status.NetworkPolicyEnforced == nil || *run.Status.NetworkPolicyEnforced != true {
		t.Errorf("after second pin Status.NetworkPolicyEnforced = %v; want unchanged true (immutable after first pin)",
			run.Status.NetworkPolicyEnforced)
	}
}
