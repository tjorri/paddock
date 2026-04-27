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
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/policy"
)

// patPoolBearerPrefix marks bearers minted by this provider. Same
// shape as the other providers — the prefix is both the routing hint
// for the broker's substitute-auth dispatch and the audit tag for
// AuditEvents.
const patPoolBearerPrefix = "pdk-patpool-"

// defaultPATLeaseTTL is the lifetime of one PAT lease. PATs are
// long-lived by design (that's why this provider is marked
// riskLevel=high); the TTL controls in-memory bookkeeping, not PAT
// validity. Longer TTL = fewer mid-run re-leases; shorter TTL = PATs
// return to the pool faster when a run dies. One hour matches the
// other providers' defaults.
const defaultPATLeaseTTL = 60 * time.Minute

// RISK LEVEL: high (spec 0002 §6.3).
//
// PATPoolProvider hands out long-lived personal access tokens from a
// static pool. The operator declaring the pool has accepted that each
// token is broadly scoped against its owning GitHub account (PATs
// can't be installation-scoped the way App tokens are) and that
// revocation is manual. This provider is documented as "homelab and
// migration paths only" — hostile-co-tenant production installs
// should prefer GitHubAppProvider.
//
// Lease model:
//   - Issue picks the first free entry in the pool, returns an opaque
//     Paddock bearer, records (bearer → pool index) under a lease.
//   - SubstituteAuth resolves the bearer to its leased PAT and emits
//     the canonical git Basic-auth form.
//   - Leases expire after defaultPATLeaseTTL; the entry returns to the
//     free set on the next Issue call (opportunistic sweep).
//
// Pool exhaustion returns applicationError-friendly PoolExhausted;
// the broker surfaces that as a 503. Runs then requeue via the
// reconciler's BrokerReady=False path, so a lease that frees up mid-
// run will be picked up on the next reconcile.
//
// Concurrency: Issue and SubstituteAuth are safe for parallel use.
// One provider instance may serve many BrokerPolicies pointing at
// different pool Secrets — leases are keyed by (namespace, secretKey)
// so pools do not cross-contaminate.
type PATPoolProvider struct {
	// Client reads the pool-backing Secret. Secret.Data[key] is
	// newline-delimited; empty lines + lines starting with '#' are
	// skipped. Stability note: the pool ordering matters — Issue picks
	// by index, so inserting a line in the middle may re-order which
	// bearer resolves to which PAT. Operators should append, not insert.
	Client client.Client

	clockSource

	mu    sync.Mutex
	pools map[patPoolKey]*patPool
}

// patPoolKey names one pool — one (namespace, secretName, secretKey)
// tuple. The broker enforces that secretRef.namespace == the run's
// namespace; this struct mirrors that scope.
type patPoolKey struct {
	Namespace string
	Secret    string
	Key       string
}

// patPool tracks the in-use state for one pool. Pool entries are
// kept in the original order they appear in the Secret for audit
// stability; `leased` records the index of the currently-leased PAT
// per bearer. A single bearer leases exactly one entry.
type patPool struct {
	entries []string
	// leased[idx] is true while entry idx is claimed.
	leased []bool
	// byBearer maps bearer → (entry index, expiresAt, runName,
	// credential). A bearer reaching its expiry releases the entry
	// on the next Issue or SubstituteAuth touch.
	byBearer map[string]*patLease
}

type patLease struct {
	Index          int
	RunName        string
	CredentialName string
	ExpiresAt      time.Time
	// AllowedHosts is the list of hostnames this lease may be substituted
	// for. Populated at Issue from grant.Provider.Hosts (admission
	// requires non-empty for PATPool — see brokerpolicy_webhook.go). F-09.
	AllowedHosts []string
	// LeasedPAT is the literal PAT string this lease was minted against.
	// SubstituteAuth re-reads the pool Secret and validates that the
	// entry at lease.Index still matches LeasedPAT before returning —
	// without this check, a PAT removed from the Secret between Issue
	// and SubstituteAuth would still be served (B-06; engineering shape
	// of F-14).
	LeasedPAT string
}

