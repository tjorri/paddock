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

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

type capturedSink struct {
	all []*paddockv1alpha1.AuditEvent
	err error
}

func (c *capturedSink) Write(_ context.Context, ae *paddockv1alpha1.AuditEvent) error {
	c.all = append(c.all, ae.DeepCopy())
	return c.err
}

// events returns a snapshot of captured AuditEvents.
func (c *capturedSink) events() []*paddockv1alpha1.AuditEvent {
	return c.all
}

func TestControllerAudit_EmitCredentialIssuedSummary(t *testing.T) {
	rec := &capturedSink{}
	a := &ControllerAudit{Sink: rec}
	a.EmitCredentialIssuedSummary(context.Background(), "hr-1", "team-a", 3)
	if len(rec.all) != 1 {
		t.Fatalf("got %d events, want 1", len(rec.all))
	}
	ae := rec.all[0]
	if ae.Spec.Kind != paddockv1alpha1.AuditKindCredentialIssued {
		t.Errorf("kind = %q, want credential-issued", ae.Spec.Kind)
	}
	if ae.Spec.Count != 3 {
		t.Errorf("count = %d, want 3", ae.Spec.Count)
	}
}

func TestControllerAudit_FailOpen_OnSinkError(t *testing.T) {
	rec := &capturedSink{err: errors.New("etcd partition")}
	a := &ControllerAudit{Sink: rec}
	// Must not panic; method returns nothing.
	a.EmitRunCompleted(context.Background(), "hr-1", "team-a", paddockv1alpha1.AuditDecisionGranted, "")
}

func TestControllerAudit_NilSink_NoOp(t *testing.T) {
	var a *ControllerAudit // nil receiver — must not panic
	a.EmitCAProjected(context.Background(), "hr-1", "team-a", "hr-1-broker-ca")
	a2 := &ControllerAudit{Sink: nil}
	a2.EmitCAProjected(context.Background(), "hr-1", "team-a", "hr-1-broker-ca")
	// pass = no panic
}

func TestControllerAudit_RunFailedAndCompleted(t *testing.T) {
	rec := &capturedSink{}
	a := &ControllerAudit{Sink: rec}
	a.EmitRunFailed(context.Background(), "hr-1", "team-a", "BrokerDenied", "policy missing")
	a.EmitRunCompleted(context.Background(), "hr-1", "team-a", paddockv1alpha1.AuditDecisionDenied, "BrokerDenied")
	if len(rec.all) != 2 {
		t.Fatalf("got %d, want 2", len(rec.all))
	}
	if rec.all[0].Spec.Kind != paddockv1alpha1.AuditKindRunFailed ||
		rec.all[1].Spec.Kind != paddockv1alpha1.AuditKindRunCompleted {
		t.Errorf("kinds = %q,%q", rec.all[0].Spec.Kind, rec.all[1].Spec.Kind)
	}
}

func TestControllerAudit_EmitNetworkPolicyEnforcementWithdrawn(t *testing.T) {
	rec := &capturedSink{}
	a := &ControllerAudit{Sink: rec}
	a.EmitNetworkPolicyEnforcementWithdrawn(context.Background(), "hr-1", "team-a",
		"per-run NetworkPolicy hr-1-egress was missing on reconcile; re-created")
	if len(rec.all) != 1 {
		t.Fatalf("got %d events, want 1", len(rec.all))
	}
	ae := rec.all[0]
	if ae.Spec.Kind != paddockv1alpha1.AuditKindNetworkPolicyEnforcementWithdrawn {
		t.Errorf("kind = %q", ae.Spec.Kind)
	}
	if ae.Spec.Decision != paddockv1alpha1.AuditDecisionWarned {
		t.Errorf("decision = %q, want warned", ae.Spec.Decision)
	}
}
