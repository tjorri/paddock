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
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
)

// peekConn wraps a net.Conn so Read first drains a buffered copy of
// whatever earlier code peeked, then falls through to the underlying
// connection. Required for transparent-mode flow: we need to parse the
// TLS ClientHello to extract SNI before we start the TLS handshake
// ourselves, and the tls.Server we hand the connection to must still
// see those bytes.
type peekConn struct {
	net.Conn
	buffered bytes.Buffer
}

// Read drains p.buffered first; returns underlying data once empty.
func (p *peekConn) Read(b []byte) (int, error) {
	if p.buffered.Len() > 0 {
		return p.buffered.Read(b)
	}
	return p.Conn.Read(b)
}

// peekClientHello reads enough of the wire from conn to parse the TLS
// ClientHello, then stashes those bytes back onto p.buffered so the
// TLS library handshaking later sees the full stream. Only the first
// packet is consumed; subsequent bytes flow through untouched.
func peekClientHello(ctx context.Context, p *peekConn) (*tls.ClientHelloInfo, error) {
	var hello *tls.ClientHelloInfo
	// Use a zero-length tls.Server with GetConfigForClient — the only
	// way stdlib lets us inspect the ClientHello without committing to
	// an actual handshake is to abort mid-handshake.
	tee := &teeReader{upstream: p.Conn, buf: &p.buffered}
	// nullWriterConn discards writes from the throwaway tls.Server so its
	// fatal alert (sent when GetConfigForClient returns errFinishedPeeking)
	// does not corrupt the conn for the real tls.Server that follows.
	dummy := &nullWriterConn{Conn: p.Conn, r: tee}
	cfg := &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			hi := *h
			hello = &hi
			return nil, errFinishedPeeking
		},
	}
	err := tls.Server(dummy, cfg).HandshakeContext(ctx)
	if errors.Is(err, errFinishedPeeking) && hello != nil {
		return hello, nil
	}
	if hello != nil {
		return hello, nil
	}
	return nil, err
}

var errFinishedPeeking = errors.New("clientHello peeked")

// teeReader mirrors all bytes read from upstream into buf. We use it
// to let the stdlib TLS parser chew through the ClientHello while
// simultaneously building the replay buffer we hand to the real
// tls.Server afterwards.
type teeReader struct {
	upstream io.Reader
	buf      *bytes.Buffer
}

func (t *teeReader) Read(b []byte) (int, error) {
	n, err := t.upstream.Read(b)
	if n > 0 {
		_, _ = t.buf.Write(b[:n])
	}
	return n, err
}

// nullWriterConn wraps a net.Conn so all Writes are silently dropped
// while Reads delegate to a caller-supplied io.Reader. Used by
// peekClientHello to feed the ClientHello to a throwaway tls.Server
// without letting that server's fatal-alert response (sent when
// GetConfigForClient returns a non-nil error) reach the agent and
// poison the subsequent real TLS handshake.
type nullWriterConn struct {
	net.Conn
	r io.Reader
}

func (n *nullWriterConn) Read(b []byte) (int, error)  { return n.r.Read(b) }
func (n *nullWriterConn) Write(b []byte) (int, error) { return len(b), nil }
