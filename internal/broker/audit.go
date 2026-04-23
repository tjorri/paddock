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

package broker

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// AuditWriter creates AuditEvent objects in the run's namespace. This
// is the canonical security trail — every credential issuance (including
// Static) and every denial lands as one record. See ADR-0016.
//
// v0.3 ships the simplest possible writer: one etcd write per event.
// The debounce + summary story from ADR-0016 is wired at the emitter
// (per-run counter here + a periodic flush) in later milestones when
// proxy-side egress events start generating enough volume to matter.
type AuditWriter struct {
	Client client.Client
}

// AuditCredentialIssued records a successful Issue.
func (w *AuditWriter) CredentialIssued(ctx context.Context, e CredentialAudit) error {
	return w.write(ctx, e.buildEvent(paddockv1alpha1.AuditDecisionGranted, paddockv1alpha1.AuditKindCredentialIssued))
}

// AuditCredentialDenied records a failed Issue.
func (w *AuditWriter) CredentialDenied(ctx context.Context, e CredentialAudit) error {
	return w.write(ctx, e.buildEvent(paddockv1alpha1.AuditDecisionDenied, paddockv1alpha1.AuditKindCredentialDenied))
}

// CredentialAudit is the emitter-side shape for a credential decision.
type CredentialAudit struct {
	RunName        string
	Namespace      string
	CredentialName string
	Purpose        paddockv1alpha1.CredentialPurpose
	Provider       string
	MatchedPolicy  string
	Reason         string
	When           time.Time
}

func (e CredentialAudit) buildEvent(decision paddockv1alpha1.AuditDecision, kind paddockv1alpha1.AuditKind) *paddockv1alpha1.AuditEvent {
	when := e.When
	if when.IsZero() {
		when = time.Now().UTC()
	}
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    e.Namespace,
			GenerateName: "ae-cred-",
			Labels: map[string]string{
				paddockv1alpha1.AuditEventLabelRun:      e.RunName,
				paddockv1alpha1.AuditEventLabelDecision: string(decision),
				paddockv1alpha1.AuditEventLabelKind:     string(kind),
			},
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  decision,
			Kind:      kind,
			Timestamp: metav1.NewTime(when),
			Reason:    e.Reason,
			Credential: &paddockv1alpha1.AuditCredentialRef{
				Name:     e.CredentialName,
				Provider: e.Provider,
				Purpose:  string(e.Purpose),
			},
		},
	}
	if e.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: e.RunName}
	}
	if e.MatchedPolicy != "" {
		ae.Spec.MatchedPolicy = &paddockv1alpha1.LocalObjectReference{Name: e.MatchedPolicy}
	}
	return ae
}

func (w *AuditWriter) write(ctx context.Context, ae *paddockv1alpha1.AuditEvent) error {
	if err := w.Client.Create(ctx, ae); err != nil {
		return fmt.Errorf("creating AuditEvent: %w", err)
	}
	return nil
}
