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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const streamSubprotocol = "paddock.stream.v1"

// maxReconnectAttempts is the number of reconnect attempts before Open
// gives up and closes the frame channel.
const maxReconnectAttempts = 5

// StreamFrame is one event frame off the broker stream. Type and Data
// are passed through verbatim from the adapter; the TUI projects them
// into PaddockEvents.
type StreamFrame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Open opens the broker stream for run ns/run. Frames flow on the
// returned channel until ctx is cancelled or the connection closes
// permanently after exhausting reconnect attempts.
// Reconnects with exponential backoff (1s/2s/4s/8s, max 5 attempts);
// a successful dial resets the backoff counter.
func (c *Client) Open(ctx context.Context, ns, run string) (<-chan StreamFrame, error) {
	// Validate the initial token fetch succeeds so a misconfigured client
	// fails fast at Open() rather than silently looping in the goroutine.
	if _, err := c.auth.Get(ctx); err != nil {
		return nil, err
	}

	wsURL := streamURL(c.baseURL, ns, run)

	out := make(chan StreamFrame, 16)
	go func() {
		defer close(out)
		attempt := 0
		for attempt < maxReconnectAttempts {
			// Check context before each dial attempt.
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Refresh the token on every dial so a long-lived stream picks
			// up rotations from tokenCache. tokenCache caches near-half-TTL
			// so this is cheap on the steady-state path.
			tok, err := c.auth.Get(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				backoff(ctx, attempt)
				attempt++
				continue
			}

			conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{ //nolint:bodyclose // upgrade response body is hijacked by the WS conn
				HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + tok}},
				Subprotocols: []string{streamSubprotocol},
				HTTPClient:   c.httpCli,
			})
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				backoff(ctx, attempt)
				attempt++
				continue
			}

			// Successful dial — reset the backoff counter.
			attempt = 0

			// readFrames returns false when ctx is cancelled; true when the
			// connection closed normally (reconnect).
			reconnect := readFrames(ctx, conn, out)
			if !reconnect {
				return
			}
			// Connection dropped; back off before reconnecting.
			backoff(ctx, attempt)
			attempt++
		}
	}()
	return out, nil
}

// streamURL converts an https:// base URL to a wss:// WebSocket URL
// for the run stream endpoint.
func streamURL(baseURL, ns, run string) string {
	u := strings.Replace(baseURL, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return fmt.Sprintf("%s/v1/runs/%s/%s/stream", u, ns, run)
}

// readFrames reads JSON frames from conn and sends them to out until
// ctx is cancelled or the connection closes. Returns false when the
// caller should stop (ctx cancelled), true when a reconnect should be
// attempted (connection dropped).
func readFrames(ctx context.Context, conn *websocket.Conn, out chan<- StreamFrame) (reconnect bool) {
	// loopDone signals the watcher goroutine that the read loop has
	// exited so it can stop waiting and exit cleanly.
	loopDone := make(chan struct{})
	defer close(loopDone)

	// Spawn a watcher that closes conn when ctx is cancelled, unblocking
	// any in-flight conn.Read call and preventing a goroutine leak.
	go func() {
		select {
		case <-ctx.Done():
			// Cancel the blocked Read by closing the connection.
			_ = conn.Close(websocket.StatusNormalClosure, "context cancelled")
		case <-loopDone:
			// Read loop exited on its own; nothing to do.
		}
	}()

	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return false
			}
			return true // connection dropped; ask for reconnect
		}

		var f StreamFrame
		if err := json.Unmarshal(data, &f); err != nil {
			// Malformed frame; skip and continue reading.
			continue
		}

		select {
		case out <- f:
		case <-ctx.Done():
			return false
		}
	}
}

// backoff blocks for 2^attempt seconds (capped at 8 s) or until ctx is
// cancelled.
func backoff(ctx context.Context, attempt int) {
	delays := []time.Duration{1, 2, 4, 8}
	idx := attempt
	if idx >= len(delays) {
		idx = len(delays) - 1
	}
	d := delays[idx] * time.Second
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
