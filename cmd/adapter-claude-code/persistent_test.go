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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPersistentDriver_SubmitWritesStreamJSON verifies that SubmitPrompt
// pumps a stream-json control line into the agent's stdin. The fake
// claude stub redirects its stdin to a file we can inspect.
func TestPersistentDriver_SubmitWritesStreamJSON(t *testing.T) {
	tmp := t.TempDir()

	// Fake claude that copies stdin to a file we can inspect, then exits
	// when stdin is closed (so End() can Wait without hanging).
	stub := filepath.Join(tmp, "fake-claude.sh")
	inputPath := filepath.Join(tmp, "input.jsonl")
	body := "#!/bin/sh\ncat > " + inputPath + "\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	t.Setenv("PADDOCK_WORKSPACE", tmp)
	t.Setenv("PADDOCK_RUN_NAME", "run-1")
	t.Setenv("PADDOCK_CLAUDE_BINARY", stub)

	d := NewPersistentDriver(testLogger())

	if err := d.SubmitPrompt(context.Background(), Prompt{Text: "hi", Seq: 1}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// End() closes stdin; the stub flushes input.jsonl and exits.
	if err := d.End(context.Background()); err != nil {
		t.Fatalf("end: %v", err)
	}

	// Poll for the file to exist and have content (the stub may flush
	// after stdin closes).
	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(inputPath)
		if err == nil && len(b) > 0 {
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) == 0 {
		t.Fatalf("input file empty or missing at %s", inputPath)
	}
	if !strings.Contains(string(got), `"type":"user"`) {
		t.Fatalf("input missing type=user: %q", got)
	}
	if !strings.Contains(string(got), `"text":"hi"`) {
		t.Fatalf("input missing text=hi: %q", got)
	}
}

// TestPersistentDriver_StartFailureSubmitErrors verifies that when the
// underlying claude binary cannot be spawned, the driver enters a
// "broken" state where SubmitPrompt returns an error rather than
// panicking.
func TestPersistentDriver_StartFailureSubmitErrors(t *testing.T) {
	tmp := t.TempDir()

	t.Setenv("PADDOCK_WORKSPACE", tmp)
	t.Setenv("PADDOCK_RUN_NAME", "run-1")
	// Point at a non-existent path so cmd.Start() fails.
	t.Setenv("PADDOCK_CLAUDE_BINARY", filepath.Join(tmp, "does-not-exist"))

	d := NewPersistentDriver(testLogger())

	err := d.SubmitPrompt(context.Background(), Prompt{Text: "hi", Seq: 1})
	if err == nil {
		t.Fatal("SubmitPrompt: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "agent not running") {
		t.Fatalf("SubmitPrompt error = %q, want substring %q", err, "agent not running")
	}

	// End on a broken driver must not panic.
	if err := d.End(context.Background()); err != nil {
		t.Fatalf("End on broken driver: %v", err)
	}
}

// TestPersistentDriver_EndIsIdempotent verifies that End() can be
// called repeatedly without panicking or hanging.
func TestPersistentDriver_EndIsIdempotent(t *testing.T) {
	tmp := t.TempDir()

	// Fake claude that exits immediately when stdin closes.
	stub := filepath.Join(tmp, "fake-claude.sh")
	body := "#!/bin/sh\ncat > /dev/null\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	t.Setenv("PADDOCK_WORKSPACE", tmp)
	t.Setenv("PADDOCK_RUN_NAME", "run-1")
	t.Setenv("PADDOCK_CLAUDE_BINARY", stub)

	d := NewPersistentDriver(testLogger())

	// First End closes stdin and reaps the process.
	doneCh := make(chan error, 1)
	go func() { doneCh <- d.End(context.Background()) }()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("first End: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first End: timed out")
	}

	// Second End must be a no-op (no panic, no hang).
	doneCh2 := make(chan error, 1)
	go func() { doneCh2 <- d.End(context.Background()) }()
	select {
	case err := <-doneCh2:
		if err != nil {
			t.Fatalf("second End: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second End: timed out (not idempotent)")
	}
}
