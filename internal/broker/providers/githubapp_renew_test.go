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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// newTestGitHubApp builds a *GitHubAppProvider wired to apiBaseURL
// with a fake Kubernetes client that holds a valid RSA private key
// Secret. Mirrors the constructor pattern used in githubapp_test.go.
func newTestGitHubApp(t *testing.T, apiBaseURL string) *GitHubAppProvider {
	t.Helper()
	key := generateRSAKey(t)
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(githubSecret(key)).
		Build()
	return &GitHubAppProvider{
		Client:      c,
		HTTPClient:  &http.Client{},
		APIEndpoint: apiBaseURL,
	}
}

func TestGitHubApp_Renew_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		// First call (from Issue's fail-fast validation) returns any valid
		// token; the actual renewal response carries ghs_renewed_xyz.
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_renewed_xyz","expires_at":"2026-04-29T13:00:00Z"}`))
	}))
	defer srv.Close()

	g := newTestGitHubApp(t, srv.URL)
	g.HTTPClient = srv.Client()

	// Issue first to populate the in-memory lease; Renew looks up config
	// by LeaseID so it needs this state to reconstruct the install config.
	issued, err := g.Issue(context.Background(), IssueRequest{
		RunName:        "cc-1",
		Namespace:      "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
		GitRepos:       githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	out, err := g.Renew(context.Background(), paddockv1alpha1.IssuedLease{
		Provider:       "GitHubApp",
		LeaseID:        issued.LeaseID,
		CredentialName: "GITHUB_TOKEN",
		ExpiresAt:      &metav1.Time{Time: time.Now().Add(2 * time.Minute)},
	})
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if out == nil || out.Value != "ghs_renewed_xyz" {
		t.Fatalf("renewed value = %q, want ghs_renewed_xyz", out.Value)
	}
	if out.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = zero, want non-zero")
	}
	// Renew preserves identity — LeaseID must be unchanged.
	if out.LeaseID != issued.LeaseID {
		t.Fatalf("LeaseID = %q, want %q (renew preserves identity)", out.LeaseID, issued.LeaseID)
	}
}

func TestGitHubApp_Renew_GitHubError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	g := newTestGitHubApp(t, srv.URL)
	g.HTTPClient = srv.Client()

	// Issue to populate the in-memory lease. The test server returns 401
	// for all calls including Issue's fail-fast Secret validation — but
	// Issue does NOT call the GitHub API (it defers to SubstituteAuth),
	// so Issue itself succeeds here.
	issued, err := g.Issue(context.Background(), IssueRequest{
		RunName:        "cc-1",
		Namespace:      "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
		GitRepos:       githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = g.Renew(context.Background(), paddockv1alpha1.IssuedLease{
		Provider: "GitHubApp",
		LeaseID:  issued.LeaseID,
	})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401-related error", err)
	}
}

// TestGitHubApp_Renew_UpdatesLeaseExpiresAt is a regression test for the
// bug where Renew updated the token cache but left the in-memory
// lease.ExpiresAt at the original Issue-time value. A SubstituteAuth call
// made after the original expiry would incorrectly reject the bearer with
// "github bearer expired" even though Renew had successfully refreshed the
// token.
func TestGitHubApp_Renew_UpdatesLeaseExpiresAt(t *testing.T) {
	t.Parallel()

	// srv returns a fresh token whose expiry is 1h in the future relative
	// to the server's static response time. The first Issue call does not
	// hit this server (Issue defers GitHub API calls to SubstituteAuth);
	// Renew calls it once.
	renewedExpiry := time.Unix(1_700_010_000, 0) // well beyond the 10-min Issue window
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_renewed_token","expires_at":"` +
			renewedExpiry.UTC().Format(time.RFC3339) + `"}`))
	}))
	defer srv.Close()

	// Start the clock at t=0; Issue sets lease.ExpiresAt = t0 + defaultGitHubAppTokenTTL (1h).
	t0 := time.Unix(1_700_000_000, 0)
	issueExpiry := t0.Add(defaultGitHubAppTokenTTL)

	key := generateRSAKey(t)
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(githubSecret(key)).
		Build()
	clock := t0
	p := &GitHubAppProvider{
		Client:      c,
		HTTPClient:  srv.Client(),
		APIEndpoint: srv.URL,
		clockSource: clockSource{Now: func() time.Time { return clock }},
	}

	// Issue: lease.ExpiresAt = t0 + 10min.
	issued, err := p.Issue(context.Background(), IssueRequest{
		RunName:        "cc-renew-1",
		Namespace:      "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          githubGrant(),
		GitRepos:       githubRepos(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !issued.ExpiresAt.Equal(issueExpiry) {
		t.Fatalf("Issue ExpiresAt = %v, want %v", issued.ExpiresAt, issueExpiry)
	}

	// Renew at t0+55min (inside the original 1h window so the bearer is
	// still valid). Renew must (a) return the renewed ExpiresAt, AND (b)
	// update the in-memory lease.ExpiresAt so a later SubstituteAuth beyond
	// the original expiry succeeds.
	clock = t0.Add(55 * time.Minute)
	renewed, err := p.Renew(context.Background(), paddockv1alpha1.IssuedLease{
		Provider:       "GitHubApp",
		LeaseID:        issued.LeaseID,
		CredentialName: "GITHUB_TOKEN",
		ExpiresAt:      &metav1.Time{Time: issueExpiry},
	})
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if renewed.Value != "ghs_renewed_token" {
		t.Fatalf("Renew Value = %q, want ghs_renewed_token", renewed.Value)
	}
	if !renewed.ExpiresAt.Equal(renewedExpiry) {
		t.Fatalf("Renew ExpiresAt = %v, want %v", renewed.ExpiresAt, renewedExpiry)
	}

	// Advance clock past the original Issue-time expiry (t0+10min+1s).
	// Before fix #1, lease.ExpiresAt was still issueExpiry, so
	// SubstituteAuth would reject the bearer here with "github bearer expired".
	// After fix #1, lease.ExpiresAt == renewedExpiry, so this must succeed.
	clock = issueExpiry.Add(time.Second)

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName:        "cc-renew-1",
		Namespace:      "my-team",
		Host:           "github.com",
		Port:           443,
		IncomingBearer: "Bearer " + issued.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth after Renew: %v (want success; pre-fix this would fail with 'github bearer expired')", err)
	}
	if !sub.Matched {
		t.Fatalf("SubstituteAuth Matched = false, want true")
	}
}

// TestRenewableProviderOf_StaticProviderReturnsNil ensures non-renewable
// providers return nil from the helper.
func TestRenewableProviderOf_StaticProviderReturnsNil(t *testing.T) {
	t.Parallel()
	var static Provider = &AnthropicAPIProvider{}
	if got := RenewableProviderOf(static); got != nil {
		t.Fatalf("RenewableProviderOf(AnthropicAPIProvider) = %v, want nil", got)
	}
}

// TestRenewableProviderOf_GitHubAppReturnsProvider ensures GitHubApp
// satisfies RenewableProvider and is returned by the helper.
func TestRenewableProviderOf_GitHubAppReturnsProvider(t *testing.T) {
	t.Parallel()
	var p Provider = &GitHubAppProvider{}
	rp := RenewableProviderOf(p)
	if rp == nil {
		t.Fatal("RenewableProviderOf(GitHubAppProvider) = nil, want non-nil")
	}
	if _, ok := rp.(*GitHubAppProvider); !ok {
		t.Fatalf("RenewableProviderOf returned %T, want *GitHubAppProvider", rp)
	}
}
