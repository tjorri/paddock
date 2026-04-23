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

func (r *recordingSubstituter) SubstituteAuth(_ context.Context, _ string, _ int, headers http.Header) (SubstitutionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seenHeaders = headers.Clone()
	return SubstitutionResult{
		SetHeaders:    map[string]string{"x-api-key": r.realKey},
		RemoveHeaders: []string{"Authorization"},
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
	req, _ := http.NewRequest(http.MethodPost,
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

	failSub := subFunc(func(_ context.Context, _ string, _ int, _ http.Header) (SubstitutionResult, error) {
		return SubstitutionResult{}, errors.New("simulated broker denial")
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
	_, err := cli.Get(fmt.Sprintf("https://%s:%d/", host, port))
	if err == nil {
		t.Fatalf("expected error — substituter failure must drop connection before upstream")
	}
}

// subFunc is a one-liner adapter used by tests that supply a
// one-shot Substituter implementation.
type subFunc func(context.Context, string, int, http.Header) (SubstitutionResult, error)

func (f subFunc) SubstituteAuth(ctx context.Context, host string, port int, headers http.Header) (SubstitutionResult, error) {
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
