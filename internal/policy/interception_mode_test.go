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

package policy

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
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

func TestResolveInterceptionMode_PSA(t *testing.T) {
	cases := []struct {
		name     string
		psaLabel string
		want     paddockv1alpha1.InterceptionMode
	}{
		{"no label", "", paddockv1alpha1.InterceptionModeTransparent},
		{"privileged", PSALevelPrivileged, paddockv1alpha1.InterceptionModeTransparent},
		{"baseline blocks NET_ADMIN", PSALevelBaseline, paddockv1alpha1.InterceptionModeCooperative},
		{"restricted blocks NET_ADMIN", PSALevelRestricted, paddockv1alpha1.InterceptionModeCooperative},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
			if c.psaLabel != "" {
				ns.Labels = map[string]string{PSAEnforceLabel: c.psaLabel}
			}
			cli := fake.NewClientBuilder().
				WithScheme(newTestScheme(t)).
				WithObjects(ns).
				Build()
			mode, floor, err := ResolveInterceptionMode(context.Background(), cli, "test", nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != c.want {
				t.Errorf("mode = %q, want %q", mode, c.want)
			}
			if floor.Policy != "" {
				t.Errorf("floor should be empty without policies; got %+v", floor)
			}
		})
	}
}

func TestResolveInterceptionMode_RejectsDowngradeBelowFloor(t *testing.T) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "tenant",
			Labels: map[string]string{PSAEnforceLabel: PSALevelBaseline},
		},
	}
	policy := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "strict", Namespace: "tenant"},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates:  []string{"*"},
			MinInterceptionMode: paddockv1alpha1.InterceptionModeTransparent,
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(ns).
		Build()
	mode, floor, err := ResolveInterceptionMode(context.Background(), cli, "tenant", []*paddockv1alpha1.BrokerPolicy{policy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != paddockv1alpha1.InterceptionModeCooperative {
		t.Errorf("mode = %q, want cooperative (baseline forbids NET_ADMIN)", mode)
	}
	if floor.Policy != "strict" {
		t.Errorf("floor policy = %q, want strict", floor.Policy)
	}
}

func TestResolveInterceptionMode_AcceptsWhenFloorMet(t *testing.T) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "tenant",
			Labels: map[string]string{PSAEnforceLabel: PSALevelPrivileged},
		},
	}
	policy := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "strict", Namespace: "tenant"},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates:  []string{"*"},
			MinInterceptionMode: paddockv1alpha1.InterceptionModeTransparent,
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(ns).
		Build()
	mode, floor, err := ResolveInterceptionMode(context.Background(), cli, "tenant", []*paddockv1alpha1.BrokerPolicy{policy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != paddockv1alpha1.InterceptionModeTransparent {
		t.Errorf("mode = %q, want transparent", mode)
	}
	if floor.Policy != "" {
		t.Errorf("floor should be satisfied; got %+v", floor)
	}
}

// TestResolveInterceptionMode_NoFloorOnCooperativeFloor confirms that a
// cooperative floor is always satisfiable — even a restricted namespace.
// It exists to prevent future refactors from flipping the modeCovers
// logic.
func TestResolveInterceptionMode_NoFloorOnCooperativeFloor(t *testing.T) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "tenant",
			Labels: map[string]string{PSAEnforceLabel: PSALevelRestricted},
		},
	}
	policy := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "loose", Namespace: "tenant"},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates:  []string{"*"},
			MinInterceptionMode: paddockv1alpha1.InterceptionModeCooperative,
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(ns).
		Build()
	mode, floor, err := ResolveInterceptionMode(context.Background(), cli, "tenant", []*paddockv1alpha1.BrokerPolicy{policy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != paddockv1alpha1.InterceptionModeCooperative {
		t.Errorf("mode = %q, want cooperative", mode)
	}
	if floor.Policy != "" {
		t.Errorf("cooperative floor should be met; got %+v", floor)
	}
}
