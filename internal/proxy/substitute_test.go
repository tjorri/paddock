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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"paddock.dev/paddock/internal/broker/providers"
)

// substitutingValidator mirrors the shape the BrokerClient will have at
// runtime: allows, tags with matchedPolicy, and sets SubstituteAuth=true
// to trigger the proxy's substitute-auth MITM loop.
type substitutingValidator struct {
	host     string
	port     int
	policy   string
	matching bool
}

func (v *substitutingValidator) ValidateEgress(_ context.Context, host string, port int) (Decision, error) {
	if host == v.host && port == v.port {
		v.matching = true
		return Decision{
			Allowed:        true,
			MatchedPolicy:  v.policy,
			SubstituteAuth: true,
		}, nil
	}
	return Decision{Allowed: false, Reason: "not in allow-list"}, nil
}

// recordingSubstituter swaps the incoming Paddock bearer for a real
// upstream credential and records the headers it saw so the test can
// assert what the agent sent.
type recordingSubstituter struct {
	mu          sync.Mutex
	realKey     string
	seenHeaders http.Header
}

func (r *recordingSubstituter) SubstituteAuth(_ context.Context, _ string, _ int, headers http.Header) (providers.SubstituteResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seenHeaders = headers.Clone()
	return providers.SubstituteResult{
		SetHeaders:     map[string]string{"x-api-key": r.realKey},
		RemoveHeaders:  []string{"Authorization"},
		AllowedHeaders: []string{"Content-Type", "Content-Length", "User-Agent"},
	}, nil
}

// startUpstreamEcho returns an httptest TLS server that 401s when
// x-api-key != wantedRealKey, and otherwise echoes the x-api-key + body.
// Lets the test verify that the substituted headers reached upstream
// without the agent ever knowing the real key.
func startUpstreamEcho(t *testing.T, wantedRealKey string) (*httptest.Server, string, int, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != wantedRealKey {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "authorization leaked", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "echo:%s", string(body))
	}))
	t.Cleanup(srv.Close)

	u, _ := url.Parse(srv.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := parsePort(portStr)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, host, port, pool
}

func TestProxy_SubstituteAuthRewritesHeaders(t *testing.T) {
	const realKey = "sk-real-42"
	upstream, host, port, upstreamPool := startUpstreamEcho(t, realKey)
	_ = upstream

	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}

	sub := &recordingSubstituter{realKey: realKey}
	validator := &substitutingValidator{host: host, port: port, policy: "anthropic-policy"}

	srv := &Server{
		CA:                ca,
		Validator:         validator,
		Substituter:       sub,
		Audit:             &recordingSink{},
		UpstreamTLSConfig: &tls.Config{RootCAs: upstreamPool, MinVersion: tls.VersionTLS12},
	}
	proxyURL := startProxy(t, srv)
	pu, _ := url.Parse(proxyURL)

	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)

	tr := &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: clientPool, MinVersion: tls.VersionTLS12},
	}
	cli := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	// Agent sends a Paddock bearer — the proxy must swap it before the
	// upstream sees it. Body is POSTed to exercise non-empty request.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		fmt.Sprintf("https://%s:%d/v1/messages", host, port),
		stringReader("hello"))
	req.Header.Set("Authorization", "Bearer pdk-anthropic-deadbeef")
	req.Header.Set("Content-Type", "text/plain")

	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %q, want 200", resp.StatusCode, body)
	}
	if string(body) != "echo:hello" {
		t.Fatalf("body = %q, want echo:hello", body)
	}

	// Recorded headers: substituter saw the original Authorization
	// before it was removed, and the upstream saw only the swapped key.
	sub.mu.Lock()
	seen := sub.seenHeaders
	sub.mu.Unlock()
	if got := seen.Get("Authorization"); got != "Bearer pdk-anthropic-deadbeef" {
		t.Errorf("substituter saw Authorization = %q, want Bearer pdk-anthropic-deadbeef", got)
	}
	if !validator.matching {
		t.Errorf("validator was not called for %s:%d", host, port)
	}
}