// Prom metrics. Registered with the process default registerer so
// cmd/broker/main.go's /metrics handler exposes them automatically.
var (
	patPoolSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "paddock_broker_patpool_size",
		Help: "Total number of PATs in a Paddock pool, labelled by namespace + backing Secret.",
	}, []string{"namespace", "secret", "key"})

	patPoolLeased = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "paddock_broker_patpool_leased",
		Help: "Number of PATs currently leased from a Paddock pool.",
	}, []string{"namespace", "secret", "key"})

	patPoolExhausted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paddock_broker_patpool_exhausted_total",
		Help: "Count of Issue calls that failed because a Paddock pool was fully leased. High values suggest the operator should grow the pool or migrate to GitHubAppProvider.",
	}, []string{"namespace", "secret", "key"})
)

func init() {
	prometheus.MustRegister(patPoolSize, patPoolLeased, patPoolExhausted)
}

// Compile-time checks.
var (
	_ Provider    = (*PATPoolProvider)(nil)
	_ Substituter = (*PATPoolProvider)(nil)
)

func (p *PATPoolProvider) Name() string { return "PATPool" }

// ErrPoolExhausted is returned when every entry in a pool is leased.
// The broker surfaces this to the caller via the applicationError
// path — runs see BrokerReady=False with reason=PoolExhausted until
// another lease frees up.
var ErrPoolExhausted = errors.New("PAT pool exhausted")

// Issue reads the pool Secret, reconciles free entries, picks the
// first one, records a lease, and returns the bearer.
func (p *PATPoolProvider) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	cfg := req.Grant.Provider
	if cfg.SecretRef == nil {
		return IssueResult{}, fmt.Errorf("PATPoolProvider requires secretRef on grant %q", req.Grant.Name)
	}

	entries, err := p.readPool(ctx, req.Namespace, cfg.SecretRef)
	if err != nil {
		return IssueResult{}, err
	}
	if len(entries) == 0 {
		return IssueResult{}, fmt.Errorf("pool %s/%s key %q is empty",
			req.Namespace, cfg.SecretRef.Name, cfg.SecretRef.Key)
	}

	key := patPoolKey{Namespace: req.Namespace, Secret: cfg.SecretRef.Name, Key: cfg.SecretRef.Key}
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pools == nil {
		p.pools = make(map[patPoolKey]*patPool)
	}
	pool := p.pools[key]
	if pool == nil {
		pool = &patPool{byBearer: make(map[string]*patLease)}
		p.pools[key] = pool
	}

	p.reconcilePoolLocked(key, pool, entries, now)

	idx := firstFreeIndex(pool.leased)
	if idx < 0 {
		patPoolExhausted.WithLabelValues(key.Namespace, key.Secret, key.Key).Inc()
		return IssueResult{}, fmt.Errorf("%w: %s/%s key %q has %d/%d leased",
			ErrPoolExhausted, key.Namespace, key.Secret, key.Key,
			countLeased(pool.leased), len(pool.entries))
	}

	bearer, err := mintBearer(patPoolBearerPrefix)
	if err != nil {
		return IssueResult{}, err
	}
	ttl := defaultPATLeaseTTL
	if cfg.RotationSeconds != nil && *cfg.RotationSeconds > 0 {
		ttl = time.Duration(*cfg.RotationSeconds) * time.Second
	}
	expires := now.Add(ttl)

	pool.leased[idx] = true
	pool.byBearer[bearer] = &patLease{
		Index:          idx,
		RunName:        req.RunName,
		CredentialName: req.CredentialName,
		ExpiresAt:      expires,
		AllowedHosts:   cfg.Hosts,
		LeasedPAT:      pool.entries[idx],
	}
	patPoolLeased.WithLabelValues(key.Namespace, key.Secret, key.Key).Set(float64(countLeased(pool.leased)))

	return IssueResult{
		Value:     bearer,
		LeaseID:   "pat-" + bearer[len(patPoolBearerPrefix):len(patPoolBearerPrefix)+8],
		ExpiresAt: expires,
	}, nil
}

