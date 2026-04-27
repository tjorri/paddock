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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// anthropicBearerPrefix is both the "how the proxy recognises this
// bearer" marker and the "how the broker routes an incoming
// SubstituteAuth call to the right provider" marker. A compromised
// upstream that sees the bearer would see only the prefix + 48 random
// hex digits — nothing upstream-usable.
const anthropicBearerPrefix = "pdk-anthropic-"

// defaultAnthropicTTL bounds how long an issued bearer remains valid
// before the provider refuses to substitute it. Matches the default
// Pod lifetime budget (1h) — long enough that a single claude-code run
// never renews mid-session, short enough that a leaked bearer doesn't
// outlive its owning run by much. Overridable per-grant via
// rotationSeconds.
const defaultAnthropicTTL = 60 * time.Minute

// AnthropicAPIProvider mints opaque bearers for runs targeting
// api.anthropic.com. The agent sees only the Paddock-issued bearer;
// the proxy's MITM path calls SubstituteAuth at request-time to swap
// it for the real x-api-key read fresh from the backing Secret. See
// ADR-0015 §"AnthropicAPIProvider" and spec 0002 §6.3.
//
// Concurrency: Issue and SubstituteAuth are safe for parallel use.
// Expired leases are swept opportunistically on each Issue call —
// v0.3 keeps a single in-memory map; a future milestone can swap this
// for a cached-Secret + bearer-as-HMAC design if lease volume warrants.
type AnthropicAPIProvider struct {
	// Client reads the BrokerPolicy-referenced Secret at both Issue
	// time (to fail fast if the key is missing) and SubstituteAuth time
	// (so rotations land on the next request without bearer re-issue).
	Client client.Client

	clockSource

	mu      sync.Mutex
	bearers map[string]*anthropicLease
}

// anthropicLease records what a minted bearer stands for. Kept small
// and self-contained — the provider can cold-start without recovering
// leases, since a cold broker also kicks out any in-flight runs via
// BrokerReady=False.
type anthropicLease struct {
	Namespace      string
	SecretRef      paddockv1alpha1.SecretKeyReference
	RunName        string
	CredentialName string
	ExpiresAt      time.Time
	// AllowedHosts is the list of hostnames this lease may be substituted
	// for. Populated at Issue from grant.Provider.Hosts (or the default
	// [api.anthropic.com] when the grant omits it). SubstituteAuth
	// rejects a request whose req.Host is not on this list. F-09.
	AllowedHosts []string
}

// Compile-time checks.
var (
	_ Provider    = (*AnthropicAPIProvider)(nil)
	_ Substituter = (*AnthropicAPIProvider)(nil)
)

func (p *AnthropicAPIProvider) Name() string { return "AnthropicAPI" }

// Issue verifies the backing Secret is readable, mints a fresh opaque
// bearer, and records the lease. The agent receives Value; the real
// API key never leaves the broker until SubstituteAuth.
func (p *AnthropicAPIProvider) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	cfg := req.Grant.Provider
	if cfg.SecretRef == nil {
		return IssueResult{}, fmt.Errorf("AnthropicAPIProvider requires secretRef on grant %q", req.Grant.Name)
	}

	// Fail-fast Secret read — avoids issuing a bearer that can't be
	// substituted. The SubstituteAuth path re-reads at request time so
	// rotations stay live.
	var secret corev1.Secret
	key := types.NamespacedName{Name: cfg.SecretRef.Name, Namespace: req.Namespace}
	if err := p.Client.Get(ctx, key, &secret); err != nil {
		return IssueResult{}, fmt.Errorf("reading secret %s/%s: %w", req.Namespace, cfg.SecretRef.Name, err)
	}
	if len(secret.Data[cfg.SecretRef.Key]) == 0 {
		return IssueResult{}, fmt.Errorf("key %q not present or empty in secret %s/%s",
			cfg.SecretRef.Key, req.Namespace, cfg.SecretRef.Name)
	}

	// 24 random bytes → 48 hex chars. Paired with the 14-char prefix
	// that's 62 chars of bearer — plenty of entropy, short enough for
	// Authorization headers.
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return IssueResult{}, fmt.Errorf("generating bearer: %w", err)
	}
	bearer := anthropicBearerPrefix + hex.EncodeToString(buf[:])

	now := p.now()
	ttl := defaultAnthropicTTL
	if cfg.RotationSeconds != nil && *cfg.RotationSeconds > 0 {
		ttl = time.Duration(*cfg.RotationSeconds) * time.Second
	}
	expires := now.Add(ttl)

	allowedHosts := cfg.Hosts
	if len(allowedHosts) == 0 {
		allowedHosts = []string{"api.anthropic.com"}
	}
	lease := &anthropicLease{
		Namespace:      req.Namespace,
		SecretRef:      *cfg.SecretRef,
		RunName:        req.RunName,
		CredentialName: req.CredentialName,
		ExpiresAt:      expires,
		AllowedHosts:   allowedHosts,
	}
	p.mu.Lock()
	if p.bearers == nil {
		p.bearers = make(map[string]*anthropicLease)
	}
	p.bearers[bearer] = lease
	// Opportunistic sweep. Bounded by the number of concurrent runs the
	// broker is serving; keeps the map from growing unbounded across a
	// long broker uptime.
	for b, l := range p.bearers {
		if l.ExpiresAt.Before(now) {
			delete(p.bearers, b)
		}
	}
	p.mu.Unlock()

	return IssueResult{
		Value: bearer,
		// LeaseID lets M8's Revoke hook find the lease without leaking
		// the full bearer into AuditEvents. Prefix picks the first 8
		// random hex chars; collision probability is 1/2^32 per run, and
		// LeaseID only needs to be unique within the broker's lifetime.
		LeaseID:   "anth-" + bearer[len(anthropicBearerPrefix):len(anthropicBearerPrefix)+8],
		ExpiresAt: expires,
	}, nil
}

