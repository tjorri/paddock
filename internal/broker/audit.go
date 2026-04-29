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
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"paddock.dev/paddock/internal/auditing"
)

// AuditWriter retains the v0.3 broker-local API but delegates to the
// shared auditing.Sink. New broker code should consume an auditing.Sink
// directly via Server.Sink; AuditWriter is kept so that bootstrap code
// outside cmd/broker that constructs Server with a Client (e.g. tests)
// can keep working until callers migrate.
type AuditWriter struct {
	// Client is retained for backwards-compat: code that builds an
	// AuditWriter without a Sink defaults to a KubeSink wrapping it.
	Client client.Client
	// Sink, when non-nil, is the actual write target. Construct with
	// auditing.KubeSink{Client: c, Component: "broker"} or NoopSink.
	Sink auditing.Sink
}

// CredentialAudit is the emitter-side shape for a credential decision.
type CredentialAudit struct {
	RunName        string
	Namespace      string
	CredentialName string
	Provider       string
	MatchedPolicy  string
	Reason         string
	When           time.Time
}

// NewAuditWriter is the documented constructor. Use it instead of
// AuditWriter{...} literals so misconfiguration (no Sink, no Client)
// surfaces at construction time rather than as a write-time NPE.
//
// The AuditWriter shim is intended for removal once all broker call
// sites consume auditing.Sink directly via Server.Sink. Adding the
// constructor here is the first step in that migration; the actual
// removal is a follow-up tracked in the engineering review's B-11
// mini-card.
func NewAuditWriter(sink auditing.Sink) *AuditWriter {
	if sink == nil {
		panic("broker.NewAuditWriter: sink must not be nil; use auditing.NoopSink{} for tests that don't care about audit emission")
	}
	return &AuditWriter{Sink: sink}
}

func (w *AuditWriter) sink() auditing.Sink {
	if w.Sink != nil {
		return w.Sink
	}
	if w.Client == nil {
		// Zero-value AuditWriter{} would otherwise return a
		// nil-Client KubeSink that panics at the first Write call.
		// Surface the misconfiguration here, where the stack trace
		// points at the actual site.
		panic("broker.AuditWriter: neither Sink nor Client is set; construct via broker.NewAuditWriter(sink)")
	}
	return &auditing.KubeSink{Client: w.Client, Component: "broker"}
}

// CredentialIssued records a successful Issue.
func (w *AuditWriter) CredentialIssued(ctx context.Context, e CredentialAudit) error {
	return w.sink().Write(ctx, auditing.NewCredentialIssued(auditing.CredentialIssuedInput{
		RunName:        e.RunName,
		Namespace:      e.Namespace,
		CredentialName: e.CredentialName,
		Provider:       e.Provider,
		MatchedPolicy:  e.MatchedPolicy,
		Reason:         e.Reason,
		When:           e.When,
	}))
}

// CredentialDenied records a failed Issue.
func (w *AuditWriter) CredentialDenied(ctx context.Context, e CredentialAudit) error {
	return w.sink().Write(ctx, auditing.NewCredentialDenied(auditing.CredentialDeniedInput{
		RunName:        e.RunName,
		Namespace:      e.Namespace,
		CredentialName: e.CredentialName,
		Provider:       e.Provider,
		MatchedPolicy:  e.MatchedPolicy,
		Reason:         e.Reason,
		When:           e.When,
	}))
}

// CredentialRevoked records a successful Revoke.
func (w *AuditWriter) CredentialRevoked(ctx context.Context, e CredentialAudit) error {
	return w.sink().Write(ctx, auditing.NewCredentialRevoked(auditing.CredentialRevokedInput{
		RunName:        e.RunName,
		Namespace:      e.Namespace,
		CredentialName: e.CredentialName,
		Provider:       e.Provider,
		MatchedPolicy:  e.MatchedPolicy,
		Reason:         e.Reason,
		When:           e.When,
	}))
}

// CredentialRenewed emits an audit event for a successful renewal.
func (w *AuditWriter) CredentialRenewed(ctx context.Context, namespace, runName, provider, leaseID string, expiresAt time.Time) {
	_ = w.sink().Write(ctx, auditing.NewCredentialRenewed(auditing.CredentialRenewedInput{
		RunName:   runName,
		Namespace: namespace,
		Provider:  provider,
		LeaseID:   leaseID,
		ExpiresAt: expiresAt,
	}))
}

// CredentialRenewalFailed emits an audit event for a renewal failure.
func (w *AuditWriter) CredentialRenewalFailed(ctx context.Context, namespace, runName, provider, leaseID string, err error) {
	_ = w.sink().Write(ctx, auditing.NewCredentialRenewalFailed(auditing.CredentialRenewalFailedInput{
		RunName:   runName,
		Namespace: namespace,
		Provider:  provider,
		LeaseID:   leaseID,
		Error:     err.Error(),
	}))
}
