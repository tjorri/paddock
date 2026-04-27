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

// Package proxy implements the per-run egress proxy sidecar for
// Paddock v0.3. In cooperative mode (M4) it is an HTTP/1.1 CONNECT
// proxy that intercepts TLS destinations, forges a leaf certificate
// signed by the run-scoped MITM CA, re-issues the client request
// upstream, and emits AuditEvents on denials. Transparent mode (M5)
// reuses the same MITM engine but fronts it with an iptables-init
// redirect and SO_ORIGINAL_DST lookup.
//
// See docs/specs/0002-broker-proxy-v0.3.md §7 and ADR-0013.
package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Cooperative-mode HTTP server timeout constants. F-26.
const (
	httpReadTimeout       = 60 * time.Second
	httpWriteTimeout      = 90 * time.Second
	httpIdleTimeout       = 60 * time.Second
	httpReadHeaderTimeout = 15 * time.Second
	httpMaxHeaderBytes    = 16 << 10
)

// NewHTTPServer constructs the cooperative-mode http.Server with the
// proxy's standard timeouts and limits. Caller wires it onto a
// LimitedListener via Serve(). F-26.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}

// Server is the HTTP CONNECT proxy. Zero value is not usable; populate
// CA and Validator at minimum.
type Server struct {
	// CA is the Paddock MITM CA. Every intercepted TLS connection is
	// re-signed with a leaf forged by this CA; the agent trusts it via
	// the projected ca-bundle Secret (ADR-0013 §7.3).
	CA *MITMCertificateAuthority

	// Validator decides allow/deny per (host, port). M4 shipped a
	// StaticValidator; M7 passes a BrokerClient that calls the broker's
	// ValidateEgress endpoint so the same BrokerPolicy store the
	// admission webhook consulted decides runtime flow too.
	Validator Validator

	// Substituter, when non-nil, rewrites outbound request headers when
	// the matched egress grant declared SubstituteAuth=true. The MITM
	// path drops to a request-by-request loop so headers can be swapped
	// mid-connection (required for the AnthropicAPI x-api-key swap —
	// ADR-0015 §"AnthropicAPIProvider"). nil falls back to
	// bytes-both-ways shuttle, same as cooperative M4 behaviour.
	Substituter Substituter

	// Audit receives every denial (and, later, summarised allows). nil
	// defaults to NoopAuditSink.
	Audit AuditSink

	// UpstreamDialer is used for the upstream TLS leg. nil defaults to
	// net.Dialer{}.DialContext. Tests swap it for an in-memory dialer
	// against an httptest server.
	UpstreamDialer func(ctx context.Context, network, addr string) (net.Conn, error)

	// UpstreamTLSConfig seeds the upstream tls.Config. The proxy fills
	// in ServerName per-connection; callers set RootCAs and TLS
	// versions. nil defaults to a zero tls.Config (system roots).
	UpstreamTLSConfig *tls.Config

	// HandshakeTimeout caps each inner TLS handshake (agent-side and
	// upstream-side). Defaults to 30s.
	HandshakeTimeout time.Duration

	// IdleTimeout caps the idle-read interval on the bytes-shuttle and
	// substitute-loop paths. When no data arrives within IdleTimeout the
	// proxy closes the connection so a revoked BrokerPolicy takes effect
	// within IdleTimeout on opaque tunnels too. Defaults to
	// defaultProxyIdleTimeout (60s). Zero is treated as "use default";
	// callers wanting to disable the timeout pass a deliberately-large
	// duration. F-25 part 2.
	IdleTimeout time.Duration

	// Logger, if set, receives per-connection diagnostic lines. nil
	// disables logging (tests typically pass logr.Discard()).
	Logger logr.Logger

	// OriginalDestination, if non-nil, replaces the SO_ORIGINAL_DST
	// syscall path in HandleTransparentConn. Tests use this to inject
	// pre-determined IP/port pairs against net.Pipe() conns that aren't
	// *net.TCPConn. Production callers leave it nil; the package-level
	// originalDestination from transparent_linux.go (or the no-op stub
	// in transparent_other.go) is used.
	OriginalDestination func(net.Conn) (net.IP, int, error)
}

// ServeHTTP dispatches CONNECT (MITM path) from plain HTTP requests
// (rejected — plain HTTP egress has no MITM lever, so we treat it as a
// policy question for a later milestone).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		// Plain-HTTP proxy would be a separate code path; v0.3's surface
		// is HTTPS-only. Respond loudly rather than open the hole.
		http.Error(w, "paddock-proxy: only HTTPS CONNECT traffic is supported (M4)", http.StatusMethodNotAllowed)
		return
	}
	s.handleConnect(w, r)
}

