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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// TestReconcile_SeedRejected_ControllerGate verifies the defence-in-depth gate
// (F-46) that lives in the controller's default seed branch. A Workspace whose
// seed repo uses a non-allowlisted scheme (git://) can bypass admission by
// writing directly to the API; the controller must:
//   - transition Phase to Failed (terminal, no requeue),
//   - set Seeded=False with Reason=SeedRejected,
//   - not create any seed Job.
func TestReconcile_SeedRejected_ControllerGate(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("paddock scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("batchv1 scheme: %v", err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatalf("networkingv1 scheme: %v", err)
	}

	// Pre-populate the workspace with the finalizer already set so the
	// first Reconcile call skips the finalizer-install pass and goes
	// straight to the seed gate.
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "bad-scheme-ws",
			Namespace:       "team-a",
			ResourceVersion: "1",
			Finalizers:      []string{WorkspaceFinalizer},
		},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{
					{URL: "git://example.com/foo.git"},
				},
			},
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ws).
		WithStatusSubresource(ws).
		Build()

	r := &WorkspaceReconciler{
		Client:   cli,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(8),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	// Terminal: no requeue, no RequeueAfter.
	if result.Requeue || result.RequeueAfter != 0 {
		t.Errorf("result = %+v; want no requeue (terminal)", result)
	}

	// Re-fetch the workspace to see the updated status.
	var got paddockv1alpha1.Workspace
	if err := cli.Get(context.Background(),
		types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}, &got); err != nil {
		t.Fatalf("Get workspace after reconcile: %v", err)
	}

	// Phase must be Failed.
	if got.Status.Phase != paddockv1alpha1.WorkspacePhaseFailed {
		t.Errorf("Status.Phase = %q, want %q", got.Status.Phase, paddockv1alpha1.WorkspacePhaseFailed)
	}

	// Seeded condition must be False with Reason=SeedRejected.
	var seededCond *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == paddockv1alpha1.WorkspaceConditionSeeded {
			seededCond = &got.Status.Conditions[i]
			break
		}
	}
	if seededCond == nil {
		t.Fatalf("no %q condition found; got conditions: %+v",
			paddockv1alpha1.WorkspaceConditionSeeded, got.Status.Conditions)
	}
	if seededCond.Status != metav1.ConditionFalse {
		t.Errorf("Seeded condition Status = %q, want %q", seededCond.Status, metav1.ConditionFalse)
	}
	if seededCond.Reason != "SeedRejected" {
		t.Errorf("Seeded condition Reason = %q, want SeedRejected", seededCond.Reason)
	}

	// No seed Job must have been created.
	var jobs batchv1.JobList
	if err := cli.List(context.Background(), &jobs); err != nil {
		t.Fatalf("List jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Errorf("expected no Jobs; got %d: %v", len(jobs.Items), jobs.Items)
	}
}
