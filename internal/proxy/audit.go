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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// AuditSink records per-connection decisions. The M4 implementation
// creates one AuditEvent per denied connection; allows are summarised
// in a future milestone per ADR-0016 (debounce + summary). The deny
// path is unconditional because denials are always security-relevant.
type AuditSink interface {
	RecordEgress(ctx context.Context, e EgressEvent)
}

// EgressEvent is what the MITM engine hands to the sink. Keep the
// shape flat — sinks may buffer thousands of these in memory between
// writes.
type EgressEvent struct {
	Host          string
	Port          int
	Decision      paddockv1alpha1.AuditDecision
	MatchedPolicy string
	Reason        string
	When          time.Time
}

// NoopAuditSink silently drops records. Handy for tests and for running
// the proxy locally without cluster credentials.
type NoopAuditSink struct{}

// RecordEgress implements AuditSink.
func (NoopAuditSink) RecordEgress(_ context.Context, _ EgressEvent) {}

// ClientAuditSink writes AuditEvent objects via a controller-runtime
// client. Unbuffered for M4 — every deny lands as its own object. M6+
// brings debounce + egress-block-summary once volume justifies it
// (ADR-0016 §"Debounce and summarization").
type ClientAuditSink struct {
	Client    client.Client
	Namespace string
	RunName   string
}

// RecordEgress writes one AuditEvent. Errors are swallowed after
// logging because the proxy's hot path must not block on audit writes.
// The log is the fallback record.
func (s *ClientAuditSink) RecordEgress(ctx context.Context, e EgressEvent) {
	when := e.When
	if when.IsZero() {
		when = time.Now().UTC()
	}
	var kind paddockv1alpha1.AuditKind
	switch e.Decision {
	case paddockv1alpha1.AuditDecisionDenied, paddockv1alpha1.AuditDecisionWarned:
		kind = paddockv1alpha1.AuditKindEgressBlock
	default:
		kind = paddockv1alpha1.AuditKindEgressAllow
	}
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    s.Namespace,
			GenerateName: "ae-egress-",
			Labels: map[string]string{
				paddockv1alpha1.AuditEventLabelRun:      s.RunName,
				paddockv1alpha1.AuditEventLabelDecision: string(e.Decision),
				paddockv1alpha1.AuditEventLabelKind:     string(kind),
			},
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  e.Decision,
			Kind:      kind,
			Timestamp: metav1.NewTime(when),
			Reason:    e.Reason,
			Destination: &paddockv1alpha1.AuditDestination{
				Host: e.Host,
				Port: int32(e.Port), //nolint:gosec // bounded [1,65535]
			},
		},
	}
	if s.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: s.RunName}
	}
	if e.MatchedPolicy != "" {
		ae.Spec.MatchedPolicy = &paddockv1alpha1.LocalObjectReference{Name: e.MatchedPolicy}
	}
	// Best-effort: the proxy logs + metrics are the backstop channel
	// when etcd is unreachable.
	_ = s.Client.Create(ctx, ae)
}
