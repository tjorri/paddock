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
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// githubAppBearerPrefix is the "I minted this" marker. Shape matches
// anthropicBearerPrefix: prefix + 48 hex chars so every bearer is
// self-describing and unambiguous in a logline or AuditEvent.
const githubAppBearerPrefix = "pdk-github-"

// defaultGitHubAppTokenTTL is how long a GitHub App installation token
// lives. GitHub mints these at exactly 1h — the provider refreshes a
// few minutes before expiry so no active request sees a stale token.
const defaultGitHubAppTokenTTL = 60 * time.Minute

// installationTokenRefreshBefore is the slack window we rotate within.
// An 8-minute buffer keeps a long clone (GitHub's LFS fetch path has
// minute-scale bursts) from straddling an expiry.
const installationTokenRefreshBefore = 8 * time.Minute

// defaultGitHubAPIEndpoint is the public GitHub API root. Override via
// BrokerPolicy.spec.grants.credentials[*].provider for GitHub Enterprise.
// Kept centralised here rather than on the provider struct so tests
// can swap per-run via an httptest endpoint.
const defaultGitHubAPIEndpoint = "https://api.github.com"

// GitHubAppProvider mints short-lived installation tokens for a GitHub
// App. Each Issue call returns an opaque Paddock bearer keyed to the
// run; SubstituteAuth resolves the bearer to an installation token
// obtained via GitHub's /app/installations/{id}/access_tokens endpoint
// and scoped to the BrokerPolicy grant's gitRepos list (double-gated
// against the installation's actually-installed repos by GitHub
// itself).
//
// Token reuse: all bearers issued for the same (run, credential) tuple
// resolve to the *same* cached installation token, so a run's seed Job
// and agent Pod never consume two tokens off the App's rate-limit
// budget. The token is refreshed in-place when it has less than
// installationTokenRefreshBefore remaining.
//
// Concurrency: Issue + SubstituteAuth are safe under parallel use.
// Expired leases + tokens are swept opportunistically on each Issue.
type GitHubAppProvider struct {
	// Client reads the App's private-key Secret at Issue time (fail
	// fast if the Secret is missing) and at SubstituteAuth time (so a
	// key rotation picks up on the next request).
	Client client.Client

	// HTTPClient is used for the GitHub API calls. Tests inject a
	// client wired to an httptest server; production defaults to
	// http.DefaultClient with a modest timeout.
	HTTPClient *http.Client

	// APIEndpoint overrides the GitHub API root. Primarily for tests;
	// future milestones can expose it on BrokerPolicy for GHE installs.
	APIEndpoint string

	// Now is the wall-clock source for TTL accounting. Zero defaults
	// to time.Now — tests inject a fixed clock.
	Now func() time.Time

	mu      sync.Mutex
	bearers map[string]*githubLease
	tokens  map[githubTokenKey]*installationToken
}

// githubLease tracks what a minted Paddock bearer stands for. The
// provider config is copied in at Issue time so a later BrokerPolicy
// edit doesn't silently shift which App a live bearer maps to.
type githubLease struct {
	Namespace      string
	SecretRef      paddockv1alpha1.SecretKeyReference
	AppID          string
	InstallationID string
	RunName        string
	CredentialName string
	Repositories   []string
	APIEndpoint    string
	ExpiresAt      time.Time
}

// githubTokenKey uniquely identifies one cached installation token.
// Scoping on (run, cred) means the seed + agent of the same run share
// a token; two parallel runs against the same App do not. That's the
// invariant M8 requires (GitHub App rate-limit budget).
type githubTokenKey struct {
	RunName        string
	Namespace      string
	CredentialName string
}

// installationToken is a cached /access_tokens response plus the
// absolute expiry the provider treats as authoritative.
type installationToken struct {
	Token     string
	ExpiresAt time.Time
}

// Compile-time checks.
var (
	_ Provider    = (*GitHubAppProvider)(nil)
	_ Substituter = (*GitHubAppProvider)(nil)
)