// SubstituteAuth resolves a Paddock bearer to the leased PAT and
// returns the git Basic-auth swap. Returns Matched=true on any
// bearer that begins with our prefix — including unknown/expired
// bearers — so the broker short-circuits rather than trying other
// providers and leaking the Paddock bearer upstream.
func (p *PATPoolProvider) SubstituteAuth(ctx context.Context, req SubstituteRequest) (brokerapi.SubstituteResult, error) {
	bearer := ExtractBearer(req.IncomingBearer)
	if !strings.HasPrefix(bearer, patPoolBearerPrefix) {
		return brokerapi.SubstituteResult{Matched: false}, nil
	}

	// Re-read the pool Secret + reconcile in-memory state so a PAT
	// rotated/removed between Issue and now is reflected before we
	// return a credential. B-06.
	p.mu.Lock()
	var (
		matchedKey   patPoolKey
		matchedPool  *patPool
		matchedLease *patLease
	)
	for k, pool := range p.pools {
		if l, ok := pool.byBearer[bearer]; ok {
			matchedKey = k
			matchedPool = pool
			matchedLease = l
			break
		}
	}
	p.mu.Unlock()
	if matchedLease == nil {
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("patpool bearer not recognised")
	}

	// Re-read the backing Secret outside the lock so a slow apiserver
	// doesn't block other Issue/Substitute calls. The pool key carries
	// everything needed to resolve the Secret.
	freshEntries, err := p.readPool(ctx, matchedKey.Namespace,
		&paddockv1alpha1.SecretKeyReference{
			Name: matchedKey.Secret, Key: matchedKey.Key,
		})
	if err != nil {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("re-reading pool secret: %w", err)
	}

	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	// Reconcile under the lock so a PAT removed from the Secret since
	// we leased is folded into in-memory state before we serve.
	p.reconcilePoolLocked(matchedKey, matchedPool, freshEntries, now)
	// Re-fetch the lease under the lock — reconcile may have dropped
	// it (PAT no longer in pool), or a parallel caller may have
	// released it between our unlock and re-lock above.
	matchedLease, ok := matchedPool.byBearer[bearer]
	if !ok {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("patpool PAT revoked; bearer's PAT is no longer in the pool")
	}
	if req.Namespace != "" && matchedKey.Namespace != req.Namespace {
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("bearer lease namespace %q does not match caller namespace %q",
			matchedKey.Namespace, req.Namespace)
	}
	if now.After(matchedLease.ExpiresAt) {
		p.releaseLocked(matchedKey, matchedPool, bearer)
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("patpool bearer expired")
	}
	if matchedLease.Index < 0 || matchedLease.Index >= len(matchedPool.entries) {
		// Reconcile should have dropped this; defensive fallthrough.
		p.releaseLocked(matchedKey, matchedPool, bearer)
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("patpool shrank; bearer's lease index is stale")
	}
	// Defence in depth: even after reconcile, explicitly verify the
	// entry at the lease index matches the PAT we leased. Catches any
	// future reconcile bug that drops PATs without dropping the lease.
	if matchedPool.entries[matchedLease.Index] != matchedLease.LeasedPAT {
		p.releaseLocked(matchedKey, matchedPool, bearer)
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("patpool PAT revoked; entry at lease index does not match leased PAT")
	}
	if !policy.AnyHostMatches(matchedLease.AllowedHosts, req.Host) {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("bearer host %q not in lease's allowed hosts %v", req.Host, matchedLease.AllowedHosts)
	}

	pat := matchedPool.entries[matchedLease.Index]
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + pat))
	return brokerapi.SubstituteResult{
		Matched: true,
		SetHeaders: map[string]string{
			"Authorization": "Basic " + basic,
		},
		// F-21: same allowlist as GitHubApp — both back GitHub-shaped traffic.
		AllowedHeaders: []string{
			"Content-Type", "Content-Length",
			"Accept", "Accept-Encoding", "User-Agent",
			"X-GitHub-Api-Version",
		},
		AllowedQueryParams: nil,
		CredentialName:     matchedLease.CredentialName,
	}, nil
}