// SubstituteAuth implements providers.Substituter. Returns Matched=true
// when IncomingBearer is one this provider minted — even on error
// (expired / revoked) — so the broker doesn't fall through to the next
// provider and accidentally let the request out with the wrong swap.
func (p *AnthropicAPIProvider) SubstituteAuth(ctx context.Context, req SubstituteRequest) (brokerapi.SubstituteResult, error) {
	// Lenient extraction: handles "Bearer X", raw tokens, and the "Basic"
	// shape git + the go-git library emit. Providers that wanted the
	// unparsed value would bypass this helper — none do today.
	bearer := ExtractBearer(req.IncomingBearer)
	if !strings.HasPrefix(bearer, anthropicBearerPrefix) {
		return brokerapi.SubstituteResult{Matched: false}, nil
	}

	p.mu.Lock()
	lease, ok := p.bearers[bearer]
	p.mu.Unlock()
	if !ok {
		// Prefix matches ours but we don't know this bearer. Claim
		// Matched=true anyway — returning false would tell the broker to
		// try other providers; safer to short-circuit with an explicit
		// error.
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("anthropic bearer not recognised")
	}
	// Namespace mismatch is a programming error upstream (broker should
	// scope by caller's namespace before calling us); be defensive.
	if req.Namespace != "" && lease.Namespace != req.Namespace {
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("bearer lease namespace %q does not match caller namespace %q", lease.Namespace, req.Namespace)
	}
	if p.now().After(lease.ExpiresAt) {
		p.mu.Lock()
		delete(p.bearers, bearer)
		p.mu.Unlock()
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("anthropic bearer expired")
	}
	if !hostMatchesGlobs(req.Host, lease.AllowedHosts) {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("bearer host %q not in lease's allowed hosts %v", req.Host, lease.AllowedHosts)
	}

	var secret corev1.Secret
	key := types.NamespacedName{Name: lease.SecretRef.Name, Namespace: lease.Namespace}
	if err := p.Client.Get(ctx, key, &secret); err != nil {
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("reading secret %s/%s: %w", lease.Namespace, lease.SecretRef.Name, err)
	}
	data, ok := secret.Data[lease.SecretRef.Key]
	if !ok || len(data) == 0 {
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("key %q missing or empty in secret %s/%s",
			lease.SecretRef.Key, lease.Namespace, lease.SecretRef.Name)
	}

	// Anthropic's REST API authenticates via x-api-key. We also drop
	// Authorization if present, so a belt-and-braces agent that sent
	// both doesn't leak the Paddock bearer upstream.
	return brokerapi.SubstituteResult{
		Matched: true,
		SetHeaders: map[string]string{
			"x-api-key": string(data),
		},
		RemoveHeaders: []string{"Authorization"},
		// F-21: minimal protocol-relevant header allowlist. Any agent header
		// not in this set (and not in SetHeaders) is stripped by the proxy.
		AllowedHeaders: []string{
			"Content-Type", "Content-Length",
			"Accept", "Accept-Encoding", "User-Agent",
			"Anthropic-Version", "Anthropic-Beta",
		},
		// F-21: Anthropic's REST API doesn't use query-param auth; empty list.
		AllowedQueryParams: nil,
		// F-10: the broker handler re-validates matchPolicyGrant against
		// this name before returning the substituted credential.
		CredentialName: lease.CredentialName,
	}, nil
}
