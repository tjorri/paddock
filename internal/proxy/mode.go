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
	"fmt"
	"io"
	"net"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// HandleTransparentConn is the entry point for an iptables-redirected
// TCP connection. Transparent mode differs from cooperative (CONNECT)
// mode in where the target information comes from:
//
//   - Cooperative: the HTTP CONNECT line carries host:port — proxy
//     parses, validates, forges, MITM.
//   - Transparent: SO_ORIGINAL_DST recovers the original IP:port that
//     the kernel would have routed to; SNI from the client's TLS
//     ClientHello supplies the hostname for leaf forging + validation.
//     The upstream leg dials the original IP:port directly.
//
// Hostname-less traffic (no SNI) is dropped with a deny AuditEvent —
// M4's HTTPS-only stance holds under transparent mode too. Plain HTTP
// traffic on :80 is handled by reading the Host header out of the
// first request line before forging; that is deferred to M8 (the
// gitforge work) since no v0.3 agent makes plain-HTTP egress calls.
func (s *Server) HandleTransparentConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	origIP, origPort, err := originalDestination(conn)
	if err != nil {
		s.log().V(1).Info("SO_ORIGINAL_DST failed", "err", err)
		return
	}

	// Peek the ClientHello to extract SNI without consuming bytes.
	peek := &peekConn{Conn: conn}
	hello, err := peekClientHello(ctx, peek)
	if err != nil {
		s.log().V(1).Info("ClientHello peek failed", "err", err, "dst", origIP.String())
		if aErr := s.recordEgress(ctx, EgressEvent{
			Host:     origIP.String(),
			Port:     origPort,
			Decision: paddockv1alpha1.AuditDecisionDenied,
			Reason:   "could not parse TLS ClientHello — plain-HTTP transparent interception is a v0.4 item",
		}); aErr != nil {
			s.log().Error(aErr, "audit write failed on transparent deny", "host", origIP.String(), "port", origPort)
			abruptClose(conn)
		}
		return
	}
	sni := hello.ServerName
	if sni == "" {
		if aErr := s.recordEgress(ctx, EgressEvent{
			Host:     origIP.String(),
			Port:     origPort,
			Decision: paddockv1alpha1.AuditDecisionDenied,
			Reason:   "TLS ClientHello did not carry an SNI; destinations without SNI are blocked in v0.3",
		}); aErr != nil {
			s.log().Error(aErr, "audit write failed on transparent deny", "host", origIP.String(), "port", origPort)
			abruptClose(conn)
		}
		return
	}

	decision, vErr := s.Validator.ValidateEgress(ctx, sni, origPort)
	if vErr != nil {
		s.log().Error(vErr, "validator error", "host", sni, "port", origPort)
		if aErr := s.recordEgress(ctx, EgressEvent{
			Host: sni, Port: origPort,
			Decision: paddockv1alpha1.AuditDecisionDenied,
			Reason:   fmt.Sprintf("validator error: %v", vErr),
		}); aErr != nil {
			s.log().Error(aErr, "audit write failed on transparent deny", "host", sni, "port", origPort)
			abruptClose(conn)
		}
		return
	}
	if !decision.Allowed {
		s.log().V(1).Info("denied", "host", sni, "port", origPort, "reason", decision.Reason)
		if aErr := s.recordEgress(ctx, EgressEvent{
			Host: sni, Port: origPort,
			Decision: paddockv1alpha1.AuditDecisionDenied,
			Reason:   decision.Reason,
		}); aErr != nil {
			s.log().Error(aErr, "audit write failed on transparent deny", "host", sni, "port", origPort)
			abruptClose(conn)
		}
		return
	}

	s.mitmTransparent(ctx, peek, sni, origIP, origPort, decision)
}

// mitmTransparent terminates TLS on the agent side (leaf forged from
// SNI), dials the upstream at the original IP:port, and proxies bytes.
// Symmetric with Server.mitm but without the CONNECT 200-OK write —
// transparent mode drops straight into TLS termination.
//
// Branches to the substitute-auth request loop when the matched
// grant so declares, identical to the cooperative MITM path.
func (s *Server) mitmTransparent(
	ctx context.Context,
	clientConn net.Conn,
	sni string,
	origIP net.IP,
	origPort int,
	decision Decision,
) {
	leaf, err := s.CA.ForgeFor(sni)
	if err != nil {
		s.log().Error(err, "forge leaf", "host", sni)
		return
	}
	clientTLS := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*leaf},
		MinVersion:   tls.VersionTLS12,
	})
	hsCtx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
	defer cancel()
	if err := clientTLS.HandshakeContext(hsCtx); err != nil {
		s.log().V(1).Info("client TLS handshake failed", "host", sni, "err", err)
		return
	}
	defer func() { _ = clientTLS.Close() }()

	upstream, err := s.dialUpstreamAt(ctx, sni, origIP, origPort)
	if err != nil {
		s.log().V(1).Info("upstream dial failed", "host", sni, "err", err)
		return
	}
	defer func() { _ = upstream.Close() }()

	kind := paddockv1alpha1.AuditKindEgressAllow
	if decision.DiscoveryAllow {
		kind = paddockv1alpha1.AuditKindEgressDiscoveryAllow
	}
	if aErr := s.recordEgress(ctx, EgressEvent{
		Host: sni, Port: origPort,
		Decision:      paddockv1alpha1.AuditDecisionGranted,
		MatchedPolicy: decision.MatchedPolicy,
		Kind:          kind,
		Reason:        decision.Reason,
	}); aErr != nil {
		// Allow path proceeds despite audit failure — see comment in
		// server.go's mitm() for the rationale.
		s.log().Error(aErr, "audit write failed on allow path", "host", sni, "port", origPort)
	}

	if decision.SubstituteAuth && s.Substituter != nil {
		if err := handleSubstituted(ctx, clientTLS, upstream, sni, origPort, s.Substituter, s.idleTimeout()); err != nil {
			s.log().V(1).Info("substitute-auth MITM ended", "host", sni, "err", err)
		}
		return
	}

	// F-25: idle-deadline on each direction so a stale-policy tunnel
	// closes within s.idleTimeout(). copyNonBlocking is now wrapper-only
	// kept for the test scaffolding (legacy alias).
	clientReader := &deadlineExtendingReader{conn: clientTLS, timeout: s.idleTimeout()}
	upstreamReader := &deadlineExtendingReader{conn: upstream, timeout: s.idleTimeout()}
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(upstream, clientReader); errCh <- err }()
	go func() { _, err := io.Copy(clientTLS, upstreamReader); errCh <- err }()
	<-errCh
}

// dialUpstreamAt dials a TLS upstream using the caller-specified IP
// directly — i.e. the SO_ORIGINAL_DST target — but verifies the peer
// certificate against the SNI the agent requested. This preserves the
// agent's intent (connect to hostname X) while respecting the kernel's
// original routing decision. All shared logic lives in dialUpstreamTLS.
func (s *Server) dialUpstreamAt(ctx context.Context, sni string, ip net.IP, port int) (net.Conn, error) {
	return s.dialUpstreamTLS(ctx, joinHostPortInt(ip.String(), port), sni)
}

// abruptClose sets SO_LINGER to 0 on conn (when supported) so the
// deferred Close sends a TCP RST instead of a graceful FIN. Used on
// transparent-mode deny paths when audit emission fails — the agent
// sees an unexpected connection drop instead of a clean close and can
// retry rather than treating the graceful close as a final decision.
func abruptClose(conn net.Conn) {
	if tc, ok := conn.(interface{ SetLinger(int) error }); ok {
		_ = tc.SetLinger(0)
	}
}