// reconcilePoolLocked folds the on-disk pool entries into the in-memory
// pool state: new entries get a false slot, missing entries are pruned
// from any stale leases. Expired leases are released. Call with p.mu
// held.
func (p *PATPoolProvider) reconcilePoolLocked(key patPoolKey, pool *patPool, fresh []string, now time.Time) {
	// Sweep expired leases first — frees slots for reuse even when the
	// Secret hasn't changed.
	for bearer, lease := range pool.byBearer {
		if now.After(lease.ExpiresAt) {
			if lease.Index >= 0 && lease.Index < len(pool.leased) {
				pool.leased[lease.Index] = false
			}
			delete(pool.byBearer, bearer)
		}
	}

	// If the pool's on-disk shape hasn't changed, we can skip the
	// expensive compare. Common hot path.
	if stringsEqual(pool.entries, fresh) {
		patPoolSize.WithLabelValues(key.Namespace, key.Secret, key.Key).Set(float64(len(pool.entries)))
		patPoolLeased.WithLabelValues(key.Namespace, key.Secret, key.Key).Set(float64(countLeased(pool.leased)))
		return
	}

	// Preserve existing lease indices where possible so a bearer minted
	// pre-edit still resolves to the same PAT post-edit as long as the
	// PAT string is still present.
	oldByIndex := pool.entries
	newEntries := fresh
	newLeased := make([]bool, len(newEntries))
	newByBearer := make(map[string]*patLease, len(pool.byBearer))

	// Map each bearer to its new index (by PAT value). Bearers whose PAT
	// is no longer in the pool get dropped — their leased slot is gone.
	for bearer, lease := range pool.byBearer {
		if lease.Index < 0 || lease.Index >= len(oldByIndex) {
			continue
		}
		oldPAT := oldByIndex[lease.Index]
		newIdx := indexOf(newEntries, oldPAT)
		if newIdx < 0 {
			continue
		}
		lease.Index = newIdx
		newLeased[newIdx] = true
		newByBearer[bearer] = lease
	}
	pool.entries = newEntries
	pool.leased = newLeased
	pool.byBearer = newByBearer

	patPoolSize.WithLabelValues(key.Namespace, key.Secret, key.Key).Set(float64(len(pool.entries)))
	patPoolLeased.WithLabelValues(key.Namespace, key.Secret, key.Key).Set(float64(countLeased(pool.leased)))
}

// releaseLocked drops a bearer's lease and frees its pool slot.
// Call with p.mu held.
func (p *PATPoolProvider) releaseLocked(key patPoolKey, pool *patPool, bearer string) {
	lease, ok := pool.byBearer[bearer]
	if !ok {
		return
	}
	if lease.Index >= 0 && lease.Index < len(pool.leased) {
		pool.leased[lease.Index] = false
	}
	delete(pool.byBearer, bearer)
	patPoolLeased.WithLabelValues(key.Namespace, key.Secret, key.Key).Set(float64(countLeased(pool.leased)))
}

// readPool loads the pool Secret and returns the trimmed, non-empty
// entries. Comments (lines starting with '#') and blank lines are
// skipped so operators can annotate rotations inline.
func (p *PATPoolProvider) readPool(ctx context.Context, namespace string, ref *paddockv1alpha1.SecretKeyReference) ([]string, error) {
	var secret corev1.Secret
	if err := p.Client.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: namespace}, &secret); err != nil {
		return nil, fmt.Errorf("reading secret %s/%s: %w", namespace, ref.Name, err)
	}
	raw, ok := secret.Data[ref.Key]
	if !ok {
		return nil, fmt.Errorf("key %q not present in secret %s/%s", ref.Key, namespace, ref.Name)
	}
	return parsePoolEntries(raw), nil
}

// parsePoolEntries splits the raw Secret bytes on newline, trims
// whitespace, and drops empty + comment lines. Exported-style naming
// kept lowercase because only the provider needs it.
func parsePoolEntries(raw []byte) []string {
	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func firstFreeIndex(leased []bool) int {
	for i, v := range leased {
		if !v {
			return i
		}
	}
	return -1
}

func countLeased(leased []bool) int {
	n := 0
	for _, v := range leased {
		if v {
			n++
		}
	}
	return n
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func indexOf(ss []string, v string) int {
	for i, s := range ss {
		if s == v {
			return i
		}
	}
	return -1
}
