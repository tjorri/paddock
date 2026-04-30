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

package broker

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// TestStartForwarder_NoPods covers correctness concern #1 from the
// portforward.go doc: a Service with zero Pods backing it must surface
// a fast error rather than hanging in dial. We exercise the
// Pod-resolution branch only — the real SPDY tunnel is e2e territory.
func TestStartForwarder_NoPods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	kc := fake.NewSimpleClientset()
	cfg := &rest.Config{Host: "https://127.0.0.1"}

	_, err := startForwarder(ctx, kc, cfg, "paddock-system", "paddock-broker", 8443)
	if err == nil {
		t.Fatal("expected error when no Pods back the service, got nil")
	}
	if !strings.Contains(err.Error(), "no Ready pod") {
		t.Errorf("expected 'no Ready pod' in error, got %q", err.Error())
	}
}

// TestStartForwarder_NoRunningPod covers the second leg of correctness
// concern #1: a Pod exists but is not in the Running phase.
func TestStartForwarder_NoRunningPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "paddock-broker-xyz",
			Namespace: "paddock-system",
			Labels:    map[string]string{"app.kubernetes.io/name": "paddock-broker"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	kc := fake.NewSimpleClientset(pending)
	cfg := &rest.Config{Host: "https://127.0.0.1"}

	_, err := startForwarder(ctx, kc, cfg, "paddock-system", "paddock-broker", 8443)
	if err == nil {
		t.Fatal("expected error when only Pending Pods exist, got nil")
	}
	if !strings.Contains(err.Error(), "no Ready pod") {
		t.Errorf("expected 'no Ready pod' in error, got %q", err.Error())
	}
}
