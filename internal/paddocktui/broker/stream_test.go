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

package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// newStreamTestClient builds a minimal Client pointed at the given
// httptest.Server (no real kube, no port-forward). The token cache is
// pre-loaded with a static token so no TokenRequest is needed.
func newStreamTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		opts:    Options{ServiceAccount: "default", Namespace: "ns"},
		httpCli: srv.Client(),
		baseURL: srv.URL,
		auth:    &tokenCache{token: "test-token", expires: time.Now().Add(time.Hour)},
	}
}

// mustFrame wraps a value into a StreamFrame with json.RawMessage data.
func mustFrame(t *testing.T, typ string, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustFrame: marshal data: %v", err)
	}
	f := StreamFrame{Type: typ, Data: json.RawMessage(data)}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("mustFrame: marshal frame: %v", err)
	}
	return b
}

// TestStream_RoundTrip opens a stream, writes one frame from the
// server, and asserts the client receives it.
func TestStream_RoundTrip(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{streamSubprotocol},
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		frame := mustFrame(t, "text_delta", map[string]string{"text": "hello"})
		if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
			t.Errorf("server write: %v", err)
		}
	}))
	defer srv.Close()

	c := newStreamTestClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.Open(ctx, "ns", "run1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before receiving frame")
		}
		if f.Type != "text_delta" {
			t.Errorf("frame.Type = %q, want %q", f.Type, "text_delta")
		}
		var data struct{ Text string }
		if err := json.Unmarshal(f.Data, &data); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
		if data.Text != "hello" {
			t.Errorf("data.Text = %q, want %q", data.Text, "hello")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for first frame")
	}
}

// TestStream_Reconnect forces a synthetic close (server closes
// abruptly), asserts the client reconnects transparently, then writes
// a second frame that the client receives.
func TestStream_Reconnect(t *testing.T) {
	t.Parallel()

	// dialCount tracks how many times the server accepted a connection.
	var dialCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{streamSubprotocol},
		})
		if err != nil {
			return
		}

		n := dialCount.Add(1)

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if n == 1 {
			// First connection: send a frame then close abruptly (no
			// StatusNormalClosure) to trigger client reconnect.
			frame := mustFrame(t, "text_delta", map[string]string{"text": "first"})
			_ = conn.Write(ctx, websocket.MessageText, frame)
			// CloseNow drops the connection without a clean WS close
			// handshake, simulating a server crash / network drop.
			_ = conn.CloseNow()
			return
		}

		// Second connection: send a second frame then close normally.
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
		frame := mustFrame(t, "text_delta", map[string]string{"text": "second"})
		_ = conn.Write(ctx, websocket.MessageText, frame)
	}))
	defer srv.Close()

	c := newStreamTestClient(t, srv)

	// Use a short-backoff client by temporarily overriding the global
	// backoff. We patch backoff via a very short reconnect window by
	// closing quickly and using a fast-running context.
	// Since the actual backoff uses time.After(1s) for attempt=0, we
	// use a context long enough to let one reconnect happen.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := c.Open(ctx, "ns", "run1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Read first frame.
	var received []string
	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before first frame")
		}
		var d struct{ Text string }
		_ = json.Unmarshal(f.Data, &d)
		received = append(received, d.Text)
	case <-ctx.Done():
		t.Fatal("timed out waiting for first frame")
	}

	// Read second frame (arrives after reconnect).
	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before second frame")
		}
		var d struct{ Text string }
		_ = json.Unmarshal(f.Data, &d)
		received = append(received, d.Text)
	case <-ctx.Done():
		t.Fatalf("timed out waiting for second frame; dialCount=%d", dialCount.Load())
	}

	if len(received) != 2 || received[0] != "first" || received[1] != "second" {
		t.Errorf("received frames = %v, want [first second]", received)
	}
	if dialCount.Load() < 2 {
		t.Errorf("dialCount = %d, want >= 2 (reconnect must have happened)", dialCount.Load())
	}
}