// handleConnect implements the MITM handshake:
//  1. Parse host:port from the CONNECT line.
//  2. Call the Validator to decide allow/deny.
//  3. On deny: 403 and record an AuditEvent. Connection closes.
//  4. On allow: 200 Connection established, forge a leaf for the host,
//     terminate TLS on the client side, dial the upstream with TLS,
//     proxy bytes both ways until either side closes.
//
// The MITM is transparent to well-behaved HTTP clients — they see a
// valid TLS handshake to the expected hostname and the same HTTP
// request/response bytes they would otherwise have exchanged. A
// compromised agent that pins a cert will fail the handshake; that is
// the intended security posture (§7 of the spec).
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := splitConnectTarget(r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	decision, vErr := s.Validator.ValidateEgress(ctx, host, port)
	if vErr != nil {
		// Validator errors (e.g. broker unreachable) fail closed by
		// design — the spec's §6.4 brokerFailureMode=Closed posture.
		s.log().Error(vErr, "validator error", "host", host, "port", port)
		if aErr := s.recordEgress(ctx, EgressEvent{
			Host: host, Port: port,
			Decision: paddockv1alpha1.AuditDecisionDenied,
			Reason:   fmt.Sprintf("validator error: %v", vErr),
		}); aErr != nil {
			s.log().Error(aErr, "audit write failed on deny path", "host", host, "port", port)
			http.Error(w, "paddock-proxy: audit unavailable", http.StatusBadGateway)
			return
		}
		http.Error(w, "paddock-proxy: broker unreachable", http.StatusBadGateway)
		return
	}
	if !decision.Allowed {
		s.log().V(1).Info("denied", "host", host, "port", port, "reason", decision.Reason)
		if aErr := s.recordEgress(ctx, EgressEvent{
			Host: host, Port: port,
			Decision: paddockv1alpha1.AuditDecisionDenied,
			Reason:   decision.Reason,
		}); aErr != nil {
			s.log().Error(aErr, "audit write failed on deny path", "host", host, "port", port)
			http.Error(w, "paddock-proxy: audit unavailable", http.StatusBadGateway)
			return
		}
		http.Error(w, fmt.Sprintf("paddock-proxy: %s", decision.Reason), http.StatusForbidden)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "paddock-proxy: hijack not supported", http.StatusInternalServerError)
		return
	}
	// Announce tunnel *before* hijacking: w.WriteHeader after Hijack
	// is a no-op and the client then sits in read. Order matters.
	w.WriteHeader(http.StatusOK)
	clientConn, buf, err := hijacker.Hijack()
	if err != nil {
		s.log().Error(err, "hijack")
		return
	}
	defer func() { _ = clientConn.Close() }()
	// Any bytes already buffered (e.g. a pipelined request) have to go
	// into the TLS handshake, not get dropped. In practice CONNECT
	// responses drain clean, but handle the case rather than crash.
	if buf != nil && buf.Reader.Buffered() > 0 {
		leftover := make([]byte, buf.Reader.Buffered())
		_, _ = buf.Read(leftover)
		// Prepend to the TLS-terminated path. We wrap clientConn in a
		// MultiReader-style connection so tls.Server reads the buffered
		// bytes first.
		clientConn = &prefixConn{Conn: clientConn, prefix: leftover}
	}

	s.mitm(ctx, clientConn, host, port, decision)
}

// mitm is the cooperative-mode MITM entry. The CONNECT 200 write and
// hijack already happened in handleConnect; mitm forges a leaf for
// host, terminates TLS, and delegates to doMITM. In cooperative mode
// the dial host (= upstream IP/hostname) and the SNI coincide.
func (s *Server) mitm(ctx context.Context, clientConn net.Conn, host string, port int, decision Decision) {
	if err := s.doMITM(ctx, clientConn, host, host, port, decision); err != nil {
		s.log().V(1).Info("cooperative MITM ended", "host", host, "err", err)
	}
}

func (s *Server) handshakeTimeout() time.Duration {
	if s.HandshakeTimeout > 0 {
		return s.HandshakeTimeout
	}
	return 30 * time.Second
}

func (s *Server) idleTimeout() time.Duration {
	if s.IdleTimeout > 0 {
		return s.IdleTimeout
	}
	return defaultProxyIdleTimeout
}

// origDest returns the original (pre-NAT) destination of conn. Honours
// the test-injected OriginalDestination field when set; otherwise calls
// the platform-specific originalDestination defined in
// transparent_linux.go / transparent_other.go.
func (s *Server) origDest(conn net.Conn) (net.IP, int, error) {
	if s.OriginalDestination != nil {
		return s.OriginalDestination(conn)
	}
	return originalDestination(conn)
}

// recordEgress emits one EgressEvent via the configured AuditSink.
// Returns the sink's error so the caller can fail-close on the deny
// path. nil on success and when no sink is configured.
func (s *Server) recordEgress(ctx context.Context, e EgressEvent) error {
	if s.Audit == nil {
		return nil
	}
	return s.Audit.RecordEgress(ctx, e)
}

func (s *Server) log() logr.Logger {
	if s.Logger.GetSink() == nil {
		return logr.Discard()
	}
	return s.Logger
}

// splitConnectTarget parses the host:port from a CONNECT request line.
// RFC 9110 §9.3.6 requires an authority-form request target, which
// net/http puts into r.Host for CONNECT. IPv6 literals show up as
// "[::1]:443" — net.SplitHostPort handles that.
func splitConnectTarget(target string) (string, int, error) {
	hostStr, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return "", 0, fmt.Errorf("CONNECT target %q: %w", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("CONNECT port %q: %w", portStr, err)
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("CONNECT port %d out of range", port)
	}
	return strings.ToLower(hostStr), port, nil
}

// prefixConn stitches a pre-buffered byte sequence in front of a
// net.Conn's read stream. Needed when http.Hijack hands us a reader
// with buffered bytes that we can't un-buffer back into the connection.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}
