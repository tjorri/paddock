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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// TestRun_FixturePipeline drives a hand-rolled claude stream-json
// transcript through the tailer and asserts the full event output is
// what the adapter is supposed to produce. Proves the multi-event-
// per-line path (mixed assistant content) survives the I/O plumbing.
func TestRun_FixturePipeline(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw", "out")
	eventsPath := filepath.Join(dir, "events", "events.jsonl")
	mustMkdir(t, filepath.Dir(rawPath))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, rawPath, eventsPath, 10*time.Millisecond) }()

	// Raw file doesn't exist yet — adapter must wait.
	time.Sleep(20 * time.Millisecond)

	fixtures := []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"claude-sonnet-4-6","tools":["Read","Edit"]}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"reading the file"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"auth.py"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"def auth(): pass"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"cost_usd":0.05,"total_cost_usd":0.05,"duration_ms":2500,"num_turns":2,"result":"All done"}`,
	}
	writeLines(t, rawPath, fixtures[:2])
	time.Sleep(50 * time.Millisecond)
	appendLines(t, rawPath, fixtures[2:])
	time.Sleep(50 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("run didn't exit after cancel")
	}

	events := readEvents(t, eventsPath)
	// Expected event sequence:
	//   1. Message (system init)
	//   2. Message (assistant text: "reading the file")
	//   3. ToolUse (Read)
	//   [user tool_result: skipped]
	//   4. Message (assistant text: "Done.")
	//   5. Result
	if len(events) != 5 {
		t.Fatalf("events = %d, want 5 — got:\n%+v", len(events), events)
	}
	wantTypes := []string{"Message", "Message", "ToolUse", "Message", "Result"}
	for i, w := range wantTypes {
		if events[i].Type != w {
			t.Errorf("events[%d].Type = %q, want %q", i, events[i].Type, w)
		}
	}
	if events[0].Summary != "claude-code session started" {
		t.Errorf("events[0].Summary = %q", events[0].Summary)
	}
	if events[1].Summary != "reading the file" {
		t.Errorf("events[1].Summary = %q", events[1].Summary)
	}
	if events[2].Fields["tool"] != "Read" {
		t.Errorf("events[2] tool = %q", events[2].Fields["tool"])
	}
	if events[3].Summary != "Done." {
		t.Errorf("events[3].Summary = %q", events[3].Summary)
	}
	if events[4].Summary != "All done" {
		t.Errorf("events[4].Summary = %q", events[4].Summary)
	}
	for i, ev := range events {
		if ev.SchemaVersion != "1" {
			t.Errorf("events[%d].SchemaVersion = %q, want 1", i, ev.SchemaVersion)
		}
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func appendLines(t *testing.T, path string, lines []string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("append open: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func readEvents(t *testing.T, path string) []paddockv1alpha1.PaddockEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer f.Close()
	var out []paddockv1alpha1.PaddockEvent
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var ev paddockv1alpha1.PaddockEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}
