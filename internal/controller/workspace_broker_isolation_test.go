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

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// TestEnsureSeedProxyTLS_PerWorkspaceIsolation is the seed-path
// equivalent of TestEnsureProxyTLS_PerRunIsolation. Two Workspaces in
// different tenant namespaces must produce distinct Certificate
// resources. F-18 / Phase 2f.
func TestEnsureSeedProxyTLS_PerWorkspaceIsolation(t *testing.T) {
	scheme := schemeWithCertManager(t)
	wsA := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-x", Namespace: "tenant-a", UID: "uid-a"},
	}
	wsB := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-y", Namespace: "tenant-b", UID: "uid-b"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(wsA, wsB).Build()
	r := &WorkspaceReconciler{
		Client: cli,
		Scheme: scheme,
		ProxyBrokerConfig: ProxyBrokerConfig{
			ProxyCAClusterIssuer: "paddock-proxy-ca-issuer",
		},
	}

	if _, err := r.ensureSeedProxyTLS(context.Background(), wsA); err != nil {
		t.Fatalf("ensureSeedProxyTLS wsA: %v", err)
	}
	if _, err := r.ensureSeedProxyTLS(context.Background(), wsB); err != nil {
		t.Fatalf("ensureSeedProxyTLS wsB: %v", err)
	}

	var certA, certB cmapi.Certificate
	keyA := types.NamespacedName{Name: workspaceProxyTLSSecretName(wsA.Name), Namespace: wsA.Namespace}
	keyB := types.NamespacedName{Name: workspaceProxyTLSSecretName(wsB.Name), Namespace: wsB.Namespace}
	if err := cli.Get(context.Background(), keyA, &certA); err != nil {
		t.Fatalf("get Certificate wsA: %v", err)
	}
	if err := cli.Get(context.Background(), keyB, &certB); err != nil {
		t.Fatalf("get Certificate wsB: %v", err)
	}

	if certA.Namespace == certB.Namespace {
		t.Errorf("Workspace Certificates in same namespace (%q); F-18 regression", certA.Namespace)
	}
	if certA.Name == certB.Name {
		t.Errorf("Workspace Certificates have same name (%q); F-18 regression", certA.Name)
	}
	if certA.Spec.SecretName == certB.Spec.SecretName {
		t.Errorf("Workspace Certificates produce same backing Secret (%q); F-18 regression", certA.Spec.SecretName)
	}
	if len(certA.OwnerReferences) == 0 || len(certB.OwnerReferences) == 0 {
		t.Fatalf("missing OwnerReferences: A=%+v B=%+v", certA.OwnerReferences, certB.OwnerReferences)
	}
	if certA.OwnerReferences[0].UID == certB.OwnerReferences[0].UID {
		t.Errorf("Workspace Certificates owned by same parent UID; F-18 regression")
	}
}
