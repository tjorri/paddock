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

package auditing_test

import (
	"testing"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
)

func TestNewPromptSubmitted(t *testing.T) {
	t.Parallel()
	ae := auditing.NewPromptSubmitted(auditing.PromptAuditInput{
		RunName:      "r1",
		Namespace:    "ns",
		SubmitterSA:  "system:serviceaccount:ns:user-bot",
		PromptHash:   "sha256:abc",
		PromptLength: 142,
		TurnSeq:      3,
	})
	if ae.Spec.Kind != paddockv1alpha1.AuditKindPromptSubmitted {
		t.Fatalf("kind = %q, want %q", ae.Spec.Kind, paddockv1alpha1.AuditKindPromptSubmitted)
	}
	if ae.Spec.RunRef == nil || ae.Spec.RunRef.Name != "r1" || ae.Namespace != "ns" {
		t.Fatalf("run/ns lost: %+v", ae)
	}
	if ae.Spec.Detail["promptHash"] != "sha256:abc" {
		t.Fatalf("detail.promptHash = %q", ae.Spec.Detail["promptHash"])
	}
	if ae.Spec.Detail["turnSeq"] != "3" {
		t.Fatalf("detail.turnSeq = %q", ae.Spec.Detail["turnSeq"])
	}
}

func TestNewPromptCompleted(t *testing.T) {
	t.Parallel()
	ae := auditing.NewPromptCompleted(auditing.PromptCompletedInput{
		RunName:    "r1",
		Namespace:  "ns",
		TurnSeq:    3,
		DurationMs: 4200,
		EventCount: 27,
		Outcome:    "ok",
	})
	if ae.Spec.Kind != paddockv1alpha1.AuditKindPromptCompleted {
		t.Fatalf("kind: %q", ae.Spec.Kind)
	}
	if ae.Spec.Detail["durationMs"] != "4200" {
		t.Fatalf("durationMs: %q", ae.Spec.Detail["durationMs"])
	}
}

func TestNewShellSessionOpened(t *testing.T) {
	t.Parallel()
	ae := auditing.NewShellSessionOpened(auditing.ShellOpenedInput{
		RunName:     "r1",
		Namespace:   "ns",
		SessionID:   "shellsess-abc",
		SubmitterSA: "system:serviceaccount:ns:debugger",
		Target:      "agent",
		Command:     []string{"/bin/bash"},
	})
	if ae.Spec.Kind != paddockv1alpha1.AuditKindShellSessionOpened {
		t.Fatalf("kind: %q", ae.Spec.Kind)
	}
	if ae.Spec.Detail["target"] != "agent" {
		t.Fatalf("target: %q", ae.Spec.Detail["target"])
	}
}

func TestNewShellSessionClosed(t *testing.T) {
	t.Parallel()
	ae := auditing.NewShellSessionClosed(auditing.ShellClosedInput{
		RunName:    "r1",
		Namespace:  "ns",
		SessionID:  "shellsess-abc",
		DurationMs: 12345,
		ByteCount:  9876,
	})
	if ae.Spec.Kind != paddockv1alpha1.AuditKindShellSessionClosed {
		t.Fatalf("kind: %q", ae.Spec.Kind)
	}
}

func TestNewCredentialRenewalFailed(t *testing.T) {
	t.Parallel()
	ae := auditing.NewCredentialRenewalFailed(auditing.CredentialRenewalFailedInput{
		RunName:   "r1",
		Namespace: "ns",
		Provider:  "GitHubApp",
		LeaseID:   "lease-9",
		Error:     "GitHub returned 503",
	})
	if ae.Spec.Kind != paddockv1alpha1.AuditKindCredentialRenewalFailed {
		t.Fatalf("kind: %q", ae.Spec.Kind)
	}
	if ae.Spec.Detail["error"] != "GitHub returned 503" {
		t.Fatalf("error: %q", ae.Spec.Detail["error"])
	}
}

