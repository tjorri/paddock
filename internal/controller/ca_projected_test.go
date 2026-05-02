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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestEnsureProxyTLS_EmitsCAProjectedOnCreate(t *testing.T) {
	scheme := schemeWithCertManager(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	rec := &capturedSink{}
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: rec},
		ProxyBrokerConfig: ProxyBrokerConfig{
			ProxyCAClusterIssuer: "paddock-proxy-ca-issuer",
		},
	}

	if _, err := r.ensureProxyTLS(context.Background(), run); err != nil {
		t.Fatalf("ensureProxyTLS: %v", err)
	}

	// First create: one ca-projected event.
	got := rec.all
	if len(got) != 1 || got[0].Spec.Kind != paddockv1alpha1.AuditKindCAProjected {
		t.Fatalf("got %d events, want one ca-projected; events=%+v", len(got), got)
	}
	rec.all = nil // reset between calls

	// Second call: no-op reconcile must NOT re-emit.
	if _, err := r.ensureProxyTLS(context.Background(), run); err != nil {
		t.Fatalf("ensureProxyTLS (idempotent): %v", err)
	}
	if got := rec.all; len(got) != 0 {
		t.Errorf("got %d events on no-op reconcile; want 0 (only Create emits)", len(got))
	}
}

func TestEnsureBrokerCA_EmitsCAProjectedOnCreate(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("paddock scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}

	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "paddock-broker-serving-cert", Namespace: "paddock-system"},
		Data:       map[string][]byte{"ca.crt": []byte("FAKE-CA")},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(srcSecret).Build()
	rec := &capturedSink{}
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: rec},
		ProxyBrokerConfig: ProxyBrokerConfig{
			BrokerEndpoint: "https://broker.paddock-system.svc:8443",
			ProxyImage:     "paddock-proxy:dev",
			BrokerCASource: BrokerCASource{Name: "paddock-broker-serving-cert", Namespace: "paddock-system"},
		},
	}

	ok, err := r.ensureBrokerCA(context.Background(), run)
	if err != nil {
		t.Fatalf("ensureBrokerCA: %v", err)
	}
	if !ok {
		t.Fatal("ensureBrokerCA returned ok=false on create")
	}
	if got := rec.all; len(got) != 1 || got[0].Spec.Kind != paddockv1alpha1.AuditKindCAProjected {
		t.Fatalf("got %d events, want one ca-projected; events=%+v", len(got), got)
	}
	rec.all = nil

	if _, err := r.ensureBrokerCA(context.Background(), run); err != nil {
		t.Fatalf("ensureBrokerCA (idempotent): %v", err)
	}
	if got := rec.all; len(got) != 0 {
		t.Errorf("got %d events on no-op reconcile; want 0", len(got))
	}
}