func TestProxy_SubstituteAuthErrorDropsConnection(t *testing.T) {
	upstream, host, port, upstreamPool := startUpstreamEcho(t, "never-seen")
	_ = upstream
	certPEM, keyPEM := generateTestCA(t)
	ca, _ := NewMITMCertificateAuthority(certPEM, keyPEM)

	failSub := subFunc(func(_ context.Context, _ string, _ int, _ http.Header) (providers.SubstituteResult, error) {
		return providers.SubstituteResult{}, errors.New("simulated broker denial")
	})

	srv := &Server{
		CA:                ca,
		Validator:         &substitutingValidator{host: host, port: port, policy: "p"},
		Substituter:       failSub,
		Audit:             &recordingSink{},
		UpstreamTLSConfig: &tls.Config{RootCAs: upstreamPool, MinVersion: tls.VersionTLS12},
	}
	proxyURL := startProxy(t, srv)
	pu, _ := url.Parse(proxyURL)

	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)
	tr := &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: clientPool, MinVersion: tls.VersionTLS12},
	}
	cli := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	subErrReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, fmt.Sprintf("https://%s:%d/", host, port), nil)
	resp, err := cli.Do(subErrReq)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatalf("expected error — substituter failure must drop connection before upstream")
	}
}

// subFunc is a one-liner adapter used by tests that supply a
// one-shot Substituter implementation.
type subFunc func(context.Context, string, int, http.Header) (providers.SubstituteResult, error)

func (f subFunc) SubstituteAuth(ctx context.Context, host string, port int, headers http.Header) (providers.SubstituteResult, error) {
	return f(ctx, host, port, headers)
}

// stringReader avoids pulling strings into the test just for a body.
func stringReader(s string) io.ReadCloser {
	return io.NopCloser(&stringBody{s: s})
}

type stringBody struct {
	s string
	i int
}

func (b *stringBody) Read(p []byte) (int, error) {
	if b.i >= len(b.s) {
		return 0, io.EOF
	}
	n := copy(p, b.s[b.i:])
	b.i += n
	return n, nil
}

func TestApplySubstitution_QueryParam(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://api.example.com/v1/thing?access_token=pdk-usersecret-abc&other=keep", nil)
	res := providers.SubstituteResult{
		Matched:            true,
		SetQueryParam:      map[string]string{"access_token": "real-token"},
		AllowedQueryParams: []string{"other"},
	}
	applySubstitutionToRequest(req, res)
	q := req.URL.Query()
	if q.Get("access_token") != "real-token" {
		t.Fatalf("access_token: got %q, want real-token", q.Get("access_token"))
	}
	if q.Get("other") != "keep" {
		t.Fatalf("other: got %q, want keep", q.Get("other"))
	}
}

func TestApplySubstitution_BasicAuth(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://api.example.com/repo.git", nil)
	req.Header.Set("Authorization", "Bearer pdk-usersecret-abc")
	res := providers.SubstituteResult{
		Matched:      true,
		SetBasicAuth: &providers.BasicAuth{Username: "oauth2", Password: "real-pat"},
	}
	applySubstitutionToRequest(req, res)
	u, pw, ok := req.BasicAuth()
	if !ok {
		t.Fatal("expected BasicAuth to be set")
	}
	if u != "oauth2" || pw != "real-pat" {
		t.Fatalf("BasicAuth: got (%q,%q), want (oauth2,real-pat)", u, pw)
	}
}

