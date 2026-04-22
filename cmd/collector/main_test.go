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
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRun_PersistsAndPublishes wires tail + ring + publisher into the
// full run loop using a recorder WriteFunc. Verifies:
//   - raw.jsonl + events.jsonl appear on the PVC stand-in with the
//     input bytes verbatim;
//   - the ConfigMap snapshot carries the ring contents;
//   - result.json is propagated on shutdown;
//   - phase transitions Running → Completed.
func TestRun_PersistsAndPublishes(t *testing.T) {
	dir := t.TempDir()
	rawSrc := filepath.Join(dir, "shared", "raw", "out")
	evSrc := filepath.Join(dir, "shared", "events", "events.jsonl")
	workspace := filepath.Join(dir, "workspace")
	resultPath := filepath.Join(workspace, ".paddock", "runs", "run-1", "result.json")

	mustMkdir(t, filepath.Dir(rawSrc))
	mustMkdir(t, filepath.Dir(evSrc))
	mustMkdir(t, filepath.Dir(resultPath))

	// Writer recording.
	var wmu sync.Mutex
	var snapshots []map[string]string
	writer := func(_ context.Context, data map[string]string) error {
		wmu.Lock()
		snapshots = append(snapshots, maps.Clone(data))
		wmu.Unlock()
		return nil
	}

	cfg := config{
		rawPath:       rawSrc,
		eventsPath:    evSrc,
		resultPath:    resultPath,
		workspace:     workspace,
		runName:       "run-1",
		namespace:     "default",
		cmName:        "run-1-out",
		ringMaxEvents: 50,
		ringMaxBytes:  32 * 1024,
		debounce:      30 * time.Millisecond,
		poll:          10 * time.Millisecond,
		flushTimeout:  time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, writer) }()

	// Let the collector start + create both tailers (files don't exist yet).
	time.Sleep(40 * time.Millisecond)

	// Feed raw + events.
	appendFile(t, rawSrc, `{"kind":"message","text":"raw-1"}`+"\n")
	appendFile(t, evSrc, `{"schemaVersion":"1","type":"Message","summary":"ev-1"}`+"\n")
	appendFile(t, evSrc, `{"schemaVersion":"1","type":"ToolUse","summary":"ev-2"}`+"\n")

	// Second burst to exercise debounce.
	time.Sleep(80 * time.Millisecond)
	appendFile(t, evSrc, `{"schemaVersion":"1","type":"Result","summary":"ev-3"}`+"\n")

	// result.json shows up just before shutdown (harness exit).
	time.Sleep(80 * time.Millisecond)
	mustWrite(t, resultPath, `{"summary":"echoed","filesChanged":0}`)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("run did not exit after cancel")
	}

	// PVC files contain all the bytes we wrote.
	assertFileContains(t, filepath.Join(workspace, ".paddock", "runs", "run-1", "raw.jsonl"), "raw-1")
	pvcEvents := readFile(t, filepath.Join(workspace, ".paddock", "runs", "run-1", "events.jsonl"))
	for _, want := range []string{"ev-1", "ev-2", "ev-3"} {
		if !strings.Contains(pvcEvents, want) {
			t.Errorf("events.jsonl missing %q. content:\n%s", want, pvcEvents)
		}
	}

	// Final snapshot carries events + result + Completed phase.
	wmu.Lock()
	defer wmu.Unlock()
	if len(snapshots) == 0 {
		t.Fatalf("publisher never wrote")
	}
	final := snapshots[len(snapshots)-1]
	if final["phase"] != "Completed" {
		t.Errorf("final phase = %q, want Completed", final["phase"])
	}
	if !strings.Contains(final["events.jsonl"], "ev-3") {
		t.Errorf("final events.jsonl missing ev-3, got:\n%s", final["events.jsonl"])
	}
	if !strings.Contains(final["result.json"], "echoed") {
		t.Errorf("final result.json missing content, got: %q", final["result.json"])
	}

	// At least one earlier snapshot should carry phase=Running — i.e.,
	// the debouncer fired during the run, not only on shutdown.
	var sawRunning bool
	for _, s := range snapshots[:len(snapshots)-1] {
		if s["phase"] == "Running" {
			sawRunning = true
			break
		}
	}
	if !sawRunning {
		t.Errorf("no Running snapshot before Completed (%d snapshots)", len(snapshots))
	}
}

func TestRun_NoopConfigMapWhenDisabled(t *testing.T) {
	// When cmName is empty the production path returns a no-op writer.
	// Confirm the test-facing writer can also be a no-op without
	// tripping the run loop.
	dir := t.TempDir()
	rawSrc := filepath.Join(dir, "raw", "out")
	evSrc := filepath.Join(dir, "events", "events.jsonl")
	mustMkdir(t, filepath.Dir(rawSrc))
	mustMkdir(t, filepath.Dir(evSrc))
	mustWrite(t, rawSrc, "raw\n")
	mustWrite(t, evSrc, "ev\n")

	writer := func(_ context.Context, _ map[string]string) error { return nil }

	cfg := config{
		rawPath:       rawSrc,
		eventsPath:    evSrc,
		workspace:     filepath.Join(dir, "workspace"),
		runName:       "r",
		ringMaxEvents: 10,
		ringMaxBytes:  1024,
		debounce:      20 * time.Millisecond,
		poll:          10 * time.Millisecond,
		flushTimeout:  time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	if err := run(ctx, cfg, writer); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("append open: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append write: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	got := readFile(t, path)
	if !strings.Contains(got, want) {
		t.Errorf("%s does not contain %q — content:\n%s", path, want, got)
	}
}