func (p *GitHubAppProvider) Name() string { return "GitHubApp" }

// Issue validates the provider configuration, mints a fresh opaque
// bearer, records the lease, and returns the bearer to the caller.
// The actual installation-token fetch is deferred to the first
// SubstituteAuth call; this keeps credential issuance on the run's
// hot path free of an external HTTP dependency.
func (p *GitHubAppProvider) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	cfg := req.Grant.Provider
	if cfg.SecretRef == nil {
		return IssueResult{}, fmt.Errorf("GitHubAppProvider requires secretRef on grant %q", req.Grant.Name)
	}
	if strings.TrimSpace(cfg.AppID) == "" {
		return IssueResult{}, fmt.Errorf("GitHubAppProvider requires appId on grant %q", req.Grant.Name)
	}
	if strings.TrimSpace(cfg.InstallationID) == "" {
		return IssueResult{}, fmt.Errorf("GitHubAppProvider requires installationId on grant %q", req.Grant.Name)
	}

	// Fail-fast Secret read. The actual key material is parsed again
	// on SubstituteAuth so rotations land without bearer reissue.
	var secret corev1.Secret
	if err := p.Client.Get(ctx, types.NamespacedName{Name: cfg.SecretRef.Name, Namespace: req.Namespace}, &secret); err != nil {
		return IssueResult{}, fmt.Errorf("reading secret %s/%s: %w", req.Namespace, cfg.SecretRef.Name, err)
	}
	if len(secret.Data[cfg.SecretRef.Key]) == 0 {
		return IssueResult{}, fmt.Errorf("key %q not present or empty in secret %s/%s",
			cfg.SecretRef.Key, req.Namespace, cfg.SecretRef.Name)
	}
	if _, err := parsePrivateKey(secret.Data[cfg.SecretRef.Key]); err != nil {
		return IssueResult{}, fmt.Errorf("parsing private key from secret %s/%s: %w",
			req.Namespace, cfg.SecretRef.Name, err)
	}

	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return IssueResult{}, fmt.Errorf("generating bearer: %w", err)
	}
	bearer := githubAppBearerPrefix + hex.EncodeToString(buf[:])

	// Repositories list: the BrokerPolicy's gitRepos — bounded to
	// this policy's owner-declared scope. The GitHub API intersects
	// this with the installation's actual repo list (double-gate);
	// bearers minted against a policy that grants repos outside the
	// installation still materialise tokens, just scoped tighter.
	repos := repoNamesFromGitRepos(req.GitRepos)
	apiEndpoint := p.APIEndpoint
	if apiEndpoint == "" {
		apiEndpoint = defaultGitHubAPIEndpoint
	}

	now := p.now()
	ttl := defaultGitHubAppTokenTTL
	// Bearers live ≤ the TTL of the installation token they resolve
	// to. Agent code treats ExpiresAt as advisory; the proxy drops the
	// connection as soon as SubstituteAuth returns an error anyway.
	expires := now.Add(ttl)

	lease := &githubLease{
		Namespace:      req.Namespace,
		SecretRef:      *cfg.SecretRef,
		AppID:          cfg.AppID,
		InstallationID: cfg.InstallationID,
		RunName:        req.RunName,
		CredentialName: req.CredentialName,
		Repositories:   repos,
		APIEndpoint:    apiEndpoint,
		ExpiresAt:      expires,
	}

	p.mu.Lock()
	if p.bearers == nil {
		p.bearers = make(map[string]*githubLease)
	}
	if p.tokens == nil {
		p.tokens = make(map[githubTokenKey]*installationToken)
	}
	p.bearers[bearer] = lease
	p.sweep(now)
	p.mu.Unlock()

	return IssueResult{
		Value:     bearer,
		LeaseID:   "gha-" + bearer[len(githubAppBearerPrefix):len(githubAppBearerPrefix)+8],
		ExpiresAt: expires,
	}, nil
}

