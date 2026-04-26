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
	"net"
	"strconv"
	"time"
)

// dialUpstreamTLS opens a TLS connection to tcpAddr, presenting and
// verifying the peer certificate against serverName. Owns the dialer
// fallback, TLS-config clone with ServerName injection, and the
// HandshakeContext-with-timeout shared by both upstream legs.
//
// Callers:
//   - cooperative mode (dialUpstream): tcpAddr = net.JoinHostPort(host, port);
//     serverName = host. The dial address and the cert hostname coincide.
//   - transparent mode (dialUpstreamAt): tcpAddr = net.JoinHostPort(ip, port);
//     serverName = sni. The dial address is the SO_ORIGINAL_DST IP, but
//     the cert is verified against the agent-requested SNI so the agent's
//     intent (connect to hostname X) is preserved.
func (s *Server) dialUpstreamTLS(ctx context.Context, tcpAddr, serverName string) (net.Conn, error) {
	dialer := s.UpstreamDialer
	if dialer == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		dialer = d.DialContext
	}
	raw, err := dialer(ctx, "tcp", tcpAddr)
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
		return nil, fmt.Errorf("upstream TLS handshake: %w", err)
	}
	return tlsConn, nil
}

// joinHostPortInt is a small helper around net.JoinHostPort + strconv.Itoa
// used by both wrappers. Inlined deliberately — keeps the call sites short.
func joinHostPortInt(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
