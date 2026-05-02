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

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/policy"
)

func newControllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1 add: %v", err)
	}
	if err := paddockv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("paddock add: %v", err)
	}
	return s
}

// TestReconcilerResolveInterception_PassesPolicyDecisionThrough asserts
// the reconciler method forwards the policy resolver's decision when
// IPTablesInitImage is configured: cooperative-opt-in in a restricted
// namespace yields cooperative.
func TestReconcilerResolveInterception_PassesPolicyDecisionThrough(t *testing.T) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "restricted-ns",
			Labels: map[string]string{policy.PSAEnforceLabel: policy.PSALevelRestricted},
		},
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bp", Namespace: "restricted-ns"},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			Interception: &paddockv1alpha1.InterceptionSpec{
				CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
					Accepted: true,
					Reason:   "Cluster PSA=restricted; node-level proxy not available yet",
				},
			},
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(newControllerTestScheme(t)).
		WithObjects(ns, bp).
		Build()
	r := &HarnessRunReconciler{
		Client:            cli,
		IPTablesInitImage: "paddock-iptables-init:test",
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "restricted-ns"},
	}
	tpl := &resolvedTemplate{SourceName: "echo"}

	got, err := r.resolveInterceptionMode(context.Background(), run, tpl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Unavailable {
		t.Fatalf("expected available decision; got Unavailable=true reason=%q", got.Reason)
	}
	if got.Mode != paddockv1alpha1.InterceptionModeCooperative {
		t.Errorf("Mode = %q, want cooperative", got.Mode)
	}
}

// TestReconcilerResolveInterception_FailsClosedOnPSABlock asserts that a
// default-transparent policy in a PSA=restricted namespace surfaces
// Unavailable — the reconciler does not silently downgrade.
func TestReconcilerResolveInterception_FailsClosedOnPSABlock(t *testing.T) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "restricted-ns",
			Labels: map[string]string{policy.PSAEnforceLabel: policy.PSALevelRestricted},
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(newControllerTestScheme(t)).
		WithObjects(ns).
		Build()
	r := &HarnessRunReconciler{
		Client:            cli,
		IPTablesInitImage: "paddock-iptables-init:test",
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "restricted-ns"},
	}
	tpl := &resolvedTemplate{SourceName: "echo"}

	got, err := r.resolveInterceptionMode(context.Background(), run, tpl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Unavailable {
		t.Fatalf("expected Unavailable=true; got Mode=%q", got.Mode)
	}
	if !strings.Contains(got.Reason, "PSA") && !strings.Contains(got.Reason, "NET_ADMIN") {
		t.Errorf("reason should explain PSA/NET_ADMIN cause; got %q", got.Reason)
	}
}

// TestReconcilerResolveInterception_FailsClosedWhenIptablesImageMissing
// asserts that even on PSA=privileged, a transparent-required run
// surfaces Unavailable when the manager was started without
// --iptables-init-image.
func TestReconcilerResolveInterception_FailsClosedWhenIptablesImageMissing(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "privileged-ns"}}
	cli := fake.NewClientBuilder().
		WithScheme(newControllerTestScheme(t)).
		WithObjects(ns).
		Build()
	r := &HarnessRunReconciler{
		Client:            cli,
		IPTablesInitImage: "", // operator flag unset
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "privileged-ns"},
	}
	tpl := &resolvedTemplate{SourceName: "echo"}

	got, err := r.resolveInterceptionMode(context.Background(), run, tpl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Unavailable {
		t.Fatalf("expected Unavailable=true; got Mode=%q", got.Mode)
	}
	if !strings.Contains(got.Reason, "iptables-init-image") {
		t.Errorf("reason should name the --iptables-init-image flag; got %q", got.Reason)
	}
}

// TestReconcilerResolveInterception_CooperativeSkipsIptablesImageCheck
// asserts that an explicit cooperative opt-in is unaffected by the
// manager's iptables-init-image configuration (it doesn't need it).
func TestReconcilerResolveInterception_CooperativeSkipsIptablesImageCheck(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "bp", Namespace: "ns"},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			Interception: &paddockv1alpha1.InterceptionSpec{
				CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
					Accepted: true,
					Reason:   "Cluster PSA=restricted; node-level proxy not available yet",
				},
			},
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(newControllerTestScheme(t)).
		WithObjects(ns, bp).
		Build()
	r := &HarnessRunReconciler{Client: cli, IPTablesInitImage: ""}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"},
	}
	tpl := &resolvedTemplate{SourceName: "echo"}

	got, err := r.resolveInterceptionMode(context.Background(), run, tpl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Unavailable {
		t.Fatalf("expected available; got Unavailable=true reason=%q", got.Reason)
	}
	if got.Mode != paddockv1alpha1.InterceptionModeCooperative {
		t.Errorf("Mode = %q, want cooperative", got.Mode)
	}
}