// SubstituteAuth swaps a Paddock bearer for the real installation
// token at MITM time. Returns Matched=true whenever the bearer looks
// like one of ours (prefix match) even on error, so the broker's
// substitute-auth handler short-circuits and the proxy drops the
// connection rather than letting the Paddock bearer leak upstream.
//
// The returned headers swap the agent's Authorization value for
// "Basic base64(x-access-token:<real-token>)" — git's canonical
// HTTPS auth form. Upstream GitHub accepts the same credential on
// "Bearer <token>" too; we emit Basic because it works for both git's
// libcurl path and the go-git client the seed Job uses.
func (p *GitHubAppProvider) SubstituteAuth(ctx context.Context, req SubstituteRequest) (SubstituteResult, error) {
	bearer := ExtractBearer(req.IncomingBearer)
	if !strings.HasPrefix(bearer, githubAppBearerPrefix) {
		return SubstituteResult{Matched: false}, nil
	}

	p.mu.Lock()
	lease, ok := p.bearers[bearer]
	p.mu.Unlock()
	if !ok {
		return SubstituteResult{Matched: true}, fmt.Errorf("github bearer not recognised")
	}
	if req.Namespace != "" && lease.Namespace != req.Namespace {
		return SubstituteResult{Matched: true}, fmt.Errorf("bearer lease namespace %q does not match caller namespace %q",
			lease.Namespace, req.Namespace)
	}
	if p.now().After(lease.ExpiresAt) {
		p.mu.Lock()
		delete(p.bearers, bearer)
		p.mu.Unlock()
		return SubstituteResult{Matched: true}, fmt.Errorf("github bearer expired")
	}

	token, err := p.resolveInstallationToken(ctx, lease)
	if err != nil {
		return SubstituteResult{Matched: true}, err
	}

	// Basic-auth with username "x-access-token" is the canonical form
	// GitHub documents for App-issued tokens. Drop any Bearer/Basic
	// the agent presented — the Paddock bearer must not leak upstream.
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return SubstituteResult{
		Matched: true,
		SetHeaders: map[string]string{
			"Authorization": "Basic " + basic,
		},
	}, nil
}

// resolveInstallationToken returns a valid cached installation token
// for the lease, minting a fresh one if the cache is empty or near
// expiry. Synchronous per call; GitHub's API is fast enough (<300ms
// typical) that contention is negligible under normal run rates.
func (p *GitHubAppProvider) resolveInstallationToken(ctx context.Context, lease *githubLease) (string, error) {
	key := githubTokenKey{
		RunName:        lease.RunName,
		Namespace:      lease.Namespace,
		CredentialName: lease.CredentialName,
	}

	p.mu.Lock()
	cached := p.tokens[key]
	p.mu.Unlock()
	now := p.now()
	if cached != nil && now.Add(installationTokenRefreshBefore).Before(cached.ExpiresAt) {
		return cached.Token, nil
	}

	// Re-read the Secret on every refresh so a rotated App private key
	// takes effect without any lease re-issuance.
	var secret corev1.Secret
	secretKey := types.NamespacedName{Name: lease.SecretRef.Name, Namespace: lease.Namespace}
	if err := p.Client.Get(ctx, secretKey, &secret); err != nil {
		return "", fmt.Errorf("reading secret %s/%s: %w", lease.Namespace, lease.SecretRef.Name, err)
	}
	privPEM := secret.Data[lease.SecretRef.Key]
	if len(privPEM) == 0 {
		return "", fmt.Errorf("key %q missing or empty in secret %s/%s",
			lease.SecretRef.Key, lease.Namespace, lease.SecretRef.Name)
	}
	privKey, err := parsePrivateKey(privPEM)
	if err != nil {
		return "", fmt.Errorf("parsing private key: %w", err)
	}

	jwt, err := signAppJWT(lease.AppID, privKey, now)
	if err != nil {
		return "", fmt.Errorf("signing app JWT: %w", err)
	}
	token, expires, err := p.exchangeInstallationToken(ctx, lease, jwt)
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	p.tokens[key] = &installationToken{Token: token, ExpiresAt: expires}
	p.mu.Unlock()
	return token, nil
}

