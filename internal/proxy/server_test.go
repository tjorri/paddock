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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// recordingSink captures every emitted EgressEvent for assertion. Safe
// for concurrent use: the proxy writes from the CONNECT goroutine.
type recordingSink struct {
	mu     sync.Mutex
	events []EgressEvent
}

func (r *recordingSink) RecordEgress(_ context.Context, e EgressEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingSink) snapshot() []EgressEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EgressEvent, len(r.events))
	copy(out, r.events)
	return out
}

// generateTestCA returns an in-memory PEM-encoded CA keypair suitable
// for feeding NewMITMCertificateAuthority.
func generateTestCA(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-paddock-proxy-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("self-sign CA: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal CA key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// startUpstream returns an httptest TLS server that responds "ok" on
// GET /. We return (server, host, port, caBundle) — the proxy needs
// the CA bundle to verify the upstream leg.
func startUpstream(t *testing.T) (*httptest.Server, string, int, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	port, err := parsePort(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, host, port, pool
}

func parsePort(s string) (int, error) {
	var p int
	if _, err := fmt.Sscanf(s, "%d", &p); err != nil {
		return 0, err
	}
	return p, nil
}

// startProxy boots the Server on a random loopback port and returns
// its URL. t.Cleanup tears everything down. Tests plug in their
// Validator + AuditSink.
func startProxy(t *testing.T, srv *Server) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpSrv.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	})
	return "http://" + l.Addr().String()
}

func TestProxy_AllowsAndMITMsTrustedHost(t *testing.T) {
	upstream, host, port, upstreamPool := startUpstream(t)
	_ = upstream

	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}

	sink := &recordingSink{}
	// Allow only the upstream's host:port.
	validator, err := NewStaticValidatorFromEnv(fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		t.Fatalf("build validator: %v", err)
	}

	srv := &Server{
		CA:                ca,
		Validator:         validator,
		Audit:             sink,
		UpstreamTLSConfig: &tls.Config{RootCAs: upstreamPool, MinVersion: tls.VersionTLS12},
	}
	proxyURL := startProxy(t, srv)
	pu, _ := url.Parse(proxyURL)

	// Client trusts the MITM CA, not the upstream cert — that's what
	// cooperative-mode runs look like: agent only trusts Paddock's CA.
	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)

	tr := &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: clientPool, MinVersion: tls.VersionTLS12},
	}
	cli := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	resp, err := cli.Get(fmt.Sprintf("https://%s:%d/", host, port))
	if err != nil {
		t.Fatalf("proxy GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("response body = %q, want ok", body)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1 (one allow)", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionGranted {
		t.Errorf("decision = %q, want granted", evs[0].Decision)
	}
	if evs[0].MatchedPolicy != "static-allow" {
		t.Errorf("matchedPolicy = %q, want static-allow", evs[0].MatchedPolicy)
	}
}

func TestProxy_DeniesWhenHostNotInAllowList(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}
	// Allow-list that never matches "evil.com".
	validator, err := NewStaticValidatorFromEnv("api.anthropic.com:443")
	if err != nil {
		t.Fatalf("build validator: %v", err)
	}

	srv := &Server{CA: ca, Validator: validator, Audit: sink}
	proxyURL := startProxy(t, srv)
	pu, _ := url.Parse(proxyURL)

	tr := &http.Transport{Proxy: http.ProxyURL(pu)}
	cli := &http.Client{Transport: tr, Timeout: 3 * time.Second}

	// The 403 from the proxy closes the tunnel; net/http surfaces this
	// as a transport error, not a response.
	_, err = cli.Get("https://evil.com/")
	if err == nil {
		t.Fatalf("expected connection error; got nil")
	}

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1 (one deny)", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("decision = %q, want denied", evs[0].Decision)
	}
	if evs[0].Host != "evil.com" || evs[0].Port != 443 {
		t.Errorf("destination = %s:%d, want evil.com:443", evs[0].Host, evs[0].Port)
	}
}

func TestProxy_RejectsPlainHTTPRequest(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	// Non-CONNECT requests are rejected regardless of validator state.
	validator, _ := NewStaticValidatorFromEnv("*:*")
	srv := &Server{CA: ca, Validator: validator, Audit: &recordingSink{}}
	proxyURL := startProxy(t, srv)

	req, _ := http.NewRequest(http.MethodGet, proxyURL+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// TestStaticValidator_Matchers exercises the wildcard + port rules.
func TestStaticValidator_Matchers(t *testing.T) {
	v, err := NewStaticValidatorFromEnv("*.anthropic.com:443,github.com:*,api.openai.com:443")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		host    string
		port    int
		allowed bool
	}{
		{"api.anthropic.com", 443, true},
		{"console.anthropic.com", 443, true},
		{"anthropic.com", 443, false}, // apex not matched by *.
		{"api.anthropic.com", 80, false},
		{"github.com", 22, true},
		{"gist.github.com", 443, false},
		{"api.openai.com", 443, true},
		{"api.openai.com", 80, false},
		{"evil.com", 443, false},
	}
	for _, c := range cases {
		got, err := v.ValidateEgress(context.Background(), c.host, c.port)
		if err != nil {
			t.Errorf("%s:%d unexpected error: %v", c.host, c.port, err)
			continue
		}
		if got.Allowed != c.allowed {
			t.Errorf("%s:%d allowed = %v, want %v (reason=%q)", c.host, c.port, got.Allowed, c.allowed, got.Reason)
		}
	}
}

// TestStaticValidator_DenyAllOnEmpty keeps the deny-all default from
// drifting — the spec's §6.4 fail-closed posture requires it.
func TestStaticValidator_DenyAllOnEmpty(t *testing.T) {
	v, err := NewStaticValidatorFromEnv("")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := v.ValidateEgress(context.Background(), "api.anthropic.com", 443)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Allowed {
		t.Errorf("empty allow-list must deny-all; got Allowed=true")
	}
}

// errValidator simulates a broker-unreachable error path so we can
// verify the proxy emits a deny AuditEvent even when validation
// *errors* (as opposed to returning Allowed=false).
type errValidator struct{}

func (errValidator) ValidateEgress(_ context.Context, _ string, _ int) (Decision, error) {
	return Decision{}, errors.New("simulated broker outage")
}

func TestProxy_ValidatorErrorFailsClosed(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}
	srv := &Server{CA: ca, Validator: errValidator{}, Audit: sink}
	proxyURL := startProxy(t, srv)
	pu, _ := url.Parse(proxyURL)

	tr := &http.Transport{Proxy: http.ProxyURL(pu)}
	cli := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	_, err = cli.Get("https://api.anthropic.com/")
	if err == nil {
		t.Fatalf("expected error on validator failure; got nil")
	}

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1 (one deny)", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("decision = %q, want denied", evs[0].Decision)
	}
}
