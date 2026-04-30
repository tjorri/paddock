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
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// TestPerPromptDriver_SubmitWritesPromptFile verifies that SubmitPrompt writes
// the prompt text to the expected path under the workspace.
func TestPerPromptDriver_SubmitWritesPromptFile(t *testing.T) {
	tmp := t.TempDir()

	// Create a fake claude binary that reads stdin (to allow the pipe to close
	// cleanly) and emits a minimal stream-json result line.
	fakeBin := filepath.Join(tmp, "claude")
	script := `#!/bin/bash
cat > /dev/null
printf '{"type":"result","result":"done","session_id":"test"}\n'
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	t.Setenv("PADDOCK_WORKSPACE", tmp)
	t.Setenv("PADDOCK_RUN_NAME", "run-1")
	t.Setenv("PADDOCK_CLAUDE_BINARY", fakeBin)

	drv := NewPerPromptDriver(testLogger())

	if err := drv.SubmitPrompt(context.Background(), Prompt{Text: "hello world", Seq: 1}); err != nil {
		t.Fatalf("SubmitPrompt: %v", err)
	}

	// The prompt file should be written synchronously before the goroutine starts.
	promptPath := filepath.Join(tmp, ".paddock", "runs", "run-1", "prompts", "1.txt")
	got, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("prompt file content = %q, want %q", string(got), "hello world")
	}

	// Wait for the spawned process to finish so the tmp dir can be cleaned up.
	time.Sleep(300 * time.Millisecond)
}

// TestPerPromptDriver_InterruptKills verifies that Interrupt sends SIGINT to a
// running claude process without returning an error.
func TestPerPromptDriver_InterruptKills(t *testing.T) {
	tmp := t.TempDir()

	// Fake claude that sleeps so Interrupt can race against it.
	fakeBin := filepath.Join(tmp, "claude")
	script := `#!/bin/bash
sleep 60
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	t.Setenv("PADDOCK_WORKSPACE", tmp)
	t.Setenv("PADDOCK_RUN_NAME", "run-1")
	t.Setenv("PADDOCK_CLAUDE_BINARY", fakeBin)

	drv := NewPerPromptDriver(testLogger())

	go func() {
		// Ignore the error; the context may be cancelled when the process is killed.
		_ = drv.SubmitPrompt(context.Background(), Prompt{Text: "hi", Seq: 1})
	}()

	// Give the process time to start.
	time.Sleep(200 * time.Millisecond)

	if err := drv.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt returned error: %v", err)
	}
}
