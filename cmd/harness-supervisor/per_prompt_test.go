package main

import (
	"bufio"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPerPrompt_TwoPromptsTwoProcesses(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:       "per-prompt-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPerPrompt(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	scanner := bufio.NewScanner(dataConn)
	enc := json.NewEncoder(ctlConn)

	// First prompt.
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataConn.Write([]byte(`"first"` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}
	// The fake harness emits a "ready" event before processing input;
	// drain it before checking the actual response.
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"ready"`) {
			continue
		}
		if !strings.Contains(scanner.Text(), `"first"`) {
			t.Errorf("first response = %q, want contains \"first\"", scanner.Text())
		}
		break
	}

	// Second prompt — must spawn a fresh process (the fake CLI exits on stdin EOF,
	// so the second begin-prompt requires a new process).
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataConn.Write([]byte(`"second"` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"ready"`) {
			continue
		}
		if !strings.Contains(scanner.Text(), `"second"`) {
			t.Errorf("second response = %q, want contains \"second\"", scanner.Text())
		}
		break
	}

	if err := enc.Encode(map[string]any{"action": "end"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("runPerPrompt: %v", err)
	}
}

func TestPerPrompt_BoundedShutdown(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:       "per-prompt-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: filepath.Join(testFixturesDir(t), "sleep_forever.sh"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPerPrompt(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	enc := json.NewEncoder(ctlConn)
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 1}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() { // ready
		t.Fatalf("scan ready: %v", scanner.Err())
	}

	// end-prompt with a CLI that ignores stdin close and SIGTERM. endPrompt
	// must return within ~shutdownGentle + shutdownHard.
	start := time.Now()
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"action": "end"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errCh:
	case <-time.After(8 * time.Second):
		t.Fatalf("runPerPrompt did not bound shutdown within 8s")
	}
	if elapsed := time.Since(start); elapsed > 7*time.Second {
		t.Errorf("shutdown took %v, want <7s", elapsed)
	}
}

func TestPerPrompt_PromptCrashedEvent(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:       "per-prompt-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: filepath.Join(testFixturesDir(t), "crash_on_first_prompt.sh"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPerPrompt(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	enc := json.NewEncoder(ctlConn)
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 7}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() { // ready
		t.Fatalf("scan ready: %v", scanner.Err())
	}
	if _, err := dataConn.Write([]byte(`"hi"` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(ctlConn)
	var got ctlMessage
	_ = ctlConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode prompt-crashed event: %v", err)
	}
	if got.Event != "prompt-crashed" {
		t.Errorf("event = %q, want \"prompt-crashed\"", got.Event)
	}
	if got.Seq != 7 {
		t.Errorf("seq = %d, want 7", got.Seq)
	}
	if got.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", got.ExitCode)
	}

	// Clean up.
	if err := enc.Encode(map[string]any{"action": "end"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("runPerPrompt: %v", err)
	}
}

func TestPerPrompt_InterruptKillsCurrentProcess(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:       "per-prompt-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPerPrompt(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	enc := json.NewEncoder(ctlConn)
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 1}); err != nil {
		t.Fatal(err)
	}

	// Wait for the fixture's "ready" event, which fires right after
	// it installs the SIGINT trap. Sending the interrupt before that
	// races bash's startup: SIGINT's default action terminates the
	// process, killing fake_harness before the trap is in place.
	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() {
		t.Fatalf("scan ready: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"ready"`) {
		t.Fatalf("expected ready event, got %q", scanner.Text())
	}

	if _, err := dataConn.Write([]byte(`"slow-prompt"` + "\n")); err != nil {
		t.Fatal(err)
	}
	// Send interrupt mid-prompt; the fake_harness's INT trap is non-fatal,
	// so the CLI continues running until end-prompt closes its stdin.
	if err := enc.Encode(map[string]any{"action": "interrupt"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}

	// Drain whatever output came back (the response or the interrupted line).
	for scanner.Scan() {
		text := scanner.Text()
		if strings.Contains(text, `"slow-prompt"`) || strings.Contains(text, `"interrupted"`) {
			break
		}
	}

	if err := enc.Encode(map[string]any{"action": "end"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("runPerPrompt: %v", err)
	}
}
