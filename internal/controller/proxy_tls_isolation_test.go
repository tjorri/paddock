/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"testing"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// TestEnsureProxyTLS_PerRunIsolation is the load-bearing F-18
// anti-regression check. Two HarnessRuns in different tenant namespaces
// must produce DISTINCT Certificate resources (different names AND
// different namespaces — neither shared).
//
// If a future refactor goes back to byte-copying a single source
// Secret into both tenant namespaces, this test will detect the
// regression by observing identical Certificate resource references —
// or, in the worst case, identical backing Secrets across runs (the
// original F-18 shape).
//
// We can't compare cert-manager-emitted private-key bytes here because
// the fake client doesn't run cert-manager. The strict-resource-
// separation assertion is the achievable load-bearing check; in
// production, cert-manager guarantees each Certificate produces
// independently-generated keys. F-18 / Phase 2f.
func TestEnsureProxyTLS_PerRunIsolation(t *testing.T) {
	scheme := schemeWithCertManager(t)
	runA := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-x", Namespace: "tenant-a", UID: "uid-a"},
	}
	runB := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-y", Namespace: "tenant-b", UID: "uid-b"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runA, runB).Build()
	r := &HarnessRunReconciler{
		Client:               cli,
		Scheme:               scheme,
		Audit:                &ControllerAudit{Sink: &capturedSink{}},
		ProxyCAClusterIssuer: "paddock-proxy-ca-issuer",
	}

	if _, err := r.ensureProxyTLS(context.Background(), runA); err != nil {
		t.Fatalf("ensureProxyTLS runA: %v", err)
	}
	if _, err := r.ensureProxyTLS(context.Background(), runB); err != nil {
		t.Fatalf("ensureProxyTLS runB: %v", err)
	}

	var certA, certB cmapi.Certificate
	keyA := types.NamespacedName{Name: proxyTLSSecretName(runA.Name), Namespace: runA.Namespace}
	keyB := types.NamespacedName{Name: proxyTLSSecretName(runB.Name), Namespace: runB.Namespace}
	if err := cli.Get(context.Background(), keyA, &certA); err != nil {
		t.Fatalf("get Certificate runA: %v", err)
	}
	if err := cli.Get(context.Background(), keyB, &certB); err != nil {
		t.Fatalf("get Certificate runB: %v", err)
	}

	// Resource separation: distinct namespaces (the F-18 fix property).
	if certA.Namespace == certB.Namespace {
		t.Errorf("Certificates in same namespace (%q); F-18 regression — runs must each get their own", certA.Namespace)
	}
	// Distinct names within their namespaces.
	if certA.Name == certB.Name {
		t.Errorf("Certificates have same name (%q); F-18 regression — names must be per-run", certA.Name)
	}
	// Distinct backing Secrets (secretName is per-run).
	if certA.Spec.SecretName == certB.Spec.SecretName {
		t.Errorf("Certificates produce same backing Secret name (%q); F-18 regression", certA.Spec.SecretName)
	}
	// Distinct OwnerReferences (each Cert owned by its parent HarnessRun).
	if len(certA.OwnerReferences) == 0 || len(certB.OwnerReferences) == 0 {
		t.Fatalf("missing OwnerReferences: A=%+v B=%+v", certA.OwnerReferences, certB.OwnerReferences)
	}
	if certA.OwnerReferences[0].UID == certB.OwnerReferences[0].UID {
		t.Errorf("Certificates owned by same parent UID; F-18 regression")
	}
}
