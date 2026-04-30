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
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// perPromptDriver implements Driver for per-prompt-process mode.
// Each SubmitPrompt spawns a fresh `claude` invocation against the
// shared workspace; conversation continuity comes from the harness's
// own session resume mechanism plus PVC files.
type perPromptDriver struct {
	logger     *log.Logger
	workspace  string
	runName    string
	claudeBin  string
	mu         sync.Mutex
	currentCmd *exec.Cmd
}

// NewPerPromptDriver constructs a per-prompt-process Driver. Reads
// PADDOCK_WORKSPACE, PADDOCK_RUN_NAME, PADDOCK_CLAUDE_BINARY (defaults
// to "claude") from env.
func NewPerPromptDriver(logger *log.Logger) Driver {
	bin := os.Getenv("PADDOCK_CLAUDE_BINARY")
	if bin == "" {
		bin = "claude"
	}
	return &perPromptDriver{
		logger:    logger,
		workspace: os.Getenv("PADDOCK_WORKSPACE"),
		runName:   os.Getenv("PADDOCK_RUN_NAME"),
		claudeBin: bin,
	}
}

func (d *perPromptDriver) SubmitPrompt(_ context.Context, p Prompt) error {
	// Fix #2: take the lock first so a rejected concurrent submit never
	// leaves a stale prompt file on disk.
	d.mu.Lock()
	if d.currentCmd != nil {
		d.mu.Unlock()
		return fmt.Errorf("a prompt is already in flight")
	}
	// Slot is free — write the prompt file while still holding the lock so
	// we remain the exclusive submitter until Start returns.
	dir := filepath.Join(d.workspace, ".paddock", "runs", d.runName, "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("mkdir prompts: %w", err)
	}
	promptPath := filepath.Join(dir, fmt.Sprintf("%d.txt", p.Seq))
	if err := os.WriteFile(promptPath, []byte(p.Text), 0o600); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("write prompt: %w", err)
	}

	// Fix #1: use exec.Command (no context binding) so the spawned process
	// is not killed when the HTTP request context is cancelled after 202.
	args := []string{"--print", "--input-format", "stream-json", "--output-format", "stream-json"}
	cmd := exec.Command(d.claudeBin, args...)
	cmd.Dir = d.workspace
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("start: %w", err)
	}
	d.currentCmd = cmd
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			d.currentCmd = nil
			d.mu.Unlock()
		}()
		_, _ = fmt.Fprintf(stdin, `{"type":"user","message":{"content":[{"type":"text","text":%q}]}}`+"\n", p.Text)
		_ = stdin.Close()
		_ = drainStreamJSON(d.logger, stdout, d.workspace, d.runName)
		_ = cmd.Wait()
	}()
	return nil
}

// wait blocks until no prompt is in flight (currentCmd == nil). Intended for
// tests only; production callers should use Interrupt/End + their own timeouts.
func (d *perPromptDriver) wait(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		done := d.currentCmd == nil
		d.mu.Unlock()
		if done {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (d *perPromptDriver) Interrupt(ctx context.Context) error {
	d.mu.Lock()
	cmd := d.currentCmd
	d.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		return fmt.Errorf("signal: %w", err)
	}
	return nil
}

func (d *perPromptDriver) End(ctx context.Context) error {
	// Signal any in-flight process to exit. The adapter's signal-handler
	// (in runInteractive in main.go) handles HTTP server shutdown; this
	// method only cancels the in-flight prompt. The adapter pod will
	// typically exit when SIGTERM is delivered by the controller, not
	// from inside End.
	d.mu.Lock()
	cmd := d.currentCmd
	d.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	return nil
}

func (d *perPromptDriver) StreamHandler() http.Handler {
	// per-prompt-process doesn't expose a stream; events flow via
	// events.jsonl on the PVC.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "stream not supported in per-prompt-process mode", http.StatusBadRequest)
	})
}

// drainStreamJSON reads newline-delimited stream-json from r, converts
// each line via convertLine (convert.go), and appends PaddockEvents to
// <workspace>/.paddock/runs/<run>/events.jsonl.
func drainStreamJSON(logger *log.Logger, r io.Reader, workspace, runName string) error {
	eventsPath := filepath.Join(workspace, ".paddock", "runs", runName, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir events: %w", err)
	}
	out, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}
	defer out.Close()
	br := bufio.NewReader(r)
	enc := json.NewEncoder(out)
	for {
		line, rerr := br.ReadString('\n')
		if line != "" {
			events, cerr := convertLine(line, time.Now().UTC())
			if cerr != nil {
				logger.Printf("convertLine: %v", cerr)
			}
			for _, ev := range events {
				if werr := enc.Encode(ev); werr != nil {
					return fmt.Errorf("write event: %w", werr)
				}
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("read: %w", rerr)
		}
	}
}
