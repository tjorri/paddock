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

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// TestRun_EndToEnd exercises the tailer: it writes raw lines (some in
// two bursts separated by a poll) and asserts the adapter produces the
// expected PaddockEvent sequence in events.jsonl.
func TestRun_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw", "out")
	eventsPath := filepath.Join(dir, "events", "events.jsonl")

	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	// Start the adapter in a goroutine with a tight poll so the test
	// stays fast.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, rawPath, eventsPath, 10*time.Millisecond)
	}()

	// Burst 1: raw file does not exist yet — adapter must wait.
	time.Sleep(20 * time.Millisecond)
	writeRawLines(t, rawPath,
		`{"kind":"message","text":"hello"}`,
		`{"kind":"tool","tool":"read","path":"foo.txt"}`,
	)

	// Burst 2: append more after a gap to exercise the polling loop.
	time.Sleep(50 * time.Millisecond)
	appendRawLines(t, rawPath,
		`{"kind":"result","summary":"done","filesChanged":2}`,
	)
	time.Sleep(50 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("run did not exit after cancel")
	}

	events := readEventsFile(t, eventsPath)
	if len(events) != 3 {
		t.Fatalf("events count = %d, want 3 — events: %+v", len(events), events)
	}
	types := []string{events[0].Type, events[1].Type, events[2].Type}
	wantTypes := []string{"Message", "ToolUse", "Result"}
	for i, w := range wantTypes {
		if types[i] != w {
			t.Errorf("events[%d].Type = %q, want %q", i, types[i], w)
		}
	}
	for i, ev := range events {
		if ev.SchemaVersion != "1" {
			t.Errorf("events[%d].SchemaVersion = %q, want 1", i, ev.SchemaVersion)
		}
		if ev.Timestamp.IsZero() {
			t.Errorf("events[%d] has zero timestamp", i)
		}
	}
}

func writeRawLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create raw: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write raw: %v", err)
		}
	}
}

func appendRawLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("append raw: %v", err)
		}
	}
}

// TestInteractive_PromptReturns202 exercises the interactive-mode loopback
// HTTP server: POST /prompts must return 202 and append a synthetic event
// to events.jsonl so a tailing collector can observe the prompt.
func TestInteractive_PromptReturns202(t *testing.T) {
	tmp := t.TempDir()
	eventsPath := filepath.Join(tmp, "events.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	errCh := make(chan error, 1)
	go func() { errCh <- runInteractive(ctx, ln, eventsPath) }()

	// Poll until the server is accepting connections.
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://"+addr+"/prompts", strings.NewReader(`{"text":"hi","seq":1}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		r, err := client.Do(req)
		if err == nil {
			resp = r
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp == nil {
		t.Fatalf("server never started")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	// Cancel and wait for the server to exit before reading events.jsonl
	// — the handler may still be flushing when the response returns.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runInteractive returned err: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("runInteractive did not exit on context cancel")
	}

	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(data), "interactive echo received prompt seq=1") {
		t.Fatalf("events.jsonl missing prompt event: %s", string(data))
	}
}

// TestInteractive_ServerExitsOnContextCancel verifies the server exits
// cleanly (nil error, no stuck goroutines) when its context is cancelled.
func TestInteractive_ServerExitsOnContextCancel(t *testing.T) {
	tmp := t.TempDir()
	eventsPath := filepath.Join(tmp, "events.jsonl")

	ctx, cancel := context.WithCancel(context.Background())

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatalf("listen: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- runInteractive(ctx, ln, eventsPath) }()

	// Give it a moment to bind, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runInteractive returned err: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("runInteractive did not exit on context cancel")
	}
}

func readEventsFile(t *testing.T, path string) []paddockv1alpha1.PaddockEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer f.Close()
	var out []paddockv1alpha1.PaddockEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev paddockv1alpha1.PaddockEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan events: %v", err)
	}
	return out
}
