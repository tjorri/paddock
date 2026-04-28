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
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// fixedOrigDest returns a Server.OriginalDestination function that
// always reports the supplied IP+port. The conn argument is ignored —
// the seam is precisely about decoupling transparent-mode from the
// SO_ORIGINAL_DST syscall.
func fixedOrigDest(ip net.IP, port int) func(net.Conn) (net.IP, int, error) {
	return func(_ net.Conn) (net.IP, int, error) {
		return ip, port, nil
	}
}

// driveTransparentClient runs a TLS client handshake on conn against
// SNI and issues a single GET / over the resulting connection. Returns
// the server's body and any error. The CA bundle clientCAs lets the
// agent trust the proxy's forged leaf.
func driveTransparentClient(t *testing.T, conn net.Conn, sni string, clientCAs *x509.CertPool) (string, error) {
	t.Helper()
	cfg := &tls.Config{
		ServerName: sni,
		RootCAs:    clientCAs,
		MinVersion: tls.VersionTLS12,
	}
	tlsConn := tls.Client(conn, cfg)
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		return "", fmt.Errorf("client handshake: %w", err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+sni+"/", nil)
	if err := req.Write(tlsConn); err != nil {
		return "", fmt.Errorf("write request: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tlsConn), req)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

func TestHandleTransparentConn_AllowsAndMITMs(t *testing.T) {
	upstream, host, port, upstreamPool := startUpstream(t)
	_ = upstream

	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}

	// Use a DNS-name SNI rather than the upstream's IP-literal host:
	// tls.Client omits SNI when ServerName is an IP literal (RFC 6066
	// §3), and a no-SNI ClientHello hits transparent mode's deny path.
	// httptest's stock cert carries SAN=example.com, so the upstream-leg
	// verification (which uses sni as ServerName) still passes.
	const sni = "example.com"
	validator, err := NewStaticValidatorFromEnv(fmt.Sprintf("%s:%d", sni, port))
	if err != nil {
		t.Fatalf("validator: %v", err)
	}

	// Resolve host to an IP for the test seam — startUpstream returns a
	// loopback host, so the resolver is fast.
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer resolveCancel()
	ips, err := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
	if err != nil || len(ips) == 0 {
		t.Fatalf("lookup %q: %v", host, err)
	}

	// Wire a stub resolver that returns the upstream IP for the test SNI so
	// the F-22 Layer 1 re-resolve check passes (origIP == resolved IP).
	resolvedIP := ips[0].IP
	stubLookup := func(_ context.Context, _ string) ([]net.IP, error) {
		return []net.IP{resolvedIP}, nil
	}

	srv := &Server{
		CA:                  ca,
		Validator:           validator,
		Audit:               sink,
		UpstreamTLSConfig:   &tls.Config{RootCAs: upstreamPool, MinVersion: tls.VersionTLS12},
		OriginalDestination: fixedOrigDest(resolvedIP, port),
		IdleTimeout:         150 * time.Millisecond,
		Resolver:            newCachingResolverWithLookup(stubLookup, time.Minute, 16),
	}

	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Drive the agent side in a goroutine so HandleTransparentConn (which
	// runs on this goroutine) can read the ClientHello, peek SNI, validate,
	// MITM, and respond.
	type clientResult struct {
		body string
		err  error
	}
	clientCh := make(chan clientResult, 1)
	go func() {
		body, err := driveTransparentClient(t, clientConn, sni, clientPool)
		// Close our pipe end so the proxy's deferred clientTLS.Close (in
		// mitm.go) doesn't block on the 5s hardcoded close_notify write
		// deadline that (*tls.Conn).Close uses (crypto/tls/conn.go). Under
		// net.Pipe's synchronous semantics with no reader on the other end,
		// that write blocks the full 5s; real TCP doesn't hit this because
		// the kernel socket buffer absorbs close_notify immediately.
		_ = clientConn.Close()
		clientCh <- clientResult{body, err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	select {
	case res := <-clientCh:
		if res.err != nil {
			t.Fatalf("client: %v", res.err)
		}
		if res.body != "ok" {
			t.Errorf("body = %q, want ok", res.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client goroutine did not return")
	}

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionGranted {
		t.Errorf("decision = %q, want granted", evs[0].Decision)
	}
	if evs[0].Host != sni {
		t.Errorf("host = %q, want %q", evs[0].Host, sni)
	}
	if evs[0].Kind != paddockv1alpha1.AuditKindEgressAllow {
		t.Errorf("kind = %q, want egress-allow", evs[0].Kind)
	}
}

func TestHandleTransparentConn_DeniesUnknownHost(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}

	// Allow-list does NOT include evil.example.com.
	validator, err := NewStaticValidatorFromEnv("api.anthropic.com:443")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}

	srv := &Server{
		CA:                  ca,
		Validator:           validator,
		Audit:               sink,
		OriginalDestination: fixedOrigDest(net.IPv4(192, 0, 2, 1), 443),
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// We only need the ClientHello to reach the server; the deny path
	// closes the conn before completing the handshake.
	go driveTLSClient(clientConn, "evil.example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("decision = %q, want denied", evs[0].Decision)
	}
	if evs[0].Host != "evil.example.com" {
		t.Errorf("host = %q, want evil.example.com", evs[0].Host)
	}
}

func TestHandleTransparentConn_NoSNI_Denies(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}
	validator, _ := NewStaticValidatorFromEnv("*:*")

	srv := &Server{
		CA:                  ca,
		Validator:           validator,
		Audit:               sink,
		OriginalDestination: fixedOrigDest(net.IPv4(198, 51, 100, 7), 443),
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Drive a TLS client without a ServerName — the resulting ClientHello
	// has no SNI, which the deny path classifies as "v0.3 blocks no-SNI
	// destinations".
	go driveTLSClient(clientConn, "")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("decision = %q, want denied", evs[0].Decision)
	}
	if !strings.Contains(evs[0].Reason, "SNI") {
		t.Errorf("reason = %q, want one containing 'SNI'", evs[0].Reason)
	}
	if evs[0].Host != "198.51.100.7" {
		t.Errorf("host = %q, want IP-literal 198.51.100.7 (no-SNI fallback)", evs[0].Host)
	}
}

// TestHandleTransparent_DeniedCIDR_OrigIP verifies F-22 layer 2 transparent:
// when SO_ORIGINAL_DST resolves to an IP in the denied-CIDR set, the
// connection is abruptly closed and a deny AuditEvent is emitted.
func TestHandleTransparent_DeniedCIDR_OrigIP(t *testing.T) {
	denied, _ := ParseDeniedCIDRs("10.0.0.0/8")
	sink := &recordingSink{}
	srv := &Server{
		CA:          newTestCA(t, 16),
		Validator:   allowAllValidator{},
		Audit:       sink,
		DeniedCIDRs: denied,
		Resolver:    NewCachingResolver(time.Minute, 16),
		OriginalDestination: func(_ net.Conn) (net.IP, int, error) {
			return net.ParseIP("10.0.0.50"), 443, nil
		},
	}
	a, b := net.Pipe()
	t.Cleanup(func() { _ = b.Close() })
	go srv.HandleTransparentConn(context.Background(), a)
	for i := 0; i < 50; i++ {
		if len(sink.snapshot()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := sink.snapshot()
	if len(got) == 0 {
		t.Fatal("no audit recorded")
	}
	if got[0].Decision != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("decision = %q, want denied", got[0].Decision)
	}
	if !strings.Contains(got[0].Reason, "denied-destination-cidr") {
		t.Errorf("reason = %q, want denied-destination-cidr", got[0].Reason)
	}
}

// TestHandleTransparent_DNSRebindingMismatch verifies F-22 layer 1 transparent:
// when the proxy re-resolves the SNI and the result doesn't include
// the agent's origIP, the connection is abruptly closed and a deny
// AuditEvent with reason "dns-rebinding-mismatch" is emitted.
func TestHandleTransparent_DNSRebindingMismatch(t *testing.T) {
	// Proxy resolves evil-rebound.com → 203.0.113.10, but the agent
	// connected to 8.8.8.8 (via SO_ORIGINAL_DST). Mismatch → deny.
	stubLk := func(_ context.Context, _ string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	}
	denied, _ := ParseDeniedCIDRs("169.254.0.0/16") // 8.8.8.8 not in denied set
	sink := &recordingSink{}
	srv := &Server{
		CA:          newTestCA(t, 16),
		Validator:   allowAllValidator{},
		Audit:       sink,
		DeniedCIDRs: denied,
		Resolver:    newCachingResolverWithLookup(stubLk, time.Minute, 16),
		OriginalDestination: func(_ net.Conn) (net.IP, int, error) {
			return net.ParseIP("8.8.8.8"), 443, nil
		},
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	go srv.HandleTransparentConn(context.Background(), serverConn)

	// Drive a TLS ClientHello with SNI=evil-rebound.com from the agent side.
	go func() {
		defer func() { _ = clientConn.Close() }()
		tlsClient := tls.Client(clientConn, &tls.Config{
			ServerName:         "evil-rebound.com",
			InsecureSkipVerify: true, //nolint:gosec // test-only; the proxy will reject at rebinding check
			MinVersion:         tls.VersionTLS12,
		})
		if err := tlsClient.HandshakeContext(context.Background()); err != nil {
			// Expected — the proxy rejects at the rebinding check; we just
			// need the ClientHello bytes to reach the proxy.
			_ = err
		}
	}()

	for i := 0; i < 100; i++ {
		if len(sink.snapshot()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := sink.snapshot()
	if len(got) == 0 {
		t.Fatal("no audit recorded")
	}
	if !strings.Contains(got[0].Reason, "dns-rebinding-mismatch") {
		t.Errorf("reason = %q, want dns-rebinding-mismatch; got events %+v", got[0].Reason, got)
	}
}

func TestHandleTransparentConn_OrigDestFailure_DropsConnSilently(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}

	srv := &Server{
		CA:        ca,
		Validator: denyAllValidator{},
		Audit:     sink,
		OriginalDestination: func(_ net.Conn) (net.IP, int, error) {
			return nil, 0, fmt.Errorf("simulated SO_ORIGINAL_DST failure")
		},
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	evs := sink.snapshot()
	if len(evs) != 0 {
		t.Errorf("audit events = %d, want 0 (no-orig-dest path emits no audit)", len(evs))
	}
}

func TestHandleTransparentConn_EmitsDiscoveryAllowKind(t *testing.T) {
	upstream, host, port, upstreamPool := startUpstream(t)
	_ = upstream

	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}

	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer resolveCancel()
	ips, err := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
	if err != nil || len(ips) == 0 {
		t.Fatalf("lookup %q: %v", host, err)
	}

	// Wire a stub resolver that returns the upstream IP for the test SNI so
	// the F-22 Layer 1 re-resolve check passes (origIP == resolved IP).
	resolvedIP := ips[0].IP
	stubLookup := func(_ context.Context, _ string) ([]net.IP, error) {
		return []net.IP{resolvedIP}, nil
	}

	srv := &Server{
		CA:                  ca,
		Validator:           discoveryValidator{},
		Audit:               sink,
		UpstreamTLSConfig:   &tls.Config{RootCAs: upstreamPool, MinVersion: tls.VersionTLS12},
		OriginalDestination: fixedOrigDest(resolvedIP, port),
		IdleTimeout:         150 * time.Millisecond,
		Resolver:            newCachingResolverWithLookup(stubLookup, time.Minute, 16),
	}

	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// example.com SAN ships in the httptest cert; using a DNS-name SNI
	// (rather than the IP-literal host) keeps SNI in the ClientHello so
	// transparent mode's allow path actually runs. See AllowsAndMITMs
	// for the full rationale.
	const sni = "example.com"
	clientCh := make(chan error, 1)
	go func() {
		_, err := driveTransparentClient(t, clientConn, sni, clientPool)
		// Close our pipe end so the proxy's deferred clientTLS.Close (in
		// mitm.go) doesn't block on the 5s hardcoded close_notify write
		// deadline that (*tls.Conn).Close uses (crypto/tls/conn.go). Under
		// net.Pipe's synchronous semantics with no reader on the other end,
		// that write blocks the full 5s; real TCP doesn't hit this because
		// the kernel socket buffer absorbs close_notify immediately.
		_ = clientConn.Close()
		clientCh <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	select {
	case <-clientCh:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not return")
	}

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1", len(evs))
	}
	if evs[0].Kind != paddockv1alpha1.AuditKindEgressDiscoveryAllow {
		t.Errorf("kind = %q, want egress-discovery-allow", evs[0].Kind)
	}
}
