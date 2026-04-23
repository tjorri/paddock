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

// Package api defines the wire shape of the broker's HTTP/JSON API.
// The broker authenticates every caller via a Bearer ProjectedSA token
// with audience=paddock-broker and scopes the response to the caller's
// namespace + named run. See ADR-0012 (architecture) and ADR-0015
// (providers).
package api

import (
	"time"
)

// Header names used by the broker protocol.
const (
	// HeaderRun identifies the HarnessRun the request pertains to. The
	// broker validates the caller's SA against this run's namespace.
	HeaderRun = "X-Paddock-Run"

	// HeaderNamespace optionally narrows the run lookup. When unset the
	// broker infers the namespace from the caller's SA (run Pods always
	// live alongside the run; the controller may call from paddock-system
	// and must then set this header explicitly).
	HeaderNamespace = "X-Paddock-Run-Namespace"
)

// Path constants for the broker's HTTP routes.
const (
	PathIssue   = "/v1/issue"
	PathHealthz = "/healthz"
	PathReadyz  = "/readyz"
)

// IssueRequest asks the broker to issue a value for one of the named
// credentials declared on the run's template.
type IssueRequest struct {
	// Name matches a template's requires.credentials[*].name.
	Name string `json:"name"`
}

// IssueResponse carries the issued value, the lease that tracks it,
// and the absolute expiry.
type IssueResponse struct {
	// Value is the credential material. The caller is responsible for
	// handling it as secret data (do not log it, do not write it into
	// a ConfigMap, etc.).
	Value string `json:"value"`

	// LeaseID identifies this issuance. Required for a later renewal
	// or revocation. Opaque to the caller.
	LeaseID string `json:"leaseId"`

	// ExpiresAt is the absolute instant at which the lease expires.
	// Callers MUST renew before this time or treat the value as stale.
	// A zero value signals "no expiry" (Static provider's default).
	ExpiresAt time.Time `json:"expiresAt"`

	// Provider names the backing provider kind that minted the value.
	// Purely informational; the caller has no business deciding based
	// on it.
	Provider string `json:"provider"`
}

// ErrorResponse is the broker's error envelope. HTTP status code
// encodes the category (400/401/403/404/500); Code/Message are the
// machine- and human-readable specifics.
type ErrorResponse struct {
	// Code is a short symbolic code. Known values:
	//   - "BadRequest"           400
	//   - "Unauthorized"         401
	//   - "Forbidden"            403
	//   - "RunNotFound"          404
	//   - "CredentialNotFound"   404 (template does not declare it)
	//   - "PolicyMissing"        403 (no BrokerPolicy grants the cred)
	//   - "ProviderFailure"      500
	Code string `json:"code"`

	// Message is a human-readable explanation.
	Message string `json:"message"`
}
