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
	"io"
	"net"
	"testing"
	"time"
)

// driveTLSClient runs a tls.Client handshake on conn against the supplied
// SNI. The handshake will eventually fail (peer is a peekConn that aborts
// mid-handshake) — that's fine; we only need the ClientHello on the wire.
// errCh receives the (likely error) exit status so the test can cancel
// cleanly.
func driveTLSClient(conn net.Conn, sni string) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		cfg := &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true, //nolint:gosec // test peer is a peekConn that aborts mid-handshake; verification is moot
			MinVersion:         tls.VersionTLS12,
		}
		err := tls.Client(conn, cfg).HandshakeContext(context.Background())
		errCh <- err
	}()
	return errCh
}

func TestPeekClientHello_ExtractsSNI(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Drive a real TLS client on clientConn so a real ClientHello arrives
	// at serverConn.
	clientErr := driveTLSClient(clientConn, "api.example.com")

	peek := &peekConn{Conn: serverConn}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	hello, err := peekClientHello(ctx, peek)
	if err != nil {
		t.Fatalf("peekClientHello: %v", err)
	}
	if hello == nil {
		t.Fatalf("hello is nil")
	}
	if got, want := hello.ServerName, "api.example.com"; got != want {
		t.Errorf("ServerName = %q, want %q", got, want)
	}

	// Closing serverConn unblocks the goroutine. We don't care what
	// error the client got — peekClientHello aborted the handshake.
	_ = serverConn.Close()
	select {
	case <-clientErr:
	case <-time.After(2 * time.Second):
		t.Fatal("client goroutine did not exit after serverConn close")
	}
}

func TestPeekClientHello_NoSNI(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// driveTLSClient with empty SNI: tls.Client populates ServerName from
	// cfg.ServerName, and an empty ServerName means no SNI extension is
	// sent.
	clientErr := driveTLSClient(clientConn, "")

	peek := &peekConn{Conn: serverConn}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	hello, err := peekClientHello(ctx, peek)
	if err != nil {
		t.Fatalf("peekClientHello: %v", err)
	}
	if hello == nil {
		t.Fatalf("hello is nil")
	}
	if hello.ServerName != "" {
		t.Errorf("ServerName = %q, want empty (no SNI)", hello.ServerName)
	}

	_ = serverConn.Close()
	select {
	case <-clientErr:
	case <-time.After(2 * time.Second):
		t.Fatal("client goroutine did not exit after serverConn close")
	}
}

func TestPeekClientHello_BuffersClientHelloForReplay(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	clientErr := driveTLSClient(clientConn, "replay.example.com")

	peek := &peekConn{Conn: serverConn}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	if _, err := peekClientHello(ctx, peek); err != nil {
		t.Fatalf("peekClientHello: %v", err)
	}

	// After a successful peek, the bytes that came off the wire should
	// be replay-able from peek.Read. Read the buffered region and assert
	// it begins with a TLS 1.x record header (0x16 = handshake; 0x03 0x0n
	// = legacy version field).
	buf := make([]byte, 5)
	n, err := peek.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("peek.Read: %v", err)
	}
	if n < 5 {
		t.Fatalf("read %d bytes, want >=5 (TLS record header)", n)
	}
	if buf[0] != 0x16 {
		t.Errorf("first byte = %#x, want 0x16 (TLS handshake content type)", buf[0])
	}
	if buf[1] != 0x03 {
		t.Errorf("second byte = %#x, want 0x03 (TLS legacy version)", buf[1])
	}

	_ = serverConn.Close()
	select {
	case <-clientErr:
	case <-time.After(2 * time.Second):
		t.Fatal("client goroutine did not exit after serverConn close")
	}
}

func TestPeekClientHello_LeavesConnUsableForFollowupHandshake(t *testing.T) {
	// After peekClientHello returns, the conn must still be usable for a
	// real tls.Server.HandshakeContext to complete with the original client.
	// Before the fix, peekClientHello's throwaway tls.Server wrote a fatal
	// alert (15 03 01 00 02 02 50) to the underlying conn when its
	// GetConfigForClient callback returned errFinishedPeeking, which broke
	// every transparent-mode handshake in production.

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Generate an in-memory CA + leaf so we can build a real tls.Server
	// certificate.
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	leaf, err := ca.ForgeFor("example.com")
	if err != nil {
		t.Fatalf("forge: %v", err)
	}

	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)

	// Drive a real tls.Client that performs a full handshake.
	handshakeErr := make(chan error, 1)
	go func() {
		cfg := &tls.Config{
			ServerName: "example.com",
			RootCAs:    clientPool,
			MinVersion: tls.VersionTLS12,
		}
		c := tls.Client(clientConn, cfg)
		handshakeErr <- c.HandshakeContext(context.Background())
	}()

	// Server side: peek SNI, then perform the *real* handshake on the
	// same conn (the way HandleTransparentConn does in production).
	peek := &peekConn{Conn: serverConn}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	hello, err := peekClientHello(ctx, peek)
	if err != nil {
		t.Fatalf("peekClientHello: %v", err)
	}
	if hello.ServerName != "example.com" {
		t.Errorf("ServerName = %q, want example.com", hello.ServerName)
	}

	realCfg := &tls.Config{
		Certificates: []tls.Certificate{*leaf},
		MinVersion:   tls.VersionTLS12,
	}
	if err := tls.Server(peek, realCfg).HandshakeContext(ctx); err != nil {
		t.Fatalf("follow-up tls.Server.HandshakeContext: %v", err)
	}

	select {
	case err := <-handshakeErr:
		if err != nil {
			t.Fatalf("client handshake: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client handshake did not return")
	}
}

func TestPeekClientHello_ContextCanceled(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Don't drive a TLS client at all — the handshake will block on
	// reading bytes that never arrive. Cancel the context and assert
	// the function returns promptly with a non-nil error.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	peek := &peekConn{Conn: serverConn}
	_, err := peekClientHello(ctx, peek)
	if err == nil {
		t.Fatal("peekClientHello returned nil error after context cancel; want non-nil")
	}
}