// exchangeInstallationToken POSTs to /app/installations/{id}/access_tokens
// with the signed App JWT, optionally scoping the token via a
// repositories[] list. Returns the minted token + its expiry exactly
// as GitHub reports them — the server is authoritative, not our
// defaultGitHubAppTokenTTL.
func (p *GitHubAppProvider) exchangeInstallationToken(ctx context.Context, lease *githubLease, jwt string) (string, time.Time, error) {
	endpoint := strings.TrimRight(lease.APIEndpoint, "/") +
		"/app/installations/" + lease.InstallationID + "/access_tokens"

	var body []byte
	if len(lease.Repositories) > 0 {
		payload := map[string]any{"repositories": lease.Repositories}
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("marshalling access_tokens body: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	hc := p.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", time.Time{}, fmt.Errorf("github access_tokens returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, fmt.Errorf("decoding access_tokens response: %w", err)
	}
	if out.Token == "" {
		return "", time.Time{}, errors.New("github returned empty token")
	}
	expires := out.ExpiresAt
	if expires.IsZero() {
		expires = p.now().Add(defaultGitHubAppTokenTTL)
	}
	return out.Token, expires, nil
}

// sweep drops expired bearers + tokens. Holds the provider lock; call
// from under it.
func (p *GitHubAppProvider) sweep(now time.Time) {
	for bearer, lease := range p.bearers {
		if lease.ExpiresAt.Before(now) {
			delete(p.bearers, bearer)
		}
	}
	for key, tok := range p.tokens {
		if tok.ExpiresAt.Before(now) {
			delete(p.tokens, key)
		}
	}
}

func (p *GitHubAppProvider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// parsePrivateKey accepts the two shapes GitHub ships App private keys
// in: traditional PKCS#1 ("RSA PRIVATE KEY") and modern PKCS#8
// ("PRIVATE KEY"). Anything else (DSA, EC, keypairs-encrypted-with-
// password) is rejected — App auth requires an RSA key.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("expected RSA private key, got %T", k)
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("unexpected PEM block type %q (expected RSA PRIVATE KEY or PRIVATE KEY)", block.Type)
	}
}

// signAppJWT builds the RS256 JWT GitHub uses to authenticate as the
// App. iss = App ID, iat = now, exp = now + 9 minutes (GitHub caps at
// 10; one-minute slack covers clock drift between pods). No external
// JWT library — the shape is small and stable.
func signAppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	header := `{"alg":"RS256","typ":"JWT"}`
	claims, err := json.Marshal(map[string]any{
		// GitHub treats iat as <= now; clock skew of up to 60s has bitten
		// us in practice, so we subtract a small buffer.
		"iat": now.Add(-30 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}

	signingInput := base64URLEncode([]byte(header)) + "." + base64URLEncode(claims)
	h := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64URLEncode(sig), nil
}

func base64URLEncode(b []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}

// repoNamesFromGitRepos flattens a BrokerPolicy gitRepos list into
// the bare repo-name slice GitHub's /access_tokens repositories[]
// parameter wants. GitHub scopes tokens by installation, so the owner
// is implicit — we only send repo names. Owners that don't belong to
// the installation result in a 422 from GitHub, which surfaces as a
// provider error; the proxy drops the agent's connection in that case.
func repoNamesFromGitRepos(grants []paddockv1alpha1.GitRepoGrant) []string {
	if len(grants) == 0 {
		return nil
	}
	out := make([]string, 0, len(grants))
	for _, g := range grants {
		r := strings.TrimSpace(g.Repo)
		if r == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}
