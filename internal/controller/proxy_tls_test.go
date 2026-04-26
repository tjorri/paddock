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

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// schemeWithCertManager builds a runtime.Scheme registering paddock,
// corev1, AND cert-manager v1 types — the shape every Phase 2f test
// needs for the fake client to round-trip Certificate resources.
func schemeWithCertManager(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("paddock scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}
	if err := cmapi.AddToScheme(s); err != nil {
		t.Fatalf("cert-manager scheme: %v", err)
	}
	return s
}

// TestEnsureProxyTLS_CreatesCertificate verifies that a reconcile pass
// creates a cert-manager Certificate resource in the run's namespace
// with the right spec shape: isCA=true, ECDSA P-256, OwnerReference
// to the HarnessRun, secretName=<run>-proxy-tls, ClusterIssuer ref to
// the configured issuer name, duration=48h, no renewBefore.
func TestEnsureProxyTLS_CreatesCertificate(t *testing.T) {
	scheme := schemeWithCertManager(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: &capturedSink{}},
		ProxyBrokerConfig: ProxyBrokerConfig{
			ProxyCAClusterIssuer: "paddock-proxy-ca-issuer",
		},
	}

	if _, err := r.ensureProxyTLS(context.Background(), run); err != nil {
		t.Fatalf("ensureProxyTLS: %v", err)
	}

	var got cmapi.Certificate
	key := types.NamespacedName{Name: proxyTLSSecretName(run.Name), Namespace: run.Namespace}
	if err := cli.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get Certificate %s: %v", key, err)
	}

	if !got.Spec.IsCA {
		t.Errorf("Certificate.spec.isCA = false, want true")
	}
	if got.Spec.SecretName != proxyTLSSecretName(run.Name) {
		t.Errorf("Certificate.spec.secretName = %q, want %q", got.Spec.SecretName, proxyTLSSecretName(run.Name))
	}
	if got.Spec.IssuerRef.Kind != "ClusterIssuer" {
		t.Errorf("Certificate.spec.issuerRef.kind = %q, want ClusterIssuer", got.Spec.IssuerRef.Kind)
	}
	if got.Spec.IssuerRef.Name != "paddock-proxy-ca-issuer" {
		t.Errorf("Certificate.spec.issuerRef.name = %q, want paddock-proxy-ca-issuer", got.Spec.IssuerRef.Name)
	}
	if got.Spec.PrivateKey == nil || got.Spec.PrivateKey.Algorithm != cmapi.ECDSAKeyAlgorithm {
		t.Errorf("Certificate.spec.privateKey.algorithm = %v, want ECDSA", got.Spec.PrivateKey)
	}
	if got.Spec.PrivateKey == nil || got.Spec.PrivateKey.Size != 256 {
		t.Errorf("Certificate.spec.privateKey.size = %v, want 256", got.Spec.PrivateKey)
	}
	if got.Spec.Duration == nil || got.Spec.Duration.Hours() != 48 {
		t.Errorf("Certificate.spec.duration = %v, want 48h", got.Spec.Duration)
	}
	if got.Spec.RenewBefore != nil {
		t.Errorf("Certificate.spec.renewBefore = %v, want nil (runs are bounded; no renewal)", got.Spec.RenewBefore)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].Name != run.Name {
		t.Errorf("Certificate.ownerReferences = %+v, want one ref to %s", got.OwnerReferences, run.Name)
	}
}

// TestEnsureProxyTLS_PendingWhenCertNotReady asserts ok=false when the
// Certificate exists but its Ready condition is not True.
func TestEnsureProxyTLS_PendingWhenCertNotReady(t *testing.T) {
	scheme := schemeWithCertManager(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-2", Namespace: "team-b"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: &capturedSink{}},
		ProxyBrokerConfig: ProxyBrokerConfig{
			ProxyCAClusterIssuer: "paddock-proxy-ca-issuer",
		},
	}

	ok, err := r.ensureProxyTLS(context.Background(), run)
	if err != nil {
		t.Fatalf("ensureProxyTLS: %v", err)
	}
	if ok {
		t.Errorf("ensureProxyTLS ok = true, want false (Certificate not Ready)")
	}
}

// TestEnsureProxyTLS_ReadyAfterCertIssued asserts ok=true once the
// Certificate's Ready condition flips to True (stub for cert-manager).
func TestEnsureProxyTLS_ReadyAfterCertIssued(t *testing.T) {
	scheme := schemeWithCertManager(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-3", Namespace: "team-c"},
	}
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(&cmapi.Certificate{}).
		Build()
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: &capturedSink{}},
		ProxyBrokerConfig: ProxyBrokerConfig{
			ProxyCAClusterIssuer: "paddock-proxy-ca-issuer",
		},
	}

	// First pass: creates the Certificate; ok=false because not Ready.
	if _, err := r.ensureProxyTLS(context.Background(), run); err != nil {
		t.Fatalf("ensureProxyTLS pass 1: %v", err)
	}

	// Stub cert-manager: flip Ready=True on the Certificate's status.
	var cert cmapi.Certificate
	key := types.NamespacedName{Name: proxyTLSSecretName(run.Name), Namespace: run.Namespace}
	if err := cli.Get(context.Background(), key, &cert); err != nil {
		t.Fatalf("get Certificate: %v", err)
	}
	cert.Status.Conditions = []cmapi.CertificateCondition{
		{Type: cmapi.CertificateConditionReady, Status: cmmeta.ConditionTrue},
	}
	if err := cli.Status().Update(context.Background(), &cert); err != nil {
		t.Fatalf("status update: %v", err)
	}

	// Second pass: ok=true.
	ok, err := r.ensureProxyTLS(context.Background(), run)
	if err != nil {
		t.Fatalf("ensureProxyTLS pass 2: %v", err)
	}
	if !ok {
		t.Errorf("ensureProxyTLS ok = false, want true (Ready=True)")
	}
}
