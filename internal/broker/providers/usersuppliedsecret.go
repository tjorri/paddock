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
	"crypto/sha256"
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

const userSuppliedBearerPrefix = "pdk-usersecret-"
const defaultUserSuppliedTTL = 60 * time.Minute

// UserSuppliedSecretProvider backs BrokerPolicy grants whose provider
// kind is UserSuppliedSecret. Delivery mode is read from the grant:
//
//   - InContainer: returns the Secret value directly. The agent sees
//     plaintext; the operator consented via
//     deliveryMode.inContainer.accepted=true with a written reason.
//   - ProxyInjected: mints an opaque bearer and records a lease keyed
//     on the bearer. Implements Substituter so the proxy swaps the
//     bearer for the real secret value per the grant's configured
//     pattern (header / queryParam / basicAuth).
type UserSuppliedSecretProvider struct {
	Client client.Client
	Now    func() time.Time

	mu      sync.Mutex
	bearers map[string]*userSuppliedLease
}

type userSuppliedLease struct {
	Namespace      string
	SecretRef      paddockv1alpha1.SecretKeyReference
	RunName        string
	CredentialName string
	ExpiresAt      time.Time
	ProxyInjected  paddockv1alpha1.ProxyInjectedDelivery
}

var (
	_ Provider    = (*UserSuppliedSecretProvider)(nil)
	_ Substituter = (*UserSuppliedSecretProvider)(nil)
)

func (p *UserSuppliedSecretProvider) Name() string { return "UserSuppliedSecret" }

func (p *UserSuppliedSecretProvider) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	cfg := req.Grant.Provider
	if cfg.SecretRef == nil {
		return IssueResult{}, fmt.Errorf("UserSuppliedSecret requires secretRef on grant %q", req.Grant.Name)
	}
	if cfg.DeliveryMode == nil {
		return IssueResult{}, fmt.Errorf("UserSuppliedSecret grant %q has no deliveryMode (should have been caught at admission)", req.Grant.Name)
	}

	var secret corev1.Secret
	key := types.NamespacedName{Name: cfg.SecretRef.Name, Namespace: req.Namespace}
	if err := p.Client.Get(ctx, key, &secret); err != nil {
		return IssueResult{}, fmt.Errorf("reading secret %s/%s: %w", req.Namespace, cfg.SecretRef.Name, err)
	}
	data, ok := secret.Data[cfg.SecretRef.Key]
	if !ok {
		return IssueResult{}, fmt.Errorf("key %q not present in secret %s/%s",
			cfg.SecretRef.Key, req.Namespace, cfg.SecretRef.Name)
	}

	if cfg.DeliveryMode.InContainer != nil {
		// Deterministic lease ID: same (ns, run, credential, secret
		// resourceVersion) → same ID. Makes reconciler idempotency
		// trivial and lets audit dedupe unchanged reads. Mirrors the
		// v0.3 StaticProvider behaviour.
		sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s",
			req.Namespace, req.RunName, req.CredentialName, secret.ResourceVersion))
		leaseID := "uss-" + hex.EncodeToString(sum[:8])
		var expiresAt time.Time
		if cfg.RotationSeconds != nil && *cfg.RotationSeconds > 0 {
			expiresAt = p.now().Add(time.Duration(*cfg.RotationSeconds) * time.Second)
		}
		return IssueResult{Value: string(data), LeaseID: leaseID, ExpiresAt: expiresAt}, nil
	}

	// ProxyInjected path: mint an opaque bearer and keep the real
	// value only inside the broker. SubstituteAuth re-reads the Secret
	// at request time so rotations land without reissue.
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return IssueResult{}, fmt.Errorf("generating bearer: %w", err)
	}
	bearer := userSuppliedBearerPrefix + hex.EncodeToString(buf[:])

	ttl := defaultUserSuppliedTTL
	if cfg.RotationSeconds != nil && *cfg.RotationSeconds > 0 {
		ttl = time.Duration(*cfg.RotationSeconds) * time.Second
	}
	expires := p.now().Add(ttl)

	lease := &userSuppliedLease{
		Namespace:      req.Namespace,
		SecretRef:      *cfg.SecretRef,
		RunName:        req.RunName,
		CredentialName: req.CredentialName,
		ExpiresAt:      expires,
		ProxyInjected:  *cfg.DeliveryMode.ProxyInjected,
	}
	p.mu.Lock()
	if p.bearers == nil {
		p.bearers = make(map[string]*userSuppliedLease)
	}
	p.bearers[bearer] = lease
	// Opportunistic sweep: we don't run a dedicated janitor; expired
	// leases get reaped whenever a new one is minted. Safe because the
	// lease map is only read under the same mutex in SubstituteAuth.
	now := p.now()
	for b, l := range p.bearers {
		if l.ExpiresAt.Before(now) {
			delete(p.bearers, b)
		}
	}
	p.mu.Unlock()

	return IssueResult{
		Value:     bearer,
		LeaseID:   "uss-" + bearer[len(userSuppliedBearerPrefix):len(userSuppliedBearerPrefix)+8],
		ExpiresAt: expires,
	}, nil
}

