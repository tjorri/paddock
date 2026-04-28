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

// This file hosts the proxy's shared MITM internals — the parts that
// cooperative (server.go::handleConnect → mitm) and transparent
// (mode.go::HandleTransparentConn → mitmTransparent) modes both need.
// Cooperative and transparent differ only in how they obtain the
// (sni, dialHost, port, decision) tuple; once that tuple is known, the
// MITM dance is identical.

package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// dialUpstreamTLS opens a TLS connection to dialIP:port, verifying the
// peer certificate against serverName. Owns the dialer fallback,
// TLS-config clone with ServerName injection, and the
// HandshakeContext-with-timeout shared by both upstream legs.
//
// Called by doMITM with:
//   - cooperative mode: dialIP = the first allowed post-resolution IP
//     chosen by handleConnect; serverName = host (SNI).
//   - transparent mode: dialIP = SO_ORIGINAL_DST origIP; serverName = sni.
//     The cert is verified against the agent-requested SNI so the agent's
//     intent (connect to hostname X) is preserved.
func (s *Server) dialUpstreamTLS(ctx context.Context, dialIP net.IP, port int, serverName string) (net.Conn, error) {
	dialer := s.UpstreamDialer
	if dialer == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		dialer = d.DialContext
	}
	addr := net.JoinHostPort(dialIP.String(), strconv.Itoa(port))
	raw, err := dialer(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	cfg := s.UpstreamTLSConfig.Clone()
	if cfg == nil {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg.ServerName = serverName
	tlsConn := tls.Client(raw, cfg)
	hsCtx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
	defer cancel()
	if err := tlsConn.HandshakeContext(hsCtx); err != nil {
		_ = raw.Close()
		HandshakeFailures.Inc()
		ConnectionsRejected.WithLabelValues("handshake_failed").Inc()
		return nil, fmt.Errorf("upstream TLS handshake: %w", err)
	}
	return tlsConn, nil
}

// doMITM is the shared MITM core called by both cooperative-mode
// (server.go::mitm) and transparent-mode (mode.go::mitmTransparent)
// entry points. It owns:
//
//  1. Forging a leaf cert for sni.
//  2. Terminating TLS on the agent side with HandshakeContext(timeout).
//  3. Dialing the upstream via dialUpstreamTLS (cooperative passes
//     dialHost == sni; transparent passes dialHost = original-DST IP).
//  4. Emitting the allow-path AuditEvent (with Kind=egress-discovery-allow
//     when decision.DiscoveryAllow is set) — log+counter on failure;
//     allow-path audit is fail-open by F-24 design.
//  5. Either entering the substitute-auth request loop (when
//     decision.SubstituteAuth && Substituter != nil) or running the
//     bytes-shuttle with idle deadlines.
//
// Returns an error only for diagnostic logging; the caller does not
// fail the connection on a non-nil result. The returned error is
// suitable to log at V(1) — bytes-shuttle EOFs and similar are normal.
func (s *Server) doMITM(
	ctx context.Context,
	clientConn net.Conn,
	sni string,
	dialIP net.IP,
	port int,
	decision Decision,
) error {
	leaf, err := s.CA.ForgeFor(sni)
	if err != nil {
		s.log().Error(err, "forge leaf", "host", sni)
		return fmt.Errorf("forge leaf: %w", err)
	}
	clientTLS := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*leaf},
		MinVersion:   tls.VersionTLS12,
	})
	hsCtx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
	defer cancel()
	if err := clientTLS.HandshakeContext(hsCtx); err != nil {
		HandshakeFailures.Inc()
		ConnectionsRejected.WithLabelValues("handshake_failed").Inc()
		s.log().V(1).Info("client TLS handshake failed", "host", sni, "err", err)
		return fmt.Errorf("client TLS handshake: %w", err)
	}
	defer func() { _ = clientTLS.Close() }()

	upstreamConn, err := s.dialUpstreamTLS(ctx, dialIP, port, sni)
	if err != nil {
		s.log().V(1).Info("upstream dial failed", "host", sni, "err", err)
		return fmt.Errorf("upstream dial: %w", err)
	}
	defer func() { _ = upstreamConn.Close() }()

	kind := paddockv1alpha1.AuditKindEgressAllow
	if decision.DiscoveryAllow {
		kind = paddockv1alpha1.AuditKindEgressDiscoveryAllow
	}
	if aErr := s.recordEgress(ctx, EgressEvent{
		Host: sni, Port: port,
		Decision:      paddockv1alpha1.AuditDecisionGranted,
		MatchedPolicy: decision.MatchedPolicy,
		Kind:          kind,
		Reason:        decision.Reason,
	}); aErr != nil {
		// Allow path proceeds despite audit failure — F-24 fail-open.
		// The connection's security posture is already enforced; failing
		// legit traffic on a transient audit hiccup is worse than a
		// missing record. paddock_audit_write_failures_total catches it.
		s.log().Error(aErr, "audit write failed on allow path", "host", sni, "port", port)
	}

	if decision.SubstituteAuth && s.Substituter != nil {
		if err := handleSubstituted(ctx, clientTLS, upstreamConn, sni, port, s.Substituter, s.idleTimeout()); err != nil {
			return fmt.Errorf("substitute-auth MITM: %w", err)
		}
		return nil
	}

	// Full-duplex copy with idle deadline on each direction. Exit as soon
	// as either direction closes. F-25: a tunnel that goes idle for
	// s.idleTimeout() is torn down so a revoked BrokerPolicy takes
	// effect within that window even on opaque (no-decrypt) flows.
	clientReader := &deadlineExtendingReader{conn: clientTLS, timeout: s.idleTimeout()}
	upstreamReader := &deadlineExtendingReader{conn: upstreamConn, timeout: s.idleTimeout()}
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(upstreamConn, clientReader); errCh <- err }()
	go func() { _, err := io.Copy(clientTLS, upstreamReader); errCh <- err }()
	// Single receive is intentional: when either io.Copy returns the
	// deferred close-pair fires, the second goroutine's Read errors out
	// against the closed conn (or the idle deadline), and it sends to
	// errCh on its way out. The send doesn't block (errCh is buffered 2),
	// the goroutine exits, and we don't leak.
	return <-errCh
}
