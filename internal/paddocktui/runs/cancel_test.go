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

package runs

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestCancel_DeletesRun(t *testing.T) {
	hr := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "default"},
		Status:     paddockv1alpha1.HarnessRunStatus{Phase: paddockv1alpha1.HarnessRunPhaseRunning},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(hr).Build()
	if err := Cancel(context.Background(), cli, "default", "hr-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// kubectl paddock cancel deletes the run; assert it is gone.
	var got paddockv1alpha1.HarnessRun
	err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "hr-1"}, &got)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound after cancel, got err=%v object=%+v", err, got)
	}
}

func TestCancel_NotFoundIsError(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	err := Cancel(context.Background(), cli, "default", "hr-missing")
	if err == nil {
		t.Fatal("expected error cancelling non-existent run, got nil")
	}
}