func TestApplySubstitution_StripsNonAllowlistedHeaders(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), "POST",
		"https://api.example.com/v1/messages", stringReader("{}"))
	req.Header.Set("Authorization", "Bearer pdk-anthropic-test")
	req.Header.Set("Cookie", "session=stolen")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Anthropic-Account", "evil")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent/1")

	res := providers.SubstituteResult{
		Matched:        true,
		SetHeaders:     map[string]string{"x-api-key": "real"},
		RemoveHeaders:  []string{"Authorization"},
		AllowedHeaders: []string{"Content-Type", "Content-Length", "User-Agent"},
	}
	applySubstitutionToRequest(req, res)

	if got := req.Header.Get("Cookie"); got != "" {
		t.Errorf("Cookie should be stripped; got %q", got)
	}
	if got := req.Header.Get("X-Forwarded-For"); got != "" {
		t.Errorf("X-Forwarded-For should be stripped; got %q", got)
	}
	if got := req.Header.Get("X-Anthropic-Account"); got != "" {
		t.Errorf("X-Anthropic-Account should be stripped; got %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be removed; got %q", got)
	}
	if got := req.Header.Get("X-Api-Key"); got != "real" {
		t.Errorf("x-api-key should be set to %q; got %q", "real", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type should be preserved; got %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "agent/1" {
		t.Errorf("User-Agent should be preserved; got %q", got)
	}
}

func TestApplySubstitution_EmptyAllowlistFailsClosed(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://api.example.com/", nil)
	req.Header.Set("Authorization", "Bearer pdk-anthropic-test")
	req.Header.Set("Cookie", "session=stolen")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agent/1")

	// Provider returned no AllowedHeaders. The proxy must strip everything
	// except mustKeep + SetHeaders keys.
	res := providers.SubstituteResult{
		Matched:    true,
		SetHeaders: map[string]string{"x-api-key": "real"},
	}
	applySubstitutionToRequest(req, res)

	if got := req.Header.Get("Cookie"); got != "" {
		t.Errorf("Cookie should be stripped; got %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "" {
		t.Errorf("User-Agent should be stripped (not in mustKeep, not in SetHeaders); got %q", got)
	}
	// Content-Type is in mustKeep — preserved even with empty allowlist.
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type should be preserved (mustKeep); got %q", got)
	}
	// SetHeaders key is preserved.
	if got := req.Header.Get("X-Api-Key"); got != "real" {
		t.Errorf("x-api-key should be set; got %q", got)
	}
}

func TestApplySubstitution_StripsNonAllowlistedQueryParams(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://api.example.com/v1/thing?access_token=leaked&other=keep&allowed=yes", nil)
	res := providers.SubstituteResult{
		Matched:            true,
		AllowedQueryParams: []string{"allowed"},
	}
	applySubstitutionToRequest(req, res)
	q := req.URL.Query()
	if q.Get("access_token") != "" {
		t.Errorf("access_token should be stripped; got %q", q.Get("access_token"))
	}
	if q.Get("other") != "" {
		t.Errorf("other should be stripped; got %q", q.Get("other"))
	}
	if q.Get("allowed") != "yes" {
		t.Errorf("allowed should be preserved; got %q", q.Get("allowed"))
	}
}

func TestApplySubstitution_PreservesSetHeadersAndQueryParams(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://api.example.com/v1/x?key=stripped&api_key=replaced", nil)
	req.Header.Set("Authorization", "Bearer pdk-test")

	res := providers.SubstituteResult{
		Matched:       true,
		SetHeaders:    map[string]string{"X-Custom-Auth": "value"},
		SetQueryParam: map[string]string{"api_key": "real-key"},
		// Even with empty allowlists, SetHeaders/SetQueryParam keys are kept.
	}
	applySubstitutionToRequest(req, res)

	if got := req.Header.Get("X-Custom-Auth"); got != "value" {
		t.Errorf("X-Custom-Auth (a SetHeaders key) should be preserved; got %q", got)
	}
	q := req.URL.Query()
	if q.Get("api_key") != "real-key" {
		t.Errorf("api_key (a SetQueryParam key) should be replaced; got %q", q.Get("api_key"))
	}
	if q.Get("key") != "" {
		t.Errorf("key (not allowed, not in SetQueryParam) should be stripped; got %q", q.Get("key"))
	}
}
