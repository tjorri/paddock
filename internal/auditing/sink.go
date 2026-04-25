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
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Sink writes a single AuditEvent. Implementations decide where the
// event lands (apiserver via KubeSink, /dev/null via NoopSink). On
// failure the Sink returns an error wrapping ErrAuditWrite; the caller
// decides whether to surface the failure or log+counter and continue.
type Sink interface {
	Write(ctx context.Context, ae *paddockv1alpha1.AuditEvent) error
}

// KubeSink is the production implementation. Component is one of
// "broker" | "proxy" | "webhook" | "controller" and is stamped on
// every emitted AuditEvent's paddock.dev/component label so consumers
// can disambiguate identical kinds emitted from different components
// (e.g., the controller's credential-issued summary vs. the broker's
// per-credential events).
type KubeSink struct {
	Client    client.Client
	Component string
}

// Write stamps the component label and calls client.Create. On error
// it increments the paddock_audit_write_failures_total counter and
// returns the error wrapped in ErrAuditWrite.
func (s *KubeSink) Write(ctx context.Context, ae *paddockv1alpha1.AuditEvent) error {
	if ae.Labels == nil {
		ae.Labels = map[string]string{}
	}
	ae.Labels[paddockv1alpha1.AuditEventLabelComponent] = s.Component
	if err := s.Client.Create(ctx, ae); err != nil {
		auditWriteFailures.WithLabelValues(s.Component, string(ae.Spec.Decision), string(ae.Spec.Kind)).Inc()
		return fmt.Errorf("%w: %v", ErrAuditWrite, err)
	}
	return nil
}

// NoopSink drops every event silently. Used in tests that don't care
// about audit emission and in local-dev binaries that have no cluster
// client.
type NoopSink struct{}

// Write implements Sink. Always returns nil and leaves the AuditEvent
// untouched.
func (NoopSink) Write(_ context.Context, _ *paddockv1alpha1.AuditEvent) error { return nil }
