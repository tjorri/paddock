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

package proxy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// recordingAuditSink is a auditing.Sink fake that captures every Write
// for assertion. Safe for concurrent use.
type recordingAuditSink struct {
	mu     sync.Mutex
	writes []*paddockv1alpha1.AuditEvent
	err    error
}

func (r *recordingAuditSink) Write(_ context.Context, ae *paddockv1alpha1.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, ae)
	return r.err
}

func (r *recordingAuditSink) snapshot() []*paddockv1alpha1.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*paddockv1alpha1.AuditEvent, len(r.writes))
	copy(out, r.writes)
	return out
}

func TestClientAuditSink_Denied_WritesEgressBlock(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{Sink: rec, Namespace: "test-ns", RunName: "test-run"}
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host:     "denied.example.com",
		Port:     443,
		Decision: paddockv1alpha1.AuditDecisionDenied,
		Reason:   "deny by test",
		When:     time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	if got := w[0].Spec.Kind; got != paddockv1alpha1.AuditKindEgressBlock {
		t.Errorf("Kind = %q, want %q (block)", got, paddockv1alpha1.AuditKindEgressBlock)
	}
	if got := w[0].Spec.Decision; got != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("Decision = %q, want denied", got)
	}
	if got := w[0].Spec.Destination.Host; got != "denied.example.com" {
		t.Errorf("Host = %q, want denied.example.com", got)
	}
	if got := w[0].Spec.Destination.Port; got != 443 {
		t.Errorf("Port = %d, want 443", got)
	}
	if got := w[0].Spec.Reason; got != "deny by test" {
		t.Errorf("Reason = %q", got)
	}
	if got := w[0].Namespace; got != "test-ns" {
		t.Errorf("Namespace = %q, want test-ns", got)
	}
	if w[0].Spec.RunRef == nil || w[0].Spec.RunRef.Name != "test-run" {
		t.Errorf("RunRef = %v, want {Name: test-run}", w[0].Spec.RunRef)
	}
}

func TestClientAuditSink_Granted_WritesEgressAllow(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{Sink: rec, Namespace: "test-ns", RunName: "test-run"}
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host:          "ok.example.com",
		Port:          443,
		Decision:      paddockv1alpha1.AuditDecisionGranted,
		MatchedPolicy: "test-policy",
		Reason:        "matched",
		Kind:          paddockv1alpha1.AuditKindEgressAllow,
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	if got := w[0].Spec.Kind; got != paddockv1alpha1.AuditKindEgressAllow {
		t.Errorf("Kind = %q, want %q (allow)", got, paddockv1alpha1.AuditKindEgressAllow)
	}
	if got := w[0].Spec.Decision; got != paddockv1alpha1.AuditDecisionGranted {
		t.Errorf("Decision = %q, want granted", got)
	}
	if w[0].Spec.MatchedPolicy == nil || w[0].Spec.MatchedPolicy.Name != "test-policy" {
		t.Errorf("MatchedPolicy = %v, want {Name: test-policy}", w[0].Spec.MatchedPolicy)
	}
}

func TestClientAuditSink_DiscoveryAllowKindPassesThrough(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{Sink: rec, Namespace: "test-ns", RunName: "test-run"}
	// The proxy sets Kind = egress-discovery-allow when decision.DiscoveryAllow
	// fires; ClientAuditSink must propagate that into the auditing.EgressInput
	// so the resulting AuditEvent's Spec.Kind reflects it.
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host:     "discover.example.com",
		Port:     443,
		Decision: paddockv1alpha1.AuditDecisionGranted,
		Kind:     paddockv1alpha1.AuditKindEgressDiscoveryAllow,
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	if got := w[0].Spec.Kind; got != paddockv1alpha1.AuditKindEgressDiscoveryAllow {
		t.Errorf("Kind = %q, want %q (discovery-allow)", got, paddockv1alpha1.AuditKindEgressDiscoveryAllow)
	}
}

func TestClientAuditSink_Warned_WritesEgressBlock(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{Sink: rec}
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host:     "warn.example.com",
		Port:     443,
		Decision: paddockv1alpha1.AuditDecisionWarned,
		Reason:   "warn-mode",
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	if got := w[0].Spec.Kind; got != paddockv1alpha1.AuditKindEgressBlock {
		t.Errorf("Kind = %q, want %q (warned dispatches into block path)", got, paddockv1alpha1.AuditKindEgressBlock)
	}
	if got := w[0].Spec.Decision; got != paddockv1alpha1.AuditDecisionWarned {
		t.Errorf("Decision = %q, want warned", got)
	}
}

func TestClientAuditSink_NilSinkFallback_NoError(t *testing.T) {
	cas := &ClientAuditSink{} // Sink left nil — writeSink() returns auditing.NoopSink{}.
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host: "x.example.com", Port: 443,
		Decision: paddockv1alpha1.AuditDecisionGranted,
	})
	if err != nil {
		t.Errorf("RecordEgress with nil Sink returned %v, want nil (NoopSink fallback)", err)
	}
}

func TestClientAuditSink_ZeroWhen_DefaultsToNow(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{Sink: rec}
	before := time.Now().UTC()
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host: "t.example.com", Port: 443,
		Decision: paddockv1alpha1.AuditDecisionGranted,
		// When deliberately zero.
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	after := time.Now().UTC()
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	got := w[0].Spec.Timestamp.UTC()
	// Allow a small slop on each side — metav1.Time truncates to seconds in
	// some conversions, so before/after windows can be split by sub-second
	// rounding. ±1s is generous and stable.
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("Timestamp = %v, want in [%v, %v]", got, before, after)
	}
}

func TestClientAuditSink_SinkError_Propagates(t *testing.T) {
	rec := &recordingAuditSink{err: errors.New("etcd partition")}
	cas := &ClientAuditSink{Sink: rec}
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host: "z.example.com", Port: 443,
		Decision: paddockv1alpha1.AuditDecisionDenied,
	})
	if err == nil {
		t.Fatalf("RecordEgress returned nil; want sink error to propagate")
	}
	if !errors.Is(err, rec.err) && !strings.Contains(err.Error(), "etcd partition") {
		t.Errorf("error = %v; want it to surface the sink error 'etcd partition'", err)
	}
}