// TestStream_CtxCancel cancels the context and asserts the frame
// channel closes and no goroutine leaks.
func TestStream_CtxCancel(t *testing.T) {
	t.Parallel()

	// ready signals when the server has accepted the WS connection.
	ready := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{streamSubprotocol},
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		close(ready)

		// Block until the request context is done (client cancelled).
		<-r.Context().Done()
	}))
	defer srv.Close()

	goroutinesBefore := runtime.NumGoroutine()

	c := newStreamTestClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := c.Open(ctx, "ns", "run1")
	if err != nil {
		cancel()
		t.Fatalf("Open: %v", err)
	}

	// Wait for the server to accept the connection before cancelling.
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for server to accept WS connection")
	}

	// Cancel the context; the read loop must exit.
	cancel()

	// Assert the channel closes within 2 seconds.
	select {
	case _, ok := <-ch:
		if ok {
			// Drain any remaining frames until closed.
			for range ch {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine leak: channel did not close within 2s after ctx cancel")
	}

	// Wait briefly for goroutines spawned by Open to exit, then check
	// the count. Allow a small delta for test-framework goroutines.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= goroutinesBefore+2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	goroutinesAfter := runtime.NumGoroutine()
	if goroutinesAfter > goroutinesBefore+2 {
		t.Errorf("goroutine leak: before=%d after=%d (delta=%d, want <=2)",
			goroutinesBefore, goroutinesAfter, goroutinesAfter-goroutinesBefore)
	}
}

// TestStream_ReconnectCounterResets asserts that every successful dial
// resets the backoff counter, so each subsequent disconnect-then-redial
// pays only the attempt-0 cost (1s).
//
// Sequence: three drops after a fresh handshake, then the fourth dial
// sends a frame.
//
//	With reset:    backoffs = 1+1+1 = 3s
//	Without reset: backoffs = 1+2+4 = 7s
//
// A 5s deadline distinguishes the two: a non-resetting implementation
// would still be sleeping when the deadline fires; the resetting
// implementation receives the frame around the 3s mark.
func TestStream_ReconnectCounterResets(t *testing.T) {
	t.Parallel()

	var dialCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{streamSubprotocol},
		})
		if err != nil {
			return
		}
		n := dialCount.Add(1)
		if n < 4 {
			// Drop immediately to force a reconnect.
			_ = conn.CloseNow()
			return
		}
		// Fourth connection: send a frame and close cleanly.
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
		frame := mustFrame(t, "done", map[string]string{"msg": "ok"})
		_ = conn.Write(ctx, websocket.MessageText, frame)
	}))
	defer srv.Close()

	c := newStreamTestClient(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	ch, err := c.Open(ctx, "ns", "run1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before receiving final frame")
		}
		if f.Type != "done" {
			t.Errorf("frame.Type = %q, want done", f.Type)
		}
		elapsed := time.Since(start)
		// With reset the frame should arrive in ~3s; without reset it
		// would still be sleeping past the 5s deadline. Allow some slack
		// for slow CI but stay below the no-reset floor of 7s.
		if elapsed > 4500*time.Millisecond {
			t.Errorf("frame took %s; counter likely not resetting (no-reset floor ~7s, reset target ~3s)", elapsed)
		}
	case <-ctx.Done():
		t.Fatalf("timed out at %s; dialCount=%d — counter not resetting (no-reset would take ~7s)",
			time.Since(start), dialCount.Load())
	}

	if got := dialCount.Load(); got < 4 {
		t.Errorf("dialCount = %d, want >= 4", got)
	}
}

// TestStream_BearerTokenSent asserts the Authorization header is
// forwarded on each dial.
func TestStream_BearerTokenSent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "missing or wrong bearer: "+got, http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{streamSubprotocol},
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		frame := mustFrame(t, "auth_ok", nil)
		_ = conn.Write(ctx, websocket.MessageText, frame)
	}))
	defer srv.Close()

	c := newStreamTestClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.Open(ctx, "ns", "run1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before frame")
		}
		if f.Type != "auth_ok" {
			t.Errorf("frame.Type = %q, want auth_ok", f.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out — bearer token likely not sent or server rejected it")
	}
}
