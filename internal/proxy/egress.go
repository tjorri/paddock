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
	"fmt"
	"strconv"
	"strings"
)

// Validator decides whether the proxy should allow an outbound TLS
// connection to host:port. Implementations may consult local state, call
// the broker's ValidateEgress endpoint, or both (admission + runtime
// re-check — spec 0002 §8.2).
//
// An implementation that returns allowed=false must provide a Reason
// that is safe to surface to tenants (no upstream policy details, no
// broker internals — just the shape "no BrokerPolicy grants egress to
// evil.com:443"). Matched policies are emitted on allow so the proxy
// can attach them to AuditEvents.
type Validator interface {
	ValidateEgress(ctx context.Context, host string, port int) (Decision, error)
}

// Decision captures a single egress verdict. Mirrors the broker's
// ValidateEgress response shape so BrokerValidator's output goes
// straight through.
type Decision struct {
	Allowed       bool
	MatchedPolicy string
	Reason        string

	// SubstituteAuth declares that the MITM path must call the broker's
	// SubstituteAuth endpoint per request and rewrite headers before
	// forwarding upstream. False means the proxy either relays bytes
	// (cooperative/transparent without substitution) or still MITMs for
	// visibility but doesn't rewrite credentials.
	SubstituteAuth bool

	// DiscoveryAllow mirrors ValidateEgressResponse.DiscoveryAllow.
	// When true, the proxy emits an egress-discovery-allow AuditEvent
	// instead of egress-allow.
	DiscoveryAllow bool
}

// StaticValidator accepts a caller-provided host:port allow-list. This
// is the cooperative-mode M4 path — the broker wiring lands in M7 with
// the AnthropicAPIProvider.
//
// Hostnames support a leading "*." wildcard (matches any one-level
// subdomain). Port 0 in the allow-list matches any port.
type StaticValidator struct {
	Allow []AllowRule
}

// AllowRule is one entry in a StaticValidator. Ports is evaluated as a
// whitelist — an empty slice is equivalent to "any port".
type AllowRule struct {
	Host  string
	Ports []int
}

// NewStaticValidatorFromEnv parses a PADDOCK_PROXY_ALLOW-style value
// into a StaticValidator. Format: comma-separated "host:port" entries.
// Port "*" (or an empty port) means any. Host may start with "*." for
// a wildcard subdomain match.
//
//	"api.anthropic.com:443,*.githubusercontent.com:443,github.com:*"
//
// Returns a validator that denies everything when the input is empty.
// That posture is deliberate: the proxy must fail closed when no
// allow-list is configured. Operators who genuinely want open egress in
// a test install set a catch-all "*:*".
func NewStaticValidatorFromEnv(raw string) (*StaticValidator, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &StaticValidator{}, nil
	}
	parts := strings.Split(raw, ",")
	rules := make([]AllowRule, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		host, port, err := splitHostPort(p)
		if err != nil {
			return nil, fmt.Errorf("allow-list entry %q: %w", p, err)
		}
		rule := AllowRule{Host: strings.ToLower(host)}
		if port != 0 {
			rule.Ports = []int{port}
		}
		rules = append(rules, rule)
	}
	return &StaticValidator{Allow: rules}, nil
}

func splitHostPort(entry string) (string, int, error) {
	idx := strings.LastIndex(entry, ":")
	if idx < 0 {
		return "", 0, fmt.Errorf("missing port (use host:port, with port=* for any)")
	}
	host := entry[:idx]
	portStr := entry[idx+1:]
	if host == "" {
		return "", 0, fmt.Errorf("missing host")
	}
	if portStr == "*" || portStr == "" {
		return host, 0, nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("port %q: %w", portStr, err)
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port %d out of range", port)
	}
	return host, port, nil
}

// ValidateEgress returns allowed=true when (host, port) matches at least
// one configured rule. StaticValidator has no per-policy attribution;
// MatchedPolicy is set to the literal "static-allow" so downstream
// AuditEvents still carry a non-empty policy name when an allow rule
// matched.
func (v *StaticValidator) ValidateEgress(_ context.Context, host string, port int) (Decision, error) {
	h := strings.ToLower(host)
	for _, r := range v.Allow {
		if !hostMatches(r.Host, h) {
			continue
		}
		if len(r.Ports) == 0 {
			return Decision{Allowed: true, MatchedPolicy: "static-allow"}, nil
		}
		for _, p := range r.Ports {
			if p == port {
				return Decision{Allowed: true, MatchedPolicy: "static-allow"}, nil
			}
		}
	}
	return Decision{
		Allowed: false,
		Reason:  fmt.Sprintf("no BrokerPolicy grants egress to %s:%d", host, port),
	}, nil
}

// hostMatches implements the same matching rules the admission
// intersection uses (internal/policy/intersect.go):
// "*.example.com" matches "api.example.com" but NOT apex "example.com".
// Case-insensitive. Literal "*" is the catch-all.
func hostMatches(pattern, host string) bool {
	pattern = strings.ToLower(pattern)
	host = strings.ToLower(host)
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && host != suffix[1:]
	}
	return pattern == host
}
