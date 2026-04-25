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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func newTestRun(name, ns string) *paddockv1alpha1.HarnessRun {
	return &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       ns,
			ResourceVersion: "1",
		},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
		},
	}
}

// TestCommitStatus_TerminalFlipEmitsRunFailedAndRunCompleted verifies that
// when commitStatus flips a run from a non-terminal phase to Failed, it
// emits exactly one run-failed AuditEvent and one run-completed{denied}
// AuditEvent (F-40 Site 2+3).
func TestCommitStatus_TerminalFlipEmitsRunFailedAndRunCompleted(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	run := newTestRun("hr-1", "team-a")
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	rec := &capturedSink{}
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: rec},
	}

	// orig: Pending (non-terminal); flip to Failed (terminal).
	orig := run.Status.DeepCopy()
	now := metav1.Now()
	run.Status.Phase = paddockv1alpha1.HarnessRunPhaseFailed
	run.Status.CompletionTime = &now
	run.Status.Conditions = []metav1.Condition{
		{
			Type:               paddockv1alpha1.HarnessRunConditionCompleted,
			Status:             metav1.ConditionTrue,
			Reason:             "BrokerDenied",
			Message:            "no policy grants this credential",
			LastTransitionTime: now,
		},
	}

	if _, err := r.commitStatus(context.Background(), run, orig); err != nil {
		t.Fatalf("commitStatus: %v", err)
	}

	got := rec.all
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (run-failed + run-completed); events=%v", len(got), kindList(got))
	}

	gotKinds := map[paddockv1alpha1.AuditKind]int{}
	for _, e := range got {
		gotKinds[e.Spec.Kind]++
	}
	if gotKinds[paddockv1alpha1.AuditKindRunFailed] != 1 {
		t.Errorf("run-failed = %d, want 1; kinds=%v", gotKinds[paddockv1alpha1.AuditKindRunFailed], gotKinds)
	}
	if gotKinds[paddockv1alpha1.AuditKindRunCompleted] != 1 {
		t.Errorf("run-completed = %d, want 1; kinds=%v", gotKinds[paddockv1alpha1.AuditKindRunCompleted], gotKinds)
	}

	// Verify the run-completed carries denied decision.
	for _, e := range got {
		if e.Spec.Kind == paddockv1alpha1.AuditKindRunCompleted {
			if e.Spec.Decision != paddockv1alpha1.AuditDecisionDenied {
				t.Errorf("run-completed decision = %q, want denied", e.Spec.Decision)
			}
		}
	}
}

// TestCommitStatus_NoFlipIfAlreadyTerminal verifies that when orig phase is
// already terminal, no AuditEvents are emitted (idempotency across requeues).
func TestCommitStatus_NoFlipIfAlreadyTerminal(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	run := newTestRun("hr-1", "team-a")
	run.Status.Phase = paddockv1alpha1.HarnessRunPhaseFailed
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	rec := &capturedSink{}
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: rec},
	}

	orig := run.Status.DeepCopy() // already Failed (terminal)
	// Add a condition: status changes but phase stays terminal.
	now := metav1.Now()
	run.Status.Conditions = append(run.Status.Conditions, metav1.Condition{
		Type:               "Anything",
		Status:             metav1.ConditionTrue,
		Reason:             "Reason",
		Message:            "msg",
		LastTransitionTime: now,
	})

	if _, err := r.commitStatus(context.Background(), run, orig); err != nil {
		t.Fatalf("commitStatus: %v", err)
	}
	if got := rec.all; len(got) != 0 {
		t.Errorf("got %d events; expected 0 because orig was already terminal", len(got))
	}
}

// TestCommitStatus_SucceededEmitsRunCompletedGranted verifies a Succeeded
// flip emits only run-completed{granted} with no run-failed.
func TestCommitStatus_SucceededEmitsRunCompletedGranted(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	run := newTestRun("hr-1", "team-a")
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	rec := &capturedSink{}
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: rec},
	}

	orig := run.Status.DeepCopy()
	now := metav1.Now()
	run.Status.Phase = paddockv1alpha1.HarnessRunPhaseSucceeded
	run.Status.CompletionTime = &now

	if _, err := r.commitStatus(context.Background(), run, orig); err != nil {
		t.Fatalf("commitStatus: %v", err)
	}

	got := rec.all
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (run-completed only); events=%v", len(got), kindList(got))
	}
	if got[0].Spec.Kind != paddockv1alpha1.AuditKindRunCompleted {
		t.Errorf("kind = %q, want run-completed", got[0].Spec.Kind)
	}
	if got[0].Spec.Decision != paddockv1alpha1.AuditDecisionGranted {
		t.Errorf("decision = %q, want granted", got[0].Spec.Decision)
	}
}

// TestCommitStatus_CancelledEmitsRunCompletedWarned verifies a Cancelled
// flip emits only run-completed{warned} with no run-failed.
func TestCommitStatus_CancelledEmitsRunCompletedWarned(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	run := newTestRun("hr-1", "team-a")
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	rec := &capturedSink{}
	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: rec},
	}

	orig := run.Status.DeepCopy()
	now := metav1.Now()
	run.Status.Phase = paddockv1alpha1.HarnessRunPhaseCancelled
	run.Status.CompletionTime = &now
	run.Status.Conditions = []metav1.Condition{
		{
			Type:               paddockv1alpha1.HarnessRunConditionCompleted,
			Status:             metav1.ConditionTrue,
			Reason:             "Cancelled",
			Message:            "HarnessRun was deleted",
			LastTransitionTime: now,
		},
	}

	if _, err := r.commitStatus(context.Background(), run, orig); err != nil {
		t.Fatalf("commitStatus: %v", err)
	}

	got := rec.all
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (run-completed only); events=%v", len(got), kindList(got))
	}
	if got[0].Spec.Kind != paddockv1alpha1.AuditKindRunCompleted {
		t.Errorf("kind = %q, want run-completed", got[0].Spec.Kind)
	}
	if got[0].Spec.Decision != paddockv1alpha1.AuditDecisionWarned {
		t.Errorf("decision = %q, want warned", got[0].Spec.Decision)
	}
	// Ensure no run-failed was emitted for a Cancelled run.
	for _, e := range got {
		if e.Spec.Kind == paddockv1alpha1.AuditKindRunFailed {
			t.Errorf("unexpected run-failed event for Cancelled run")
		}
	}
}

// kindList is a helper for readable error messages.
func kindList(evts []*paddockv1alpha1.AuditEvent) []paddockv1alpha1.AuditKind {
	out := make([]paddockv1alpha1.AuditKind, len(evts))
	for i, e := range evts {
		out[i] = e.Spec.Kind
	}
	return out
}
