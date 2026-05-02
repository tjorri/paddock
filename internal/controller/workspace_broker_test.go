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
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestEnsureSeedBrokerCA_TerminalOnEmptyKey(t *testing.T) {
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-serving-cert", Namespace: "paddock-system"},
		Data:       map[string][]byte{},
	}
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = paddockv1alpha1.AddToScheme(scheme)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src, ws).Build()
	r := &WorkspaceReconciler{
		Client: cli,
		Scheme: scheme,
		ProxyBrokerConfig: ProxyBrokerConfig{
			BrokerCASource: BrokerCASource{Name: "broker-serving-cert", Namespace: "paddock-system"},
		},
	}
	ok, err := r.ensureSeedBrokerCA(context.Background(), ws)
	if ok {
		t.Errorf("ok = true; want false on empty source")
	}
	if !errors.Is(err, errSourceCAMisconfigured) {
		t.Errorf("err = %v, want errSourceCAMisconfigured", err)
	}
}

func TestEnsureSeedBrokerCA_TransientOnSourceNotFound(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = paddockv1alpha1.AddToScheme(scheme)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).Build()
	r := &WorkspaceReconciler{
		Client: cli,
		Scheme: scheme,
		ProxyBrokerConfig: ProxyBrokerConfig{
			BrokerCASource: BrokerCASource{Name: "absent", Namespace: "paddock-system"},
		},
	}
	ok, err := r.ensureSeedBrokerCA(context.Background(), ws)
	if ok || err != nil {
		t.Errorf("ok=%v err=%v; want ok=false err=nil (transient)", ok, err)
	}
}
