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

package providers

import (
	"context"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// RenewableProvider is an optional companion interface to Provider for
// providers whose issued credentials have a finite lifetime. Broker
// calls Renew lazily on each prompt submission for any lease whose
// ExpiresAt is within a configurable window (default 5 min).
//
// Implementing Renew is opt-in. Providers without expiry leave Renew
// unimplemented; the broker uses RenewableProviderOf below to detect
// support.
type RenewableProvider interface {
	Provider

	// Renew re-issues a lease without revoking the prior one's identity.
	// The returned IssueResult carries a fresh Value and (typically) a
	// later ExpiresAt; the broker atomically swaps it into the in-memory
	// lease registry and patches HarnessRun.status.issuedLeases.
	//
	// Failure is non-fatal: the broker emits an audit event of kind
	// credential-renewal-failed (see Task 7), leaves the existing lease,
	// and continues.
	Renew(ctx context.Context, lease paddockv1alpha1.IssuedLease) (*IssueResult, error)
}

// RenewableProviderOf returns the provider as a RenewableProvider if it
// implements the interface, else nil. Use type assertion at call sites
// instead of a registry to keep providers self-contained.
func RenewableProviderOf(p Provider) RenewableProvider {
	rp, _ := p.(RenewableProvider)
	return rp
}