func (p *UserSuppliedSecretProvider) SubstituteAuth(ctx context.Context, req SubstituteRequest) (brokerapi.SubstituteResult, error) {
	bearer := ExtractBearer(req.IncomingBearer)
	if !strings.HasPrefix(bearer, userSuppliedBearerPrefix) {
		return brokerapi.SubstituteResult{Matched: false}, nil
	}

	p.mu.Lock()
	lease, ok := p.bearers[bearer]
	p.mu.Unlock()
	if !ok {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("UserSuppliedSecret bearer not recognised")
	}
	if req.Namespace != "" && lease.Namespace != req.Namespace {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("bearer lease namespace %q does not match caller namespace %q", lease.Namespace, req.Namespace)
	}
	if p.now().After(lease.ExpiresAt) {
		p.mu.Lock()
		delete(p.bearers, bearer)
		p.mu.Unlock()
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("UserSuppliedSecret bearer expired")
	}
	if !hostMatchesGlobs(req.Host, lease.ProxyInjected.Hosts) {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("bearer host %q not in grant's allowed hosts %v", req.Host, lease.ProxyInjected.Hosts)
	}

	// Re-read the Secret so Secret-side rotations land without the
	// caller having to reissue. Admission already guarantees the
	// operator's BrokerPolicy grant points at an existing Secret; this
	// path is the only place we need to handle post-issue disappearance.
	var secret corev1.Secret
	key := types.NamespacedName{Name: lease.SecretRef.Name, Namespace: lease.Namespace}
	if err := p.Client.Get(ctx, key, &secret); err != nil {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("reading secret %s/%s: %w", lease.Namespace, lease.SecretRef.Name, err)
	}
	data, ok := secret.Data[lease.SecretRef.Key]
	if !ok || len(data) == 0 {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("key %q missing or empty in secret %s/%s",
				lease.SecretRef.Key, lease.Namespace, lease.SecretRef.Name)
	}
	value := string(data)

	res := brokerapi.SubstituteResult{
		Matched: true,
		// F-21: minimal protocol-relevant header allowlist. UserSuppliedSecret
		// has no per-grant override yet (deferred); operators wanting custom
		// headers should declare them via deliveryMode.proxyInjected.header
		// which then lands in SetHeaders below.
		AllowedHeaders: []string{
			"Content-Type", "Content-Length",
			"Accept", "Accept-Encoding", "User-Agent",
		},
		AllowedQueryParams: nil,
		// F-10: handler re-validates matchPolicyGrant against this name.
		CredentialName: lease.CredentialName,
	}
	switch {
	case lease.ProxyInjected.Header != nil:
		res.SetHeaders = map[string]string{
			lease.ProxyInjected.Header.Name: lease.ProxyInjected.Header.ValuePrefix + value,
		}
	case lease.ProxyInjected.QueryParam != nil:
		res.SetQueryParam = map[string]string{
			lease.ProxyInjected.QueryParam.Name: value,
		}
		// The grant explicitly declared this query parameter as the
		// substitution target; allow the upstream to receive it.
		res.AllowedQueryParams = []string{lease.ProxyInjected.QueryParam.Name}
	case lease.ProxyInjected.BasicAuth != nil:
		res.SetBasicAuth = &brokerapi.BasicAuth{
			Username: lease.ProxyInjected.BasicAuth.Username,
			Password: value,
		}
	default:
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("lease for %s has no substitution pattern set", bearer)
	}
	return res, nil
}

func (p *UserSuppliedSecretProvider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// hostMatchesGlobs does a limited glob match: either exact host equality
// or a `*.example.com` style wildcard that matches any single-or-multi
// label subdomain but not the bare apex (to avoid surprising the
// operator). Case/whitespace insensitive.
func hostMatchesGlobs(host string, hosts []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if strings.HasPrefix(h, "*.") {
			suffix := h[1:]
			if strings.HasSuffix(host, suffix) && host != suffix[1:] {
				return true
			}
			continue
		}
		if h == host {
			return true
		}
	}
	return false
}
