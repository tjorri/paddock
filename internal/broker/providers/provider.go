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

// Package providers implements the broker's pluggable credential
// backends. Each provider implements Provider; the broker picks one
// per IssueRequest by matching the credential name declared on the
// HarnessTemplate to a BrokerPolicy grant and dispatching to the
// provider named by the grant's provider.kind. See ADR-0015.
package providers

import (
	"context"
	"errors"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// Provider is the extensibility seam. All credentials — including
// UserSuppliedSecret Secret reads — flow through a Provider; see
// ADR-0015 for the no-bypass invariant.
type Provider interface {
	// Name is the stable identifier matched against
	// BrokerPolicy.spec.grants.credentials[*].provider.kind.
	Name() string

	// Issue returns a value for the request. The broker passes in the
	// (already-validated) BrokerPolicy grant that picked this provider;
	// providers read their config from grant.Provider.
	Issue(ctx context.Context, req IssueRequest) (IssueResult, error)
}

// IssueRequest carries the broker-level inputs a provider needs.
type IssueRequest struct {
	// RunName is the HarnessRun's name (informational; for audit).
	RunName string

	// Namespace is the run's namespace — providers that need to read
	// Secrets use this to scope Get calls.
	Namespace string

	// CredentialName is the logical name (matches requires.name /
	// grant.name). Providers return the value for this specific
	// credential.
	CredentialName string

	// Grant is the matched policy grant, including the provider config.
	Grant paddockv1alpha1.CredentialGrant

	// GitRepos is the full list of gitRepos on the matched
	// BrokerPolicy.spec.grants. Populated so gitforge providers
	// (GitHubApp, PATPool) can scope their issued tokens to the
	// operator-declared repo set. Empty for non-gitforge grants.
	GitRepos []paddockv1alpha1.GitRepoGrant
}

// IssueResult is what a provider returns on a successful Issue.
type IssueResult struct {
	// Value is the credential material. Callers are responsible for
	// handling it as secret data.
	Value string

	// LeaseID identifies this issuance. For Static this is typically
	// a deterministic hash; for rotating providers it's a random opaque
	// token the provider can later renew or revoke.
	LeaseID string

	// ExpiresAt is the absolute instant the value becomes stale. Zero
	// signals "no expiry" (Static default).
	ExpiresAt time.Time
}

// ErrNotImplemented is returned by a Provider when it cannot back the
// given credential. Broker callers should interpret this as a policy
// mismatch, not a server-side failure.
var ErrNotImplemented = errors.New("provider cannot back this credential")

// Substituter is an optional capability: providers that back
// auth-substituting egress destinations (AnthropicAPI's x-api-key swap
// in v0.3, OpenAI Authorization: Bearer swap planned for v0.4) implement
// this interface.
//
// The broker's /v1/substitute-auth handler walks every provider that
// implements Substituter and asks each one whether it owns the
// incoming bearer; the first provider returning Matched=true answers
// definitively (possibly with an error). See spec 0002 §6.2, §7.1.
type Substituter interface {
	// SubstituteAuth looks up the incoming bearer/api-key in this
	// provider's lease store and, if owned, returns the headers the
	// proxy should set + remove before forwarding upstream. Returns
	// Matched=false when the bearer is not owned by this provider —
	// callers then try the next Substituter.
	SubstituteAuth(ctx context.Context, req SubstituteRequest) (brokerapi.SubstituteResult, error)
}

// SubstituteRequest is the per-request substitution input the broker
// passes down from its /v1/substitute-auth handler.
type SubstituteRequest struct {
	// RunName and Namespace identify the calling run — enforced already
	// by the broker handler via TokenReview; the provider uses them for
	// audit + to scope any Secret reads.
	RunName   string
	Namespace string

	// Host and Port are the upstream destination the agent was heading
	// to. Providers can reject a mismatched host (e.g. anthropic provider
	// returning Matched=false for non-api.anthropic.com hosts even when
	// the bearer matches).
	Host string
	Port int

	// IncomingBearer is the agent-sent credential the proxy pulled off
	// the request. For Anthropic, agents present it as either the value
	// of Authorization ("Bearer pdk-anthropic-…") or x-api-key
	// ("pdk-anthropic-…"); the proxy strips the "Bearer " prefix before
	// calling so the provider sees the raw bearer.
	IncomingBearer string
}
