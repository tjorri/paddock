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
	PathIssue          = "/v1/issue"
	PathValidateEgress = "/v1/validate-egress"
	PathSubstituteAuth = "/v1/substitute-auth"
	PathHealthz        = "/healthz"
	PathReadyz         = "/readyz"
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

	// DeliveryMode names how the value reaches the agent. One of
	// "ProxyInjected" or "InContainer". Built-in providers
	// (AnthropicAPI, GitHubApp, PATPool) are always ProxyInjected.
	// UserSuppliedSecret takes this from the matched grant's
	// deliveryMode.
	DeliveryMode string `json:"deliveryMode"`

	// Hosts is the destination host list associated with this
	// credential's delivery. For ProxyInjected delivery it names the
	// hostnames the proxy will substitute on; for InContainer delivery
	// it is empty. PATPool, which is host-agnostic, may omit this.
	Hosts []string `json:"hosts,omitempty"`

	// InContainerReason is the operator-supplied justification for
	// opting a UserSuppliedSecret into InContainer delivery. Empty for
	// any other delivery mode.
	InContainerReason string `json:"inContainerReason,omitempty"`
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
	//   - "BearerUnknown"        404 (SubstituteAuth could not match bearer)
	//   - "ProviderFailure"      500
	Code string `json:"code"`

	// Message is a human-readable explanation.
	Message string `json:"message"`
}

// ValidateEgressRequest is the proxy's per-connection policy question.
// The broker intersects the run's matching BrokerPolicies' egress grants
// against (host, port) and returns allow/deny. Used by the proxy on
// every new CONNECT / transparent-mode TCP arrival.
type ValidateEgressRequest struct {
	// Host is the destination hostname (CONNECT target host or SNI).
	Host string `json:"host"`
	// Port is the destination TCP port.
	Port int `json:"port"`
}

// ValidateEgressResponse carries the egress verdict. A non-nil
// SubstituteAuth=true signals that the proxy must MITM and call
// PathSubstituteAuth before forwarding — required for the AnthropicAPI
// x-api-key swap.
type ValidateEgressResponse struct {
	Allowed        bool   `json:"allowed"`
	MatchedPolicy  string `json:"matchedPolicy,omitempty"`
	Reason         string `json:"reason,omitempty"`
	SubstituteAuth bool   `json:"substituteAuth,omitempty"`
}

// SubstituteAuthRequest is the proxy's per-request credential swap call,
// made once per MITM'd request whose matched egress grant declared
// SubstituteAuth:true. The proxy sends the host + the incoming
// Authorization / x-api-key headers; the broker returns the headers it
// wants set/removed before the request is forwarded upstream.
type SubstituteAuthRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`

	// IncomingAuthorization is the agent-sent Authorization header
	// value, if any. The broker looks it up by bearer to find the
	// matching provider lease.
	IncomingAuthorization string `json:"incomingAuthorization,omitempty"`

	// IncomingXAPIKey is the agent-sent x-api-key header value, if any.
	// Providers that issue opaque bearers via x-api-key (Anthropic-
	// style) read this as an alternative to Authorization.
	IncomingXAPIKey string `json:"incomingXApiKey,omitempty"`
}

// SubstituteAuthResponse tells the proxy what to do with the request
// headers before forwarding upstream. SetHeaders overrides or adds
// headers (case-insensitive per RFC 9110); RemoveHeaders drops headers
// entirely (commonly used to strip the Paddock-issued bearer before
// upstream ever sees it).
type SubstituteAuthResponse struct {
	SetHeaders    map[string]string `json:"setHeaders,omitempty"`
	RemoveHeaders []string          `json:"removeHeaders,omitempty"`
}
