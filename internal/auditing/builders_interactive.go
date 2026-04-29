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

package auditing

import (
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// PromptAuditInput is the input shape for NewPromptSubmitted.
type PromptAuditInput struct {
	RunName      string
	Namespace    string
	SubmitterSA  string
	PromptHash   string
	PromptLength int
	TurnSeq      int32
	When         time.Time
}

// NewPromptSubmitted builds a prompt-submitted AuditEvent.
func NewPromptSubmitted(in PromptAuditInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "prompt-submitted-",
			Namespace:    in.Namespace,
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			Kind:      paddockv1alpha1.AuditKindPromptSubmitted,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Detail: map[string]string{
				"submitterSA":  in.SubmitterSA,
				"promptHash":   in.PromptHash,
				"promptLength": strconv.Itoa(in.PromptLength),
				"turnSeq":      strconv.Itoa(int(in.TurnSeq)),
			},
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// PromptCompletedInput is the input shape for NewPromptCompleted.
type PromptCompletedInput struct {
	RunName    string
	Namespace  string
	TurnSeq    int32
	DurationMs int64
	EventCount int32
	Outcome    string // "ok" | "error" | "interrupted"
	When       time.Time
}

// NewPromptCompleted builds a prompt-completed AuditEvent.
func NewPromptCompleted(in PromptCompletedInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "prompt-completed-",
			Namespace:    in.Namespace,
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			Kind:      paddockv1alpha1.AuditKindPromptCompleted,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Detail: map[string]string{
				"turnSeq":    strconv.Itoa(int(in.TurnSeq)),
				"durationMs": strconv.FormatInt(in.DurationMs, 10),
				"eventCount": strconv.Itoa(int(in.EventCount)),
				"outcome":    in.Outcome,
			},
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// ShellOpenedInput is the input shape for NewShellSessionOpened.
type ShellOpenedInput struct {
	RunName     string
	Namespace   string
	SessionID   string
	SubmitterSA string
	Target      string
	Command     []string
	When        time.Time
}

// NewShellSessionOpened builds a shell-session-opened AuditEvent.
func NewShellSessionOpened(in ShellOpenedInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "shell-opened-",
			Namespace:    in.Namespace,
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			Kind:      paddockv1alpha1.AuditKindShellSessionOpened,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Detail: map[string]string{
				"sessionID":   in.SessionID,
				"submitterSA": in.SubmitterSA,
				"target":      in.Target,
				"command":     strings.Join(in.Command, " "),
			},
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// ShellClosedInput is the input shape for NewShellSessionClosed.
type ShellClosedInput struct {
	RunName    string
	Namespace  string
	SessionID  string
	DurationMs int64
	ByteCount  int64
	When       time.Time
}

// NewShellSessionClosed builds a shell-session-closed AuditEvent.
func NewShellSessionClosed(in ShellClosedInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "shell-closed-",
			Namespace:    in.Namespace,
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			Kind:      paddockv1alpha1.AuditKindShellSessionClosed,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Detail: map[string]string{
				"sessionID":  in.SessionID,
				"durationMs": strconv.FormatInt(in.DurationMs, 10),
				"byteCount":  strconv.FormatInt(in.ByteCount, 10),
			},
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// CredentialRenewalFailedInput is the input shape for NewCredentialRenewalFailed.
type CredentialRenewalFailedInput struct {
	RunName   string
	Namespace string
	Provider  string
	LeaseID   string
	Error     string
	When      time.Time
}

// NewCredentialRenewalFailed builds a credential-renewal-failed AuditEvent.
func NewCredentialRenewalFailed(in CredentialRenewalFailedInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "renewal-failed-",
			Namespace:    in.Namespace,
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionDenied,
			Kind:      paddockv1alpha1.AuditKindCredentialRenewalFailed,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Detail: map[string]string{
				"provider": in.Provider,
				"leaseID":  in.LeaseID,
				"error":    in.Error,
			},
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// InteractiveRunTerminatedInput is the input for NewInteractiveRunTerminated.
// Reason is one of: idle, detach, max-lifetime, explicit, error.
// Decision must be set by the caller: AuditDecisionGranted for planned
// terminations (idle/detach/max-lifetime/explicit) and AuditDecisionWarned
// for error-triggered terminations.
type InteractiveRunTerminatedInput struct {
	RunName   string
	Namespace string
	Reason    string
	Decision  paddockv1alpha1.AuditDecision
	When      time.Time
}

// NewInteractiveRunTerminated builds an interactive-run-terminated AuditEvent.
func NewInteractiveRunTerminated(in InteractiveRunTerminatedInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "interactive-terminated-",
			Namespace:    in.Namespace,
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  in.Decision,
			Kind:      paddockv1alpha1.AuditKindInteractiveRunTerminated,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Detail: map[string]string{
				"reason": in.Reason,
			},
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	stampLabels(ae, in.RunName)
	return ae
}
