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

package session

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestEnd_DeletesLabeledWorkspace(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "starlight",
			Namespace: "default",
			Labels:    map[string]string{SessionLabel: SessionLabelTrue},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(ws).Build()

	if err := End(context.Background(), cli, "default", "starlight"); err != nil {
		t.Fatalf("End: %v", err)
	}
	var got paddockv1alpha1.Workspace
	err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "starlight"}, &got)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound after End, got err=%v", err)
	}
}

func TestEnd_NotASession(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "non-session", Namespace: "default"},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(ws).Build()
	if err := End(context.Background(), cli, "default", "non-session"); err == nil {
		t.Fatal("expected error for non-session workspace, got nil")
	}
}
