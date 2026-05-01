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

// Command adapter-echo is the event adapter sidecar for the
// paddock-echo harness. It tails PADDOCK_RAW_PATH, converts each raw
// JSONL line to a PaddockEvent, and appends it to PADDOCK_EVENTS_PATH.
// The collector sidecar (M6) is responsible for persisting the output
// to the workspace PVC and delivering it to the controller.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// streamSubprotocol is the WebSocket subprotocol the broker negotiates
// on /stream — must match `internal/broker/stream.go`.
const streamSubprotocol = "paddock.stream.v1"

const (
	defaultPoll = 200 * time.Millisecond
	// Bind to all interfaces. The broker connects from another pod via
	// the run pod's eth0 IP, so a loopback-only listener (127.0.0.1)
	// would be unreachable. NetworkPolicy ingress (controller Task 12)
	// restricts the actual peer set to broker-namespace + broker-pod
	// labels, which is the load-bearing security gate.
	defaultInteractiveAddr = ":8431"
)

func main() {
	rawPath := flag.String("raw", envOr("PADDOCK_RAW_PATH", "/paddock/raw/out"), "Path to raw input JSONL (tailed).")
	eventsPath := flag.String("events", envOr("PADDOCK_EVENTS_PATH", "/paddock/events/events.jsonl"), "Path to PaddockEvents output JSONL.")
	poll := flag.Duration("poll", defaultPoll, "Poll interval while tailing input.")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if os.Getenv("PADDOCK_INTERACTIVE_MODE") != "" {
		// We deliberately don't log the env-var value: gosec's taint
		// tracker treats os.Getenv as user-controlled (G706), and the
		// value carries no operator-debug signal beyond "interactive
		// vs batch", which the address line below already tells you.
		log.Printf("adapter-echo: starting in interactive mode")
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, "tcp", defaultInteractiveAddr)
		if err != nil {
			log.Fatalf("adapter-echo interactive: listen %s: %v", defaultInteractiveAddr, err)
		}
		if err := runInteractive(ctx, ln, *eventsPath); err != nil {
			log.Fatalf("adapter-echo interactive: %v", err)
		}
		return
	}

	if err := run(ctx, *rawPath, *eventsPath, *poll); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("adapter-echo: %v", err)
	}
}

// runInteractive starts the loopback HTTP server for interactive mode and
// blocks until ctx is cancelled (SIGTERM/SIGINT) or the server fails.
//
// It mirrors cmd/adapter-claude-code's runInteractive but for the echo
// harness — no per-prompt subprocess. Each /prompts handler returns
// 202 Accepted, appends a synthetic PaddockEvent to events.jsonl (so a
// tailing collector observes the prompt), AND best-effort relays the
// frame to any active /stream WebSocket subscriber so the TUI broker
// client can exercise the live stream path end-to-end.
func runInteractive(ctx context.Context, ln net.Listener, eventsPath string) error {
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir events dir: %w", err)
	}
	out, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}
	defer out.Close()

	enc := json.NewEncoder(out)
	var mu sync.Mutex // serialises events.jsonl writes

	// streamFrames buffers frames pushed by /prompts for any active
	// /stream subscriber. Buffered so /prompts never blocks if no
	// subscriber is connected (best-effort relay). Single-subscriber
	// only — sufficient for the broker's bridge.
	streamFrames := make(chan []byte, 16)

	mux := http.NewServeMux()
	mux.HandleFunc("/prompts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		// Drain up to 256 KiB of the body just to consume it; the echo
		// adapter is a stub and doesn't actually process the prompt
		// content beyond echoing it into events.jsonl.
		var body struct {
			Text string `json:"text"`
			Seq  int32  `json:"seq"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&body)
		summary := fmt.Sprintf("interactive echo received prompt seq=%d", body.Seq)
		mu.Lock()
		_ = enc.Encode(map[string]any{
			"schemaVersion": "1",
			"ts":            time.Now().UTC().Format(time.RFC3339Nano),
			"type":          "Message",
			"summary":       summary,
			"fields":        map[string]string{"text": body.Text},
		})
		_ = out.Sync()
		mu.Unlock()
		// Best-effort relay to any active /stream subscriber. The TUI
		// broker client expects {"type": "...", "data": {...}} per frame.
		if frame, err := json.Marshal(map[string]any{
			"type": "Message",
			"data": map[string]any{
				"schemaVersion": "1",
				"ts":            time.Now().UTC().Format(time.RFC3339Nano),
				"summary":       summary,
				"fields":        map[string]string{"text": body.Text},
			},
		}); err == nil {
			select {
			case streamFrames <- frame:
			default: // no subscriber or buffer full; drop
			}
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{streamSubprotocol},
		})
		if err != nil {
			// Accept already wrote a response.
			return
		}
		defer conn.CloseNow() //nolint:errcheck

		// Send an immediate ready frame so the broker → TUI bridge sees
		// at least one frame even before any prompt arrives — exercises
		// the dial + first-frame round-trip without depending on prompt
		// timing in CI.
		ready := []byte(`{"type":"echo.ready","data":{"summary":"echo adapter stream ready"}}`)
		if err := conn.Write(r.Context(), websocket.MessageText, ready); err != nil {
			return
		}

		// Pump prompt-relayed frames until the client disconnects or ctx
		// expires. /prompts pushes onto streamFrames best-effort, so a
		// subscriber connected at the moment of the push receives it.
		for {
			select {
			case frame := <-streamFrames:
				if err := conn.Write(r.Context(), websocket.MessageText, frame); err != nil {
					return
				}
			case <-r.Context().Done():
				return
			case <-ctx.Done():
				return
			}
		}
	})
	mux.HandleFunc("/interrupt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("adapter-echo interactive: listening on %s", ln.Addr())
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("interactive server: %w", err)
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// run tails rawPath, splits on '\n', converts each complete line to a
// PaddockEvent, and appends it to eventsPath. Cleanly returns on
// context cancel (SIGTERM during Pod shutdown). Waits for the input
// file to appear because native sidecars may start before the harness
// has produced it.
func run(ctx context.Context, rawPath, eventsPath string, poll time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir events dir: %w", err)
	}
	out, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}
	defer out.Close()

	in, err := openOrWait(ctx, rawPath, poll)
	if err != nil {
		return err
	}
	defer in.Close()

	enc := json.NewEncoder(out)
	var carry []byte
	buf := make([]byte, 4096)

	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			carry = append(carry, buf[:n]...)
			for {
				idx := bytes.IndexByte(carry, '\n')
				if idx < 0 {
					break
				}
				line := string(carry[:idx+1])
				carry = carry[idx+1:]
				if err := emit(enc, out, line); err != nil {
					return err
				}
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			select {
			case <-ctx.Done():
				// Flush any trailing bytes as a final line.
				if len(bytes.TrimSpace(carry)) > 0 {
					_ = emit(enc, out, string(carry))
				}
				return nil
			case <-time.After(poll):
			}
			continue
		}
		return fmt.Errorf("read raw: %w", readErr)
	}
}

func emit(enc *json.Encoder, w *os.File, line string) error {
	ev, err := convertLine(line, time.Now().UTC())
	if err != nil {
		log.Printf("skip malformed line: %v", err)
		return nil
	}
	if err := enc.Encode(ev); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	_ = w.Sync()
	return nil
}

func openOrWait(ctx context.Context, path string, poll time.Duration) (*os.File, error) {
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("open raw: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}
