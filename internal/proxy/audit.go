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

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
)

// AuditSink records per-connection decisions. Phase 2c migrated this
// from a noop-on-error best-effort interface to one that returns an
// error; callers fail-close on the deny path and log+counter on the
// allow path.
type AuditSink interface {
	RecordEgress(ctx context.Context, e EgressEvent) error
}

// EgressEvent is what the MITM engine hands to the sink.
type EgressEvent struct {
	Host          string
	Port          int
	Decision      paddockv1alpha1.AuditDecision
	MatchedPolicy string
	Reason        string
	When          time.Time
	Kind          paddockv1alpha1.AuditKind
}

// NoopAuditSink silently drops records; never errors.
type NoopAuditSink struct{}

// RecordEgress implements AuditSink.
func (NoopAuditSink) RecordEgress(_ context.Context, _ EgressEvent) error { return nil }

// ClientAuditSink writes via the shared auditing.Sink. Sink is the
// production injection point; for back-compat with old call sites that
// supply only a controller-runtime Client + namespace + run name we
// fall back to wrapping a KubeSink internally.
type ClientAuditSink struct {
	Sink      auditing.Sink
	Namespace string
	RunName   string
}

func (s *ClientAuditSink) writeSink() auditing.Sink {
	if s.Sink != nil {
		return s.Sink
	}
	return auditing.NoopSink{}
}

// RecordEgress writes one AuditEvent via the configured Sink. Returns
// the Sink's error (or nil on success). Callers decide whether to fail
// the connection or log+counter.
func (s *ClientAuditSink) RecordEgress(ctx context.Context, e EgressEvent) error {
	when := e.When
	if when.IsZero() {
		when = time.Now().UTC()
	}
	in := auditing.EgressInput{
		RunName:       s.RunName,
		Namespace:     s.Namespace,
		Host:          e.Host,
		Port:          e.Port,
		Decision:      e.Decision,
		MatchedPolicy: e.MatchedPolicy,
		Reason:        e.Reason,
		When:          when,
		Kind:          e.Kind,
	}
	switch e.Decision {
	case paddockv1alpha1.AuditDecisionDenied, paddockv1alpha1.AuditDecisionWarned:
		return s.writeSink().Write(ctx, auditing.NewEgressBlock(in))
	default:
		// Allow path: respect Kind override (egress-discovery-allow vs egress-allow).
		return s.writeSink().Write(ctx, auditing.NewEgressAllow(in))
	}
}
