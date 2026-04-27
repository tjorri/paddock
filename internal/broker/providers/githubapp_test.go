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
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// generateRSAKey returns a PEM-encoded RSA private key suitable for
// feeding GitHubAppProvider. 2048 bits is the modern GitHub App
// minimum; we generate fresh per test.
func generateRSAKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// fakeGitHub returns an httptest.Server that plays the role of
// GitHub's /app/installations/{id}/access_tokens endpoint. Counts
// calls + captures the last repositories payload so tests can assert
// both caching behaviour and scope propagation.
type fakeGitHub struct {
	server  *httptest.Server
	calls   atomic.Int32
	lastReq struct {
		repositories []string
		auth         string
	}
	token     string
	expiresAt time.Time
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	fg := &fakeGitHub{
		token:     "ghs_real_42",
		expiresAt: time.Unix(1_700_003_600, 0),
	}
	fg.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fg.calls.Add(1)
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		if !strings.Contains(r.URL.Path, "/app/installations/") ||
			!strings.HasSuffix(r.URL.Path, "/access_tokens") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		authz := r.Header.Get("Authorization")
		fg.lastReq.auth = authz
		if !strings.HasPrefix(authz, "Bearer ") {
			http.Error(w, "missing app JWT", http.StatusUnauthorized)
			return
		}
		if r.ContentLength > 0 {
			var body struct {
				Repositories []string `json:"repositories"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			fg.lastReq.repositories = body.Repositories
		} else {
			fg.lastReq.repositories = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      fg.token,
			"expires_at": fg.expiresAt.UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(fg.server.Close)
	return fg
}

func githubGrant() paddockv1alpha1.CredentialGrant {
	return paddockv1alpha1.CredentialGrant{
		Name: "GITHUB_TOKEN",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:           "GitHubApp",
			AppID:          "123456",
			InstallationID: "78901234",
			SecretRef: &paddockv1alpha1.SecretKeyReference{
				Name: "paddock-github-app", Key: "private-key",
			},
		},
	}
}

func githubSecret(keyPEM []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "paddock-github-app", Namespace: "my-team"},
		Data:       map[string][]byte{"private-key": keyPEM},
	}
}

func githubRepos() []paddockv1alpha1.GitRepoGrant {
	return []paddockv1alpha1.GitRepoGrant{
		{Owner: "my-org", Repo: "backend-service", Access: paddockv1alpha1.GitRepoAccessWrite},
		{Owner: "my-org", Repo: "shared-libs", Access: paddockv1alpha1.GitRepoAccessRead},
	}
}

func TestGitHubAppProvider_IssueThenSubstitute(t *testing.T) {
	t.Parallel()
	fg := newFakeGitHub(t)
	clock := time.Unix(1_700_000_000, 0)
	fg.expiresAt = clock.Add(time.Hour)
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	p := &GitHubAppProvider{
		Client:      c,
		HTTPClient:  fg.server.Client(),
		APIEndpoint: fg.server.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName:        "cc-1",
		Namespace:      "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
		GitRepos:       githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(res.Value, "pdk-github-") {
		t.Fatalf("Value = %q, want pdk-github- prefix", res.Value)
	}
	// No GitHub API call should have fired yet — Issue records the
	// lease, SubstituteAuth mints the token.
	if got := fg.calls.Load(); got != 0 {
		t.Fatalf("unexpected early GitHub API call count = %d, want 0", got)
	}

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "cc-1", Namespace: "my-team",
		Host: "github.com", Port: 443,
		IncomingBearer: "Basic " + base64.StdEncoding.EncodeToString(
			[]byte("x-access-token:"+res.Value),
		),
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if !sub.Matched {
		t.Fatalf("Matched = false, want true")
	}
	// Verify the substituted Authorization carries the real token.
	authz := sub.SetHeaders["Authorization"]
	if !strings.HasPrefix(authz, "Basic ") {
		t.Fatalf("Authorization = %q, want Basic prefix", authz)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authz, "Basic "))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if got := string(raw); got != "x-access-token:ghs_real_42" {
		t.Fatalf("decoded auth = %q, want x-access-token:ghs_real_42", got)
	}

	// GitHub was called exactly once; repositories matched the grant.
	if got := fg.calls.Load(); got != 1 {
		t.Fatalf("GitHub API calls = %d, want 1", got)
	}
	if got := fg.lastReq.repositories; !sliceEqual(got, []string{"backend-service", "shared-libs"}) {
		t.Fatalf("repositories[] = %v, want [backend-service shared-libs]", got)
	}
}

func TestGitHubAppProvider_TokenSharedAcrossBearers(t *testing.T) {
	t.Parallel()
	// The run-sharing invariant: two bearers issued for the same
	// (run, credential) resolve to the *same* GitHub installation
	// token. Models the seed Job + agent Pod both asking for the
	// token within one run's lifetime.
	fg := newFakeGitHub(t)
	clock := time.Unix(1_700_000_000, 0)
	fg.expiresAt = clock.Add(time.Hour)
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	p := &GitHubAppProvider{
		Client: c, HTTPClient: fg.server.Client(), APIEndpoint: fg.server.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}

	first, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
		GitRepos:       githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue 1: %v", err)
	}
	second, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
		GitRepos:       githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue 2: %v", err)
	}
	if first.Value == second.Value {
		t.Fatalf("expected distinct bearers for parallel Issue calls")
	}

	for _, bearer := range []string{first.Value, second.Value} {
		if _, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
			RunName: "cc-1", Namespace: "my-team",
			Host: "github.com", Port: 443,
			IncomingBearer: "Bearer " + bearer,
		}); err != nil {
			t.Fatalf("SubstituteAuth %s: %v", bearer, err)
		}
	}
	if got := fg.calls.Load(); got != 1 {
		t.Fatalf("GitHub API calls = %d, want 1 (one token shared across bearers)", got)
	}
}

func TestGitHubAppProvider_TokenDistinctAcrossRuns(t *testing.T) {
	t.Parallel()
	// Different runs must not share tokens — one rogue run cannot burn
	// another run's rate-limit slice.
	fg := newFakeGitHub(t)
	clock := time.Unix(1_700_000_000, 0)
	fg.expiresAt = clock.Add(time.Hour)
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	p := &GitHubAppProvider{
		Client: c, HTTPClient: fg.server.Client(), APIEndpoint: fg.server.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}

	for _, run := range []string{"cc-1", "cc-2"} {
		res, err := p.Issue(context.Background(), IssueRequest{
			RunName: run, Namespace: "my-team",
			CredentialName: "GITHUB_TOKEN",
			Grant:          githubGrant(),
			GitRepos:       githubRepos(),
		})
		if err != nil {
			t.Fatalf("Issue %s: %v", run, err)
		}
		if _, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
			RunName: run, Namespace: "my-team",
			Host: "github.com", Port: 443,
			IncomingBearer: "Bearer " + res.Value,
		}); err != nil {
			t.Fatalf("SubstituteAuth %s: %v", run, err)
		}
	}
	if got := fg.calls.Load(); got != 2 {
		t.Fatalf("GitHub API calls = %d, want 2 (one per run)", got)
	}
}

func TestGitHubAppProvider_UnknownPrefixFallsThrough(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &GitHubAppProvider{Client: c}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		IncomingBearer: "Bearer sk-something-foreign",
	})
	if err != nil {
		t.Fatalf("expected no error on unmatched prefix, got %v", err)
	}
	if sub.Matched {
		t.Fatalf("Matched = true for foreign bearer; want false")
	}
}

func TestGitHubAppProvider_UnknownBearerClaimsMatch(t *testing.T) {
	t.Parallel()
	// Prefix matches ours but bearer not in the lease store. Provider
	// must claim Matched=true with an error so the broker short-circuits.
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &GitHubAppProvider{Client: c}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		IncomingBearer: "pdk-github-deadbeef",
	})
	if !sub.Matched {
		t.Fatalf("Matched = false; want true")
	}
	if err == nil {
		t.Fatalf("expected error for unknown bearer")
	}
}

func TestGitHubAppProvider_ExpiredBearer(t *testing.T) {
	t.Parallel()
	fg := newFakeGitHub(t)
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	clock := time.Unix(1_700_000_000, 0)
	p := &GitHubAppProvider{
		Client:      c,
		HTTPClient:  fg.server.Client(),
		APIEndpoint: fg.server.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}
	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: githubGrant(), GitRepos: githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clock = clock.Add(2 * time.Hour)
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "my-team", IncomingBearer: res.Value,
	})
	if !sub.Matched {
		t.Fatalf("Matched = false, want true for expired-but-owned bearer")
	}
	if err == nil {
		t.Fatalf("expected expiry error")
	}
}

func TestGitHubAppProvider_IssueMissingConfig(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &GitHubAppProvider{Client: c}
	cases := []struct {
		name string
		g    paddockv1alpha1.CredentialGrant
	}{
		{
			name: "missing secretRef",
			g: paddockv1alpha1.CredentialGrant{
				Name: "X",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind: "GitHubApp", AppID: "1", InstallationID: "2",
				},
			},
		},
		{
			name: "missing appId",
			g: paddockv1alpha1.CredentialGrant{
				Name: "X",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind: "GitHubApp", InstallationID: "2",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				},
			},
		},
		{
			name: "missing installationId",
			g: paddockv1alpha1.CredentialGrant{
				Name: "X",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind: "GitHubApp", AppID: "1",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := p.Issue(context.Background(), IssueRequest{
				RunName: "r", Namespace: "my-team",
				CredentialName: "X", Grant: c.g,
			})
			if err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestGitHubAppProvider_IssueBadPEM(t *testing.T) {
	t.Parallel()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "paddock-github-app", Namespace: "my-team"},
		Data:       map[string][]byte{"private-key": []byte("not a valid pem")},
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(secret).Build()
	p := &GitHubAppProvider{Client: c}
	_, err := p.Issue(context.Background(), IssueRequest{
		RunName: "r", Namespace: "my-team", CredentialName: "X", Grant: githubGrant(),
	})
	if err == nil {
		t.Fatalf("expected error on unparseable PEM")
	}
}

func TestGitHubAppProvider_GitHubServerError(t *testing.T) {
	t.Parallel()
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"no repository found"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	p := &GitHubAppProvider{Client: c, HTTPClient: srv.Client(), APIEndpoint: srv.URL}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "r", Namespace: "my-team", CredentialName: "X",
		Grant: githubGrant(), GitRepos: githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// The error arrives at substitute-auth time — the bearer path is
	// where the GitHub API call happens.
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "my-team", Host: "github.com", IncomingBearer: res.Value,
	})
	if !sub.Matched {
		t.Fatalf("Matched = false, want true (the bearer is ours)")
	}
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("expected 422 propagation, got %v", err)
	}
}

func TestExtractBearer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"Bearer pdk-anthropic-abc", "pdk-anthropic-abc"},
		{"bearer pdk-github-xyz", "pdk-github-xyz"},
		{"  Bearer sk-42  ", "sk-42"},
		{"Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:pdk-github-abc")), "pdk-github-abc"},
		{"Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass:word")), "pass:word"}, // preserves colons in password
		{"pdk-anthropic-bare", "pdk-anthropic-bare"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ExtractBearer(c.in); got != c.want {
			t.Errorf("ExtractBearer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// sliceEqual is a tiny helper so tests avoid pulling reflect just for
// order-sensitive string-slice equality.
func sliceEqual(a, b []string) bool {
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

// TestSignAppJWT_RoundTrip verifies the JWT the provider mints is
// signed by the correct key and carries the expected claims. No
// external JWT library in the test so the assertion stays narrow.
func TestSignAppJWT_RoundTrip(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	jwt, err := signAppJWT("123456", key, now)
	if err != nil {
		t.Fatalf("signAppJWT: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT must have 3 dot-separated parts, got %d", len(parts))
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims["iss"] != "123456" {
		t.Fatalf("iss = %v, want 123456", claims["iss"])
	}
	// iat is slightly in the past (30s buffer); exp is ~9min ahead.
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if iat >= float64(now.Unix()) {
		t.Fatalf("iat = %v, want < %d", iat, now.Unix())
	}
	if exp <= float64(now.Unix()) {
		t.Fatalf("exp = %v, want > %d", exp, now.Unix())
	}
	if exp-iat < 60 {
		t.Fatalf("exp-iat = %v, want > 60s", exp-iat)
	}
}

// TestParsePrivateKey accepts both shapes GitHub ships.
func TestParsePrivateKey(t *testing.T) {
	t.Parallel()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(k),
	})
	pkcs8der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatalf("MarshalPKCS8: %v", err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8der})

	for name, pemBytes := range map[string][]byte{
		"pkcs1": pkcs1,
		"pkcs8": pkcs8,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parsed, err := parsePrivateKey(pemBytes)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if parsed.N.Cmp(k.N) != 0 {
				t.Fatalf("parsed public key modulus mismatch")
			}
		})
	}
	t.Run("bad-type", func(t *testing.T) {
		t.Parallel()
		bad := pem.EncodeToMemory(&pem.Block{Type: "DSA PRIVATE KEY", Bytes: []byte{1, 2, 3}})
		if _, err := parsePrivateKey(bad); err == nil {
			t.Fatalf("expected error for non-RSA PEM")
		}
	})
	t.Run("not-pem", func(t *testing.T) {
		t.Parallel()
		if _, err := parsePrivateKey([]byte("nope")); err == nil {
			t.Fatalf("expected error for non-PEM input")
		}
	})
}

func TestGitHubAppProvider_SubstituteHostNotAllowed_Default(t *testing.T) {
	t.Parallel()
	fg := newFakeGitHub(t)
	clock := time.Unix(1_700_000_000, 0)
	fg.expiresAt = clock.Add(time.Hour)
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	p := &GitHubAppProvider{
		Client:      c,
		HTTPClient:  fg.server.Client(),
		APIEndpoint: fg.server.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "demo", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Default hosts is [github.com, api.github.com]; calling for evil.com must fail-closed.
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "demo", Namespace: "my-team",
		Host: "evil.com", Port: 443,
		IncomingBearer: res.Value,
	})
	if !sub.Matched {
		t.Fatalf("Matched = false; want true so broker short-circuits")
	}
	if err == nil {
		t.Fatalf("expected HostNotAllowed error for evil.com")
	}
	if !strings.Contains(err.Error(), "evil.com") {
		t.Errorf("error must name the offending host; got %q", err)
	}
}

func TestGitHubAppProvider_SubstituteHostAllowed_Defaults(t *testing.T) {
	t.Parallel()
	fg := newFakeGitHub(t)
	clock := time.Unix(1_700_000_000, 0)
	fg.expiresAt = clock.Add(time.Hour)
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	p := &GitHubAppProvider{
		Client:      c,
		HTTPClient:  fg.server.Client(),
		APIEndpoint: fg.server.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}
	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "demo", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Both default hosts must be accepted.
	for _, h := range []string{"github.com", "api.github.com"} {
		sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
			RunName: "demo", Namespace: "my-team",
			Host: h, Port: 443,
			IncomingBearer: res.Value,
		})
		if err != nil {
			t.Errorf("SubstituteAuth(%s): %v", h, err)
			continue
		}
		if !sub.Matched {
			t.Errorf("SubstituteAuth(%s): Matched=false", h)
		}
	}
}

func TestGitHubAppProvider_SubstituteResultFieldsPopulated(t *testing.T) {
	t.Parallel()
	fg := newFakeGitHub(t)
	clock := time.Unix(1_700_000_000, 0)
	fg.expiresAt = clock.Add(time.Hour)
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	p := &GitHubAppProvider{
		Client:      c,
		HTTPClient:  fg.server.Client(),
		APIEndpoint: fg.server.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}
	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "demo", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "demo", Namespace: "my-team",
		Host: "api.github.com", Port: 443,
		IncomingBearer: res.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if sub.CredentialName != "GITHUB_TOKEN" {
		t.Errorf("CredentialName = %q, want GITHUB_TOKEN", sub.CredentialName)
	}
	wantHdrs := []string{"Content-Type", "Content-Length", "Accept", "Accept-Encoding", "User-Agent", "X-GitHub-Api-Version"}
	if len(sub.AllowedHeaders) != len(wantHdrs) {
		t.Fatalf("AllowedHeaders = %v, want %v", sub.AllowedHeaders, wantHdrs)
	}
	for i, h := range wantHdrs {
		if sub.AllowedHeaders[i] != h {
			t.Errorf("AllowedHeaders[%d] = %q, want %q", i, sub.AllowedHeaders[i], h)
		}
	}
}

func TestGitHubAppProvider_Revoke_DropsLease(t *testing.T) {
	t.Parallel()
	fg := newFakeGitHub(t)
	clock := time.Unix(1_700_000_000, 0)
	fg.expiresAt = clock.Add(time.Hour)
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(githubSecret(key)).Build()
	p := &GitHubAppProvider{
		Client:      c,
		HTTPClient:  fg.server.Client(),
		APIEndpoint: fg.server.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName:        "cc-1",
		Namespace:      "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
		GitRepos:       githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if res.LeaseID == "" {
		t.Fatal("Issue returned empty LeaseID")
	}

	// Lease must be present in the map before Revoke.
	p.mu.Lock()
	leaseCount := len(p.bearers)
	p.mu.Unlock()
	if leaseCount != 1 {
		t.Fatalf("expected 1 lease after Issue, got %d", leaseCount)
	}

	if err := p.Revoke(context.Background(), res.LeaseID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Lease must be gone after Revoke.
	p.mu.Lock()
	leaseCount = len(p.bearers)
	p.mu.Unlock()
	if leaseCount != 0 {
		t.Fatalf("expected 0 leases after Revoke, got %d", leaseCount)
	}
}

func TestGitHubAppProvider_Revoke_UnknownLeaseID_NoError(t *testing.T) {
	t.Parallel()
	p := &GitHubAppProvider{}
	if err := p.Revoke(context.Background(), "gha-deadbeef"); err != nil {
		t.Fatalf("Revoke with unknown LeaseID returned error: %v", err)
	}
}

// TestRepoNamesFromGitRepos — double-check we flatten the grant into
// just repo names (GitHub scopes by installation, so owners are
// implicit).
func TestRepoNamesFromGitRepos(t *testing.T) {
	t.Parallel()
	got := repoNamesFromGitRepos([]paddockv1alpha1.GitRepoGrant{
		{Owner: "x", Repo: "a"},
		{Owner: "y", Repo: " b "},
		{Owner: "z", Repo: ""},
	})
	want := []string{"a", "b"}
	if !sliceEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
