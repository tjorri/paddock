package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistent_RoundTrip(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:        "persistent-process",
		DataSocket:  dataPath,
		CtlSocket:   ctlPath,
		HarnessBin:  testFixtureHarness(t),
		HarnessArgs: nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	// Wait for the supervisor to listen, then dial both UDS.
	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	scanner := bufio.NewScanner(dataConn)

	// Drain the fixture's "ready" event before sending a prompt.
	if !scanner.Scan() {
		t.Fatalf("scan ready: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"ready"`) {
		t.Fatalf("expected ready event, got %q", scanner.Text())
	}

	// Send a prompt line on data UDS.
	if _, err := dataConn.Write([]byte(`"hello"` + "\n")); err != nil {
		t.Fatalf("write data: %v", err)
	}

	// Read the response line.
	if !scanner.Scan() {
		t.Fatalf("scan response: %v", scanner.Err())
	}
	got := scanner.Text()
	if !strings.Contains(got, `"hello"`) {
		t.Errorf("response did not echo prompt; got %q", got)
	}

	// End the run.
	if err := json.NewEncoder(ctlConn).Encode(map[string]string{"action": "end"}); err != nil {
		t.Fatalf("write ctl end: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("runPersistent: %v", err)
	}
}

func TestPersistent_InterruptDoesNotKillProcess(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	// Wait for the fixture to install its SIGINT trap (it emits a
	// "ready" event right after `trap`). Otherwise SIGINT may arrive
	// before bash has parsed the trap line and the default action
	// kills the harness.
	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() {
		t.Fatalf("scan ready: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"ready"`) {
		t.Fatalf("expected ready event, got %q", scanner.Text())
	}

	// Send interrupt, then a follow-up prompt that should still process.
	if err := json.NewEncoder(ctlConn).Encode(map[string]string{"action": "interrupt"}); err != nil {
		t.Fatalf("write ctl: %v", err)
	}
	// Wait for the fixture to emit its "interrupted" event, proving
	// the trap fired and the process is still alive.
	if !scanner.Scan() {
		t.Fatalf("scan interrupted: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"interrupted"`) {
		t.Errorf("expected interrupted event, got %q", scanner.Text())
	}

	if _, err := dataConn.Write([]byte(`"after-interrupt"` + "\n")); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if !scanner.Scan() {
		t.Fatalf("scan response: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"after-interrupt"`) {
		t.Errorf("post-interrupt prompt not echoed; got %q", scanner.Text())
	}

	if err := json.NewEncoder(ctlConn).Encode(map[string]string{"action": "end"}); err != nil {
		t.Fatalf("write ctl end: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("runPersistent: %v", err)
	}
}

func TestPersistent_HarnessCrashSurfaces(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	// `false` exits 1 immediately - simulates an unrecoverable harness crash.
	// Resolved via PATH because the absolute path differs by OS
	// (/bin/false on Linux, /usr/bin/false on macOS).
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skipf("no `false` binary on PATH: %v", err)
	}
	cfg := Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: falseBin,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	runErr := <-errCh
	if runErr == nil || !strings.Contains(runErr.Error(), "crashed") {
		t.Fatalf("want crash error, got %v", runErr)
	}
}

func TestPersistent_CleanExitOnDataEOFIsNotCrash(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	// Drain "ready" event.
	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() {
		t.Fatalf("scan ready: %v", scanner.Err())
	}

	// Close the data UDS write half WITHOUT sending an "end" ctl message.
	// This races stdoutDone against ctlMsgs (which never gets an end). The
	// supervisor must treat this as a clean exit, not a crash, since
	// fake_harness.sh exits 0 on stdin EOF.
	if cw, ok := dataConn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		t.Skip("dataConn doesn't support CloseWrite")
	}

	if err := <-errCh; err != nil {
		t.Errorf("clean exit misclassified as error: %v", err)
	}
}

func TestPersistent_BoundedShutdown(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	// sleep_forever.sh ignores stdin EOF AND SIGTERM, so the supervisor
	// must escalate to SIGKILL within shutdownHard.
	cfg := Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: filepath.Join(testFixturesDir(t), "sleep_forever.sh"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	// Drain the "ready" event before sending end (otherwise stdoutDone
	// fires before the dispatch loop consumes the ctl message and we
	// race on which branch runs).
	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() {
		t.Fatalf("scan ready: %v", scanner.Err())
	}

	if err := json.NewEncoder(ctlConn).Encode(map[string]string{"action": "end"}); err != nil {
		t.Fatalf("write ctl end: %v", err)
	}

	// The supervisor must return within shutdownGentle + shutdownHard
	// (2s + 3s = 5s default) plus a small slack. A naked cmd.Wait()
	// would hang forever and the test ctx would time out at 5s.
	start := time.Now()
	select {
	case err := <-errCh:
		// ExitError is fine (CLI killed by signal); nil means it
		// somehow exited cleanly which would be surprising but ok.
		_ = err
	case <-time.After(7 * time.Second):
		t.Fatalf("supervisor did not bound shutdown within 7s")
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Errorf("shutdown took %v, want <6s", elapsed)
	}
}

func TestPersistent_CrashedEventOnNonZeroExit(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skipf("no `false` binary on PATH: %v", err)
	}
	cfg := Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: falseBin,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer func() { _ = dataConn.Close() }()
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	// The supervisor must emit {"event":"crashed","exit_code":1} on
	// the ctl UDS before runPersistent returns.
	dec := json.NewDecoder(ctlConn)
	var got ctlMessage
	_ = ctlConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode crashed event: %v", err)
	}
	if got.Event != "crashed" {
		t.Errorf("event = %q, want \"crashed\"", got.Event)
	}
	if got.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", got.ExitCode)
	}

	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "crashed") {
		t.Errorf("runPersistent err = %v, want \"crashed\"", err)
	}
}

func TestPersistent_DataReconnect(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() { // ready
		t.Fatalf("scan ready: %v", scanner.Err())
	}

	// Send first prompt, observe response.
	if _, err := dataConn.Write([]byte(`"first"` + "\n")); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if !scanner.Scan() {
		t.Fatalf("scan first response: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"first"`) {
		t.Errorf("first response = %q", scanner.Text())
	}

	// Drop the data UDS (simulating runtime-sidecar restart).
	_ = dataConn.Close()

	// Reconnect.
	dataConn2 := dialEventually(t, dataPath)
	defer func() { _ = dataConn2.Close() }()
	scanner2 := bufio.NewScanner(dataConn2)

	// Second prompt over the reconnected conn must reach the SAME
	// harness CLI process (so it remembers state from the first
	// prompt — for fake_harness this is just "the process is still
	// alive and echoes new input").
	if _, err := dataConn2.Write([]byte(`"second"` + "\n")); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if !scanner2.Scan() {
		t.Fatalf("scan second response: %v", scanner2.Err())
	}
	if !strings.Contains(scanner2.Text(), `"second"`) {
		t.Errorf("second response = %q", scanner2.Text())
	}

	if err := json.NewEncoder(ctlConn).Encode(map[string]string{"action": "end"}); err != nil {
		t.Fatalf("write end: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("runPersistent: %v", err)
	}
}

// shortTempDir returns a per-test temp directory rooted at /tmp.
// Unix domain sockets are capped at ~104 bytes on macOS (sun_path),
// and Go's t.TempDir() under /var/folders/... blows that limit for
// tests with long names. /tmp is short enough on every supported
// platform.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pdk-")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testFixtureHarness(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "testfixtures", "fake_harness.sh")
}

func testFixturesDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "testfixtures")
}

func dialEventually(t *testing.T, path string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.Dial("unix", path)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(testingWriter{t}, "test: ", 0)
}

type testingWriter struct{ t *testing.T }

func (w testingWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }
