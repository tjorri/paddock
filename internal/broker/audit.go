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

func (w *AuditWriter) sink() auditing.Sink {
	if w.Sink != nil {
		return w.Sink
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
