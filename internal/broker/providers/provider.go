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
// per IssueRequest by intersecting the template's requires purpose
// with the matched BrokerPolicy's provider.kind. See ADR-0015.
package providers

import (
	"context"
	"errors"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Provider is the extensibility seam. All credentials — including
// Static Secret reads — flow through a Provider; see ADR-0015 for the
// no-bypass invariant.
type Provider interface {
	// Name is the stable identifier matched against
	// BrokerPolicy.spec.grants.credentials[*].provider.kind.
	Name() string

	// Purposes lists the credential purposes this provider can back.
	// The admission algorithm (ADR-0014) rejects a BrokerPolicy that
	// grants a credential whose purpose is not in this list.
	Purposes() []paddockv1alpha1.CredentialPurpose

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

	// Purpose is the template's declared purpose for this credential.
	Purpose paddockv1alpha1.CredentialPurpose

	// Grant is the matched policy grant, including the provider config.
	Grant paddockv1alpha1.CredentialGrant
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
// given purpose. Broker callers should interpret this as a policy
// mismatch, not a server-side failure.
var ErrNotImplemented = errors.New("provider cannot back this credential purpose")