func TestNewInteractiveRunTerminated(t *testing.T) {
	t.Parallel()

	t.Run("planned termination emits Granted", func(t *testing.T) {
		t.Parallel()
		ae := auditing.NewInteractiveRunTerminated(auditing.InteractiveRunTerminatedInput{
			RunName:   "r1",
			Namespace: "ns",
			Reason:    "idle",
			Decision:  paddockv1alpha1.AuditDecisionGranted,
		})
		if ae.Spec.Kind != paddockv1alpha1.AuditKindInteractiveRunTerminated {
			t.Fatalf("kind: %q", ae.Spec.Kind)
		}
		if ae.Spec.Detail["reason"] != "idle" {
			t.Fatalf("reason: %q", ae.Spec.Detail["reason"])
		}
		if ae.Spec.Decision != paddockv1alpha1.AuditDecisionGranted {
			t.Fatalf("decision = %q, want Granted", ae.Spec.Decision)
		}
	})

	t.Run("error termination emits Warned", func(t *testing.T) {
		t.Parallel()
		ae := auditing.NewInteractiveRunTerminated(auditing.InteractiveRunTerminatedInput{
			RunName:   "r2",
			Namespace: "ns",
			Reason:    "error",
			Decision:  paddockv1alpha1.AuditDecisionWarned,
		})
		if ae.Spec.Kind != paddockv1alpha1.AuditKindInteractiveRunTerminated {
			t.Fatalf("kind: %q", ae.Spec.Kind)
		}
		if ae.Spec.Decision != paddockv1alpha1.AuditDecisionWarned {
			t.Fatalf("decision = %q, want Warned", ae.Spec.Decision)
		}
	})
}

func TestNewInteractiveRunTerminated_WhenTimestamp(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	ae := auditing.NewInteractiveRunTerminated(auditing.InteractiveRunTerminatedInput{
		RunName:   "r1",
		Namespace: "ns",
		Reason:    "explicit",
		Decision:  paddockv1alpha1.AuditDecisionGranted,
		When:      fixed,
	})
	if !ae.Spec.Timestamp.Time.Equal(fixed) {
		t.Fatalf("timestamp = %v, want %v", ae.Spec.Timestamp.Time, fixed)
	}
}

func TestNewCredentialRenewed(t *testing.T) {
	t.Parallel()

	t.Run("fields carried correctly", func(t *testing.T) {
		t.Parallel()
		fixed := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
		exp := time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)
		ae := auditing.NewCredentialRenewed(auditing.CredentialRenewedInput{
			RunName:   "run-1",
			Namespace: "ns",
			Provider:  "GitHubApp",
			LeaseID:   "lease-42",
			ExpiresAt: exp,
			When:      fixed,
		})
		if ae.Spec.Kind != paddockv1alpha1.AuditKindCredentialRenewed {
			t.Fatalf("kind = %q, want %q", ae.Spec.Kind, paddockv1alpha1.AuditKindCredentialRenewed)
		}
		if ae.Spec.Decision != paddockv1alpha1.AuditDecisionGranted {
			t.Fatalf("decision = %q, want Granted", ae.Spec.Decision)
		}
		if ae.Spec.RunRef == nil || ae.Spec.RunRef.Name != "run-1" {
			t.Fatalf("RunRef = %v, want run-1", ae.Spec.RunRef)
		}
		if ae.Namespace != "ns" {
			t.Fatalf("namespace = %q, want ns", ae.Namespace)
		}
		if ae.Spec.Detail["provider"] != "GitHubApp" {
			t.Fatalf("detail.provider = %q", ae.Spec.Detail["provider"])
		}
		if ae.Spec.Detail["leaseID"] != "lease-42" {
			t.Fatalf("detail.leaseID = %q", ae.Spec.Detail["leaseID"])
		}
		if ae.Spec.Detail["expiresAt"] != "2026-03-01T11:00:00Z" {
			t.Fatalf("detail.expiresAt = %q", ae.Spec.Detail["expiresAt"])
		}
		if !ae.Spec.Timestamp.Time.Equal(fixed) {
			t.Fatalf("timestamp = %v, want %v", ae.Spec.Timestamp.Time, fixed)
		}
	})

	t.Run("zero ExpiresAt omits detail key", func(t *testing.T) {
		t.Parallel()
		ae := auditing.NewCredentialRenewed(auditing.CredentialRenewedInput{
			RunName:  "run-2",
			Provider: "GitHubApp",
			LeaseID:  "lease-0",
		})
		if _, ok := ae.Spec.Detail["expiresAt"]; ok {
			t.Fatalf("expected expiresAt absent for zero ExpiresAt, got %q", ae.Spec.Detail["expiresAt"])
		}
	})
}
