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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestCopyCAToSecret_PropagatesCABundle(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-serving-cert", Namespace: "paddock-system"},
		Data:       map[string][]byte{"ca.crt": []byte("PEM-BUNDLE")},
	}
	owner := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-x", Namespace: "team-a", UID: "u1"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src).Build()

	created, err := copyCAToSecret(context.Background(), cli, scheme, owner,
		types.NamespacedName{Namespace: "paddock-system", Name: "broker-serving-cert"},
		"run-x-broker-ca", "team-a",
		map[string]string{"app.kubernetes.io/component": "harnessrun-broker-ca"})
	if err != nil {
		t.Fatalf("copyCAToSecret: %v", err)
	}
	if !created {
		t.Errorf("expected created=true on first call")
	}

	var got corev1.Secret
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "run-x-broker-ca"}, &got); err != nil {
		t.Fatalf("get dst: %v", err)
	}
	if string(got.Data["ca.crt"]) != "PEM-BUNDLE" {
		t.Errorf("dst ca.crt = %q, want PEM-BUNDLE", got.Data["ca.crt"])
	}
	if got.Labels["app.kubernetes.io/component"] != "harnessrun-broker-ca" {
		t.Errorf("dst labels missing component label: %#v", got.Labels)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != "u1" {
		t.Errorf("owner ref not set: %#v", got.OwnerReferences)
	}
}

func TestCopyCAToSecret_SourceMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = paddockv1alpha1.AddToScheme(scheme)
	owner := &paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: "run-x", Namespace: "team-a", UID: "u1"}}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()

	created, err := copyCAToSecret(context.Background(), cli, scheme, owner,
		types.NamespacedName{Namespace: "paddock-system", Name: "missing"},
		"run-x-broker-ca", "team-a", nil)
	if err != nil {
		t.Fatalf("copyCAToSecret: %v", err)
	}
	if created {
		t.Errorf("expected created=false when source missing")
	}
}

func TestCopyCAToSecret_SourceEmpty(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = paddockv1alpha1.AddToScheme(scheme)
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-serving-cert", Namespace: "paddock-system"},
		Data:       map[string][]byte{"ca.crt": nil},
	}
	owner := &paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: "run-x", Namespace: "team-a", UID: "u1"}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src).Build()

	created, err := copyCAToSecret(context.Background(), cli, scheme, owner,
		types.NamespacedName{Namespace: "paddock-system", Name: "broker-serving-cert"},
		"run-x-broker-ca", "team-a", nil)
	if err != nil {
		t.Fatalf("copyCAToSecret: %v", err)
	}
	if created {
		t.Errorf("expected created=false when source ca.crt is empty")
	}
}
