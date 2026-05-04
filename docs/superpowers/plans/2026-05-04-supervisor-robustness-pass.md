# Supervisor robustness pass — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land three deferred follow-ups on `cmd/harness-supervisor/` as a single PR — bounded CLI shutdown (#108), explicit "crashed" ctl events (#106), and an accept-loop for runtime-sidecar reconnection (#111) — so the supervisor degrades gracefully when its peer restarts mid-run and can no longer be hung indefinitely by a misbehaving harness CLI.

**Architecture:**
- **Phase 1 (#108)** introduces a small `waitWithTimeout(cmd, gentle, hard)` helper that wraps `cmd.Wait()` with a SIGTERM-after-gentle / SIGKILL-after-hard escalation against the process group. All four existing `cmd.Wait()` call sites (`persistent.go:84,89,99,114`, `per_prompt.go:195`) switch to it.
- **Phase 2 (#106)** extends the ctl wire shape with an `event` field and adds a supervisor → runtime channel for `{event:"crashed",exit_code:N}` (persistent) and `{event:"prompt-crashed",seq:N,exit_code:N}` (per-prompt). The runtime sidecar starts a ctl-reader goroutine that logs received events; surfacing them as PaddockEvents is left to a follow-up.
- **Phase 3 (#111)** replaces the supervisor's two `acceptOnce` calls with an `acceptLoop` that yields a fresh `net.Conn` on each runtime-side reconnect. The harness CLI's stdin/stdout pipes are owned by the supervisor process and survive across UDS reconnects; mid-prompt connection loss is acceptable degradation (the harness CLI sees a torn stdin, exits non-zero, the new #106 crashed event surfaces it cleanly).

**Tech Stack:** Go 1.24, controller-runtime (unaffected here), `net`-package UDS listeners, `os/exec` for harness CLI lifecycle, `syscall` for `Setpgid` + signal escalation. Pre-commit hook runs `go vet -tags=e2e ./... && golangci-lint run`. Full e2e: `LABELS=interactive FAIL_FAST=1 make test-e2e 2>&1 | tee /tmp/e2e.log`.

---

## File Structure

| File | Status | Responsibility |
|------|--------|---------------|
| `cmd/harness-supervisor/wait.go` | **new** | `waitWithTimeout` helper + signal escalation against pgrp |
| `cmd/harness-supervisor/wait_test.go` | **new** | Unit tests for the helper |
| `cmd/harness-supervisor/control.go` | modify | Extend `ctlMessage` with `Event` + `ExitCode`; add `writeEvent` helper |
| `cmd/harness-supervisor/persistent.go` | modify | Use `waitWithTimeout`, emit `crashed` event, `acceptLoop` |
| `cmd/harness-supervisor/per_prompt.go` | modify | Use `waitWithTimeout`, emit `prompt-crashed` event, `acceptLoop` |
| `cmd/harness-supervisor/listener.go` | modify | Add `acceptLoop` helper alongside existing `acceptOnce` |
| `cmd/harness-supervisor/persistent_test.go` | modify | Add reconnect + bounded-shutdown + crashed-event tests |
| `cmd/harness-supervisor/per_prompt_test.go` | modify | Add reconnect + prompt-crashed-event test |
| `cmd/harness-supervisor/testfixtures/sleep_forever.sh` | **new** | Fake harness that ignores stdin EOF (drives bounded-shutdown test) |
| `cmd/harness-supervisor/testfixtures/crash_on_first_prompt.sh` | **new** | Fake harness that exits 1 after reading one line (drives prompt-crashed test) |
| `internal/runtime/proxy/ctl_reader.go` | **new** | Runtime-side ctl reader goroutine + event log |
| `internal/runtime/proxy/ctl_reader_test.go` | **new** | Unit test for the runtime ctl reader |
| `internal/runtime/proxy/proxy.go` | modify | Start ctl-reader goroutine in `NewServer`; stop on `Close` |

The supervisor changes are intra-package; only the ctl wire shape leaks across the package boundary, and that shape is JSON-versioned by field-presence (an old runtime that doesn't know about `event` simply ignores frames it can't decode as actions, which is what `json.Decoder` does today).

---

## Wire-shape note (read before Phase 2)

Today's ctl traffic is one-way (runtime → supervisor):

```jsonc
// runtime → supervisor (existing)
{"action":"begin-prompt","seq":1}
{"action":"end-prompt","seq":1}
{"action":"interrupt"}
{"action":"end","reason":"..."}
```

After this PR the wire becomes bidirectional. The supervisor never emits `action`, the runtime never emits `event`:

```jsonc
// supervisor → runtime (new)
{"event":"crashed","exit_code":1}                // persistent mode
{"event":"prompt-crashed","seq":1,"exit_code":1} // per-prompt mode
```

Both sides decode into the same `ctlMessage` struct with both `Action` and `Event` fields; the receiver discriminates on which is non-empty.

---

## Phase 1: Bounded CLI shutdown (#108)

### Task 1: `waitWithTimeout` helper

**Files:**
- Create: `cmd/harness-supervisor/wait.go`
- Test: `cmd/harness-supervisor/wait_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/harness-supervisor/wait_test.go`:

```go
package main

import (
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestWaitWithTimeout_ExitsBeforeGentle exercises the happy path: the
// process exits on its own before the gentle deadline, so neither
// SIGTERM nor SIGKILL fires.
func TestWaitWithTimeout_ExitsBeforeGentle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix process groups only")
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := waitWithTimeout(cmd, 500*time.Millisecond, 500*time.Millisecond); err != nil {
		t.Errorf("waitWithTimeout: unexpected err %v", err)
	}
}

// TestWaitWithTimeout_GentleEscalation exercises the SIGTERM path: the
// process ignores stdin and would block forever, so the helper must
// SIGTERM it after the gentle deadline, observe a clean exit-on-signal,
// and return without escalating to SIGKILL.
func TestWaitWithTimeout_GentleEscalation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix process groups only")
	}
	// `sleep 30` exits cleanly on SIGTERM (default disposition is terminate).
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	start := time.Now()
	err := waitWithTimeout(cmd, 100*time.Millisecond, 500*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 400*time.Millisecond {
		t.Errorf("escalation too slow: %v", elapsed)
	}
	// Process exited via signal, so cmd.Wait surfaces *exec.ExitError.
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Errorf("want ExitError, got %v", err)
	}
}

// TestWaitWithTimeout_HardEscalation exercises the SIGKILL path: a
// process that ignores SIGTERM (via `trap '' TERM`) must be killed
// after the hard deadline.
func TestWaitWithTimeout_HardEscalation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix process groups only")
	}
	cmd := exec.Command("/bin/sh", "-c", "trap '' TERM; sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	start := time.Now()
	err := waitWithTimeout(cmd, 100*time.Millisecond, 200*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("SIGKILL escalation too slow: %v", elapsed)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Errorf("want ExitError, got %v", err)
	}
	// Sanity: process is reaped (no defunct).
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() && cmd.ProcessState.Sys().(syscall.WaitStatus).Signaled() == false {
		// not strictly required — the exec.ExitError above is sufficient
		_ = err
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/harness-supervisor/ -run TestWaitWithTimeout -v`
Expected: FAIL with `undefined: waitWithTimeout`.

- [ ] **Step 3: Implement the helper**

Create `cmd/harness-supervisor/wait.go`:

```go
package main

import (
	"os/exec"
	"syscall"
	"time"
)

// waitWithTimeout calls cmd.Wait but escalates to SIGTERM after gentle
// elapses and SIGKILL after hard. Returns whatever cmd.Wait returns
// once the process is reaped.
//
// The signal is sent to the process group (-pid), which only works
// when cmd was started with SysProcAttr{Setpgid: true}. Both supervisor
// modes already do this.
//
// Why this exists: a misbehaving harness CLI that ignores stdin EOF
// (or its own SIGTERM trap) would otherwise hang the supervisor until
// the kubelet's terminationGracePeriodSeconds runs out. The bounded
// escalation guarantees the supervisor is the one in charge of its
// own teardown timing.
func waitWithTimeout(cmd *exec.Cmd, gentle, hard time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-time.After(gentle):
	}

	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}

	select {
	case err := <-done:
		return err
	case <-time.After(hard):
	}

	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return <-done
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/harness-supervisor/ -run TestWaitWithTimeout -v`
Expected: PASS for all three cases.

- [ ] **Step 5: Commit**

```bash
git add cmd/harness-supervisor/wait.go cmd/harness-supervisor/wait_test.go
git commit -m "feat(harness-supervisor): bounded waitWithTimeout helper

Wraps cmd.Wait() with SIGTERM-after-gentle / SIGKILL-after-hard
escalation against the process group. Foundation for #108."
```

---

### Task 2: Add a `sleep_forever.sh` test fixture

**Files:**
- Create: `cmd/harness-supervisor/testfixtures/sleep_forever.sh`

- [ ] **Step 1: Create the fixture**

```bash
cat <<'EOF' > cmd/harness-supervisor/testfixtures/sleep_forever.sh
#!/usr/bin/env bash
# Test fixture: emits a "ready" event then sleeps forever, ignoring
# stdin EOF. Used to exercise the supervisor's bounded-shutdown path:
# closing this CLI's stdin will NOT cause it to exit, so the supervisor
# must escalate via SIGTERM/SIGKILL.
trap '' TERM  # ignore SIGTERM too — force the SIGKILL path
printf '{"type":"ready"}\n'
while true; do sleep 60; done
EOF
chmod +x cmd/harness-supervisor/testfixtures/sleep_forever.sh
```

- [ ] **Step 2: Verify it runs**

Run: `cmd/harness-supervisor/testfixtures/sleep_forever.sh < /dev/null & sleep 0.2; kill -KILL $!; wait $! 2>/dev/null; echo done`
Expected: prints `{"type":"ready"}` then `done` after the SIGKILL.

- [ ] **Step 3: Commit**

```bash
git add cmd/harness-supervisor/testfixtures/sleep_forever.sh
git commit -m "test(harness-supervisor): sleep_forever fixture for bounded-shutdown tests"
```

---

### Task 3: Wire `waitWithTimeout` into `runPersistent`

**Files:**
- Modify: `cmd/harness-supervisor/persistent.go:84,89,99,114`

- [ ] **Step 1: Add a regression test first**

Add to `cmd/harness-supervisor/persistent_test.go` (after `TestPersistent_CleanExitOnDataEOFIsNotCrash`):

```go
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
```

Also add a helper at the bottom of the file (next to `testFixtureHarness`):

```go
func testFixturesDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "testfixtures")
}
```

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./cmd/harness-supervisor/ -run TestPersistent_BoundedShutdown -v -timeout 15s`
Expected: FAIL — the test ctx hits 5s and the supervisor never returns (cmd.Wait blocked).

- [ ] **Step 3: Wire `waitWithTimeout` into `runPersistent`**

In `cmd/harness-supervisor/persistent.go`, add two package-level constants near the top of the file (after the imports):

```go
// shutdownGentle is how long we wait for the harness CLI to exit on
// its own (after stdin close) before sending SIGTERM. shutdownHard is
// the additional grace before escalating to SIGKILL. Defaults size for
// "harness completes a final stream-json frame and exits" while still
// guaranteeing the supervisor itself returns within the kubelet's
// terminationGracePeriodSeconds (Paddock pods default to 30s).
const (
	shutdownGentle = 2 * time.Second
	shutdownHard   = 3 * time.Second
)
```

Add `"time"` to the imports.

Replace the four `cmd.Wait()` call sites:

- Line 84: `return cmd.Wait()` → `return waitWithTimeout(cmd, shutdownGentle, shutdownHard)`
- Line 89: `return cmd.Wait()` → `return waitWithTimeout(cmd, shutdownGentle, shutdownHard)`
- Line 99: `return cmd.Wait()` → `return waitWithTimeout(cmd, shutdownGentle, shutdownHard)`
- Line 114: `waitErr := cmd.Wait()` → `waitErr := waitWithTimeout(cmd, shutdownGentle, shutdownHard)`

- [ ] **Step 4: Run the regression test to verify it passes**

Run: `go test ./cmd/harness-supervisor/ -run TestPersistent_BoundedShutdown -v -timeout 15s`
Expected: PASS within ~5s.

Also re-run the full supervisor test suite to make sure nothing regressed:

Run: `go test ./cmd/harness-supervisor/ -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/harness-supervisor/persistent.go cmd/harness-supervisor/persistent_test.go
git commit -m "feat(harness-supervisor): bounded shutdown in persistent mode

Wraps the four cmd.Wait() call sites in runPersistent with
waitWithTimeout(2s gentle, 3s hard) so a misbehaving harness CLI
cannot hang the supervisor past the kubelet's grace period.

Closes part of #108."
```

---

### Task 4: Wire `waitWithTimeout` into `runPerPrompt`

**Files:**
- Modify: `cmd/harness-supervisor/per_prompt.go:195`

- [ ] **Step 1: Replace the bare `cmd.Wait()` in `endPrompt`**

In `cmd/harness-supervisor/per_prompt.go`, line 195 currently reads:

```go
	if cmd != nil {
		_ = cmd.Wait()
	}
```

Change to:

```go
	if cmd != nil {
		_ = waitWithTimeout(cmd, shutdownGentle, shutdownHard)
	}
```

(Re-uses the constants declared in `persistent.go` since both files are in the same package.)

- [ ] **Step 2: Add a regression test**

Add to `cmd/harness-supervisor/per_prompt_test.go`:

```go
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
```

- [ ] **Step 3: Run the regression test**

Run: `go test ./cmd/harness-supervisor/ -run TestPerPrompt_BoundedShutdown -v -timeout 15s`
Expected: PASS within ~5–6s.

Re-run full suite:

Run: `go test ./cmd/harness-supervisor/ -v`
Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/harness-supervisor/per_prompt.go cmd/harness-supervisor/per_prompt_test.go
git commit -m "feat(harness-supervisor): bounded shutdown in per-prompt mode

Closes #108."
```

---

## Phase 2: Crashed ctl events (#106)

### Task 5: Extend ctl wire shape and add `writeEvent`

**Files:**
- Modify: `cmd/harness-supervisor/control.go`

- [ ] **Step 1: Extend the `ctlMessage` struct**

In `cmd/harness-supervisor/control.go`, replace the existing struct:

```go
// ctlMessage is the wire shape of one newline-delimited JSON ctl frame.
type ctlMessage struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Seq    int32  `json:"seq,omitempty"`
}
```

with the bidirectional shape:

```go
// ctlMessage is the wire shape of one newline-delimited JSON ctl frame.
// The frame is bidirectional:
//
//   - runtime → supervisor: Action is set ("begin-prompt", "end-prompt",
//     "interrupt", "end"); Event is empty.
//   - supervisor → runtime: Event is set ("crashed", "prompt-crashed");
//     Action is empty. ExitCode carries the harness CLI's exit status.
//
// Receivers discriminate by which of Action/Event is non-empty. A frame
// with both empty is malformed; a frame with both set is also malformed
// (we never emit one).
type ctlMessage struct {
	Action   string `json:"action,omitempty"`
	Event    string `json:"event,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Seq      int32  `json:"seq,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}
```

- [ ] **Step 2: Add a write helper**

Append to `cmd/harness-supervisor/control.go`:

```go
// writeEvent serializes a supervisor → runtime ctl event onto c. Used
// by both modes' crash paths so the runtime sidecar can distinguish
// "supervisor reported crashed" from "supervisor exited cleanly via
// /end" (which today look identical from the data-UDS side).
//
// Errors are returned to the caller, which logs and continues — there
// is nothing useful to do if the runtime peer has already gone.
func writeEvent(c net.Conn, msg ctlMessage) error {
	return json.NewEncoder(c).Encode(msg)
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./cmd/harness-supervisor/`
Expected: build succeeds.

Run: `go test ./cmd/harness-supervisor/ -v`
Expected: existing tests still pass (the field rename of `action` → `action,omitempty` is backward-compatible since JSON treats missing as empty).

- [ ] **Step 4: Commit**

```bash
git add cmd/harness-supervisor/control.go
git commit -m "refactor(harness-supervisor): bidirectional ctlMessage + writeEvent helper

Extends the ctl wire shape with Event and ExitCode fields and adds
writeEvent for supervisor→runtime emission. Wire-level half of #106;
emission and runtime-side reading land in following commits."
```

---

### Task 6: Emit `crashed` event in `runPersistent`

**Files:**
- Modify: `cmd/harness-supervisor/persistent.go`
- Modify: `cmd/harness-supervisor/persistent_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/harness-supervisor/persistent_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/harness-supervisor/ -run TestPersistent_CrashedEventOnNonZeroExit -v -timeout 15s`
Expected: FAIL — the read deadline trips because the supervisor never emits anything on ctl.

- [ ] **Step 3: Emit the event in the crash branch**

In `cmd/harness-supervisor/persistent.go`, the crash branch currently reads (lines 103–119):

```go
		case <-stdoutDone:
			// Harness's stdout closed. ...
			waitErr := waitWithTimeout(cmd, shutdownGentle, shutdownHard)
			if waitErr == nil {
				return nil
			}
			return fmt.Errorf("harness crashed: %w", waitErr)
		}
```

Change the crash return to emit a ctl event first:

```go
		case <-stdoutDone:
			// Harness's stdout closed. ...
			waitErr := waitWithTimeout(cmd, shutdownGentle, shutdownHard)
			if waitErr == nil {
				return nil
			}
			if err := writeEvent(ctlConn, ctlMessage{
				Event:    "crashed",
				ExitCode: exitCodeOf(waitErr),
			}); err != nil {
				logger.Printf("write crashed event: %v", err)
			}
			return fmt.Errorf("harness crashed: %w", waitErr)
		}
```

Add a small helper at the bottom of `persistent.go`:

```go
// exitCodeOf extracts the numeric exit code from a cmd.Wait error.
// Returns -1 for signal-killed processes (which wraps via *exec.ExitError
// with ProcessState.Sys() carrying the signal) and 0 for nil — though
// callers only invoke this on a non-nil error.
func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/harness-supervisor/ -run TestPersistent_CrashedEventOnNonZeroExit -v -timeout 15s`
Expected: PASS.

Run full suite to confirm no regressions:

Run: `go test ./cmd/harness-supervisor/ -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/harness-supervisor/persistent.go cmd/harness-supervisor/persistent_test.go
git commit -m "feat(harness-supervisor): emit crashed ctl event on harness exit

Persistent mode now writes {\"event\":\"crashed\",\"exit_code\":N} on
the ctl UDS when the harness CLI exits non-zero, before returning the
wrapped error. Lets the runtime sidecar distinguish supervisor-reported
crash from clean disconnect.

Refs #106."
```

---

### Task 7: Emit `prompt-crashed` event in `runPerPrompt`

**Files:**
- Modify: `cmd/harness-supervisor/per_prompt.go`
- Modify: `cmd/harness-supervisor/per_prompt_test.go`
- Create: `cmd/harness-supervisor/testfixtures/crash_on_first_prompt.sh`

- [ ] **Step 1: Add the crashing test fixture**

```bash
cat <<'EOF' > cmd/harness-supervisor/testfixtures/crash_on_first_prompt.sh
#!/usr/bin/env bash
# Test fixture: emits a ready event, reads ONE stdin line, then exits 1.
# Used to exercise per-prompt-process's prompt-crashed event.
printf '{"type":"ready"}\n'
IFS= read -r line
exit 1
EOF
chmod +x cmd/harness-supervisor/testfixtures/crash_on_first_prompt.sh
```

- [ ] **Step 2: Write the failing test**

Add to `cmd/harness-supervisor/per_prompt_test.go`:

```go
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
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./cmd/harness-supervisor/ -run TestPerPrompt_PromptCrashedEvent -v -timeout 15s`
Expected: FAIL — no `prompt-crashed` event arrives.

- [ ] **Step 4: Plumb seq + ctlConn into `endPrompt`**

The current `endPrompt` doesn't know either the prompt seq or the ctl conn. We need to plumb both.

Edit `cmd/harness-supervisor/per_prompt.go`:

In `runPerPrompt`'s ctl dispatch loop, change:

```go
			case "begin-prompt":
				if err := state.beginPrompt(cfg); err != nil {
					return fmt.Errorf("begin-prompt seq=%d: %w", msg.Seq, err)
				}
			case "end-prompt":
				state.endPrompt()
```

to track the active seq and pass the ctl conn:

```go
			case "begin-prompt":
				if err := state.beginPrompt(cfg, msg.Seq); err != nil {
					return fmt.Errorf("begin-prompt seq=%d: %w", msg.Seq, err)
				}
			case "end-prompt":
				state.endPrompt(ctlConn, logger)
```

Update `promptState` to carry the active seq. Add a field:

```go
type promptState struct {
	dataConn net.Conn

	mu        sync.Mutex
	cond      *sync.Cond
	stdin     io.WriteCloser
	cmd       *exec.Cmd
	doneCh    chan struct{}
	activeSeq int32 // seq of the currently-active prompt; 0 between prompts.

	drainAck chan struct{}
}
```

In `beginPrompt`, take a `seq int32` parameter and assign:

```go
func (s *promptState) beginPrompt(cfg Config, seq int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return errors.New("begin-prompt while another prompt is active")
	}
	// ... existing setup ...
	s.cmd = cmd
	s.stdin = stdin
	s.doneCh = make(chan struct{})
	s.activeSeq = seq

	go func() {
		defer close(s.doneCh)
		_, _ = io.Copy(s.dataConn, stdout)
	}()
	s.cond.Broadcast()
	return nil
}
```

In `endPrompt`, take the ctl conn and a logger, then emit the event after `waitWithTimeout` returns non-nil:

```go
func (s *promptState) endPrompt(ctlConn net.Conn, logger *log.Logger) {
	s.mu.Lock()
	if s.cmd == nil {
		s.mu.Unlock()
		return
	}
	drainAck := make(chan struct{})
	s.drainAck = drainAck
	_ = s.dataConn.SetReadDeadline(time.Now())
	s.mu.Unlock()

	<-drainAck

	s.mu.Lock()
	stdin, cmd, doneCh, seq := s.stdin, s.cmd, s.doneCh, s.activeSeq
	s.stdin = nil
	s.drainAck = nil
	s.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if doneCh != nil {
		<-doneCh
	}
	var waitErr error
	if cmd != nil {
		waitErr = waitWithTimeout(cmd, shutdownGentle, shutdownHard)
	}

	s.mu.Lock()
	s.cmd = nil
	s.doneCh = nil
	s.activeSeq = 0
	s.mu.Unlock()

	if waitErr != nil && ctlConn != nil {
		if err := writeEvent(ctlConn, ctlMessage{
			Event:    "prompt-crashed",
			Seq:      seq,
			ExitCode: exitCodeOf(waitErr),
		}); err != nil {
			logger.Printf("write prompt-crashed event: %v", err)
		}
	}
}
```

`endActivePrompt` becomes:

```go
func (s *promptState) endActivePrompt(ctlConn net.Conn, logger *log.Logger) {
	s.mu.Lock()
	hasActive := s.cmd != nil
	s.mu.Unlock()
	if hasActive {
		s.endPrompt(ctlConn, logger)
	}
}
```

Update its three call sites in `runPerPrompt` (the `<-ctx.Done()` and `"end"` branches):

```go
		case <-ctx.Done():
			state.endActivePrompt(ctlConn, logger)
			return nil
		// ...
		case "end":
			state.endActivePrompt(ctlConn, logger)
			return nil
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./cmd/harness-supervisor/ -run TestPerPrompt_PromptCrashedEvent -v -timeout 15s`
Expected: PASS.

Run full suite:

Run: `go test ./cmd/harness-supervisor/ -v`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/harness-supervisor/per_prompt.go cmd/harness-supervisor/per_prompt_test.go cmd/harness-supervisor/testfixtures/crash_on_first_prompt.sh
git commit -m "feat(harness-supervisor): emit prompt-crashed ctl event in per-prompt mode

When a per-prompt CLI exits non-zero, the supervisor now writes
{\"event\":\"prompt-crashed\",\"seq\":N,\"exit_code\":N} on the ctl UDS
so the broker can surface the failure to the user mid-run.

Closes #106."
```

---

### Task 8: Runtime-side ctl reader

**Files:**
- Create: `internal/runtime/proxy/ctl_reader.go`
- Create: `internal/runtime/proxy/ctl_reader_test.go`
- Modify: `internal/runtime/proxy/proxy.go`

- [ ] **Step 1: Write the failing test**

Create `internal/runtime/proxy/ctl_reader_test.go`:

```go
package proxy

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"
)

// TestRunCtlReader_LogsCrashedEvent exercises the supervisor → runtime
// ctl event path: a {"event":"crashed","exit_code":1} frame written by
// a fake supervisor must be observed by runCtlReader and surfaced to
// the runtime's logger.
func TestRunCtlReader_LogsCrashedEvent(t *testing.T) {
	supervisorEnd, runtimeEnd := net.Pipe()
	defer func() { _ = runtimeEnd.Close() }()

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runCtlReader(ctx, runtimeEnd, logger) }()

	if _, err := io.WriteString(supervisorEnd, `{"event":"crashed","exit_code":1}`+"\n"); err != nil {
		t.Fatalf("write event: %v", err)
	}

	// The reader should log the event within a short window.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "crashed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "crashed") {
		t.Errorf("logger did not record crashed event; log = %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "exit_code=1") {
		t.Errorf("logger did not record exit_code=1; log = %q", logBuf.String())
	}

	_ = supervisorEnd.Close()
	if err := <-done; err != nil {
		t.Errorf("runCtlReader returned %v, want nil after EOF", err)
	}
}

func TestRunCtlReader_LogsPromptCrashedEvent(t *testing.T) {
	supervisorEnd, runtimeEnd := net.Pipe()
	defer func() { _ = runtimeEnd.Close() }()

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runCtlReader(ctx, runtimeEnd, logger) }()

	if _, err := io.WriteString(supervisorEnd, `{"event":"prompt-crashed","seq":3,"exit_code":2}`+"\n"); err != nil {
		t.Fatalf("write event: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "prompt-crashed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "prompt-crashed") {
		t.Errorf("logger did not record prompt-crashed; log = %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "seq=3") {
		t.Errorf("logger did not record seq=3; log = %q", logBuf.String())
	}

	_ = supervisorEnd.Close()
	<-done
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/proxy/ -run TestRunCtlReader -v`
Expected: FAIL with `undefined: runCtlReader`.

- [ ] **Step 3: Implement the reader**

Create `internal/runtime/proxy/ctl_reader.go`:

```go
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

package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
)

// supervisorEvent is the supervisor → runtime half of the ctl wire
// shape (mirror of cmd/harness-supervisor/control.go's ctlMessage with
// only the fields the runtime cares about).
type supervisorEvent struct {
	Event    string `json:"event,omitempty"`
	Seq      int32  `json:"seq,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// runCtlReader decodes supervisor → runtime ctl events from c and logs
// them. Returns nil on graceful EOF, the underlying error otherwise.
//
// v1 logs only; surfacing events as PaddockEvents (Type=Error with
// kind=harness-crashed) is a follow-up. The point of this reader today
// is the wire-level signal — the runtime can no longer mistake a
// supervisor-reported crash for a clean /end disconnect because the
// crashed event lands before the ctl UDS closes.
func runCtlReader(ctx context.Context, c net.Conn, logger *log.Logger) error {
	dec := json.NewDecoder(bufio.NewReader(c))
	for {
		var ev supervisorEvent
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if ev.Event == "" {
			// Frame had no event field — not for us. The supervisor
			// never emits action frames, but a future protocol change
			// might add other fields; ignore unknown shapes.
			continue
		}
		switch ev.Event {
		case "crashed":
			logger.Printf("supervisor reported crashed exit_code=%d", ev.ExitCode)
		case "prompt-crashed":
			logger.Printf("supervisor reported prompt-crashed seq=%d exit_code=%d", ev.Seq, ev.ExitCode)
		default:
			logger.Printf("supervisor reported unknown event %q", ev.Event)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/proxy/ -run TestRunCtlReader -v`
Expected: both tests PASS.

- [ ] **Step 5: Wire the reader into `NewServer`**

In `internal/runtime/proxy/proxy.go`, the `Server` struct holds `ctlConn net.Conn`. Today nothing reads from it. Add a goroutine started by `NewServer`.

Add a logger field to the Server struct, OR reuse the package's `log` package directly. Easier: pass a logger via `Config`. Check if `Config` already has one — if not, use `log.Default()` and document it.

Edit `internal/runtime/proxy/proxy.go`:

After the line that assigns `ctlConn` (around line 137), add a goroutine launch. The cleanest place is right after `NewServer` builds the Server, before returning it. The Server should track the goroutine for clean shutdown.

Add to the Server struct:

```go
	// ctlReaderDone closes when the ctl-reader goroutine exits. Close()
	// blocks on it (with a short timeout) so the goroutine doesn't
	// outlive the Server.
	ctlReaderDone chan struct{}
```

In `NewServer` (after `s := &Server{...}`):

```go
	s.ctlReaderDone = make(chan struct{})
	go func() {
		defer close(s.ctlReaderDone)
		if err := runCtlReader(ctx, s.ctlConn, log.Default()); err != nil {
			log.Printf("ctl reader: %v", err)
		}
	}()
```

Add `"log"` to the imports if not already present.

In `Server.Close`, after closing `ctlConn`, wait briefly for the reader to drain:

```go
	if s.ctlReaderDone != nil {
		select {
		case <-s.ctlReaderDone:
		case <-time.After(500 * time.Millisecond):
		}
		s.ctlReaderDone = nil
	}
```

(Add `"time"` to imports if not already present.)

- [ ] **Step 6: Verify the runtime-side suite still passes**

Run: `go test ./internal/runtime/proxy/ -v`
Expected: all tests pass, including `TestRunCtlReader_*`.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/proxy/ctl_reader.go internal/runtime/proxy/ctl_reader_test.go internal/runtime/proxy/proxy.go
git commit -m "feat(runtime/proxy): read supervisor ctl events

NewServer now starts a ctl-reader goroutine that decodes
supervisor → runtime events ({event:\"crashed\",...} /
{event:\"prompt-crashed\",...}) and logs them. Server.Close waits up
to 500ms for the reader to drain before returning.

v1 logs only; surfacing events as PaddockEvents is a follow-up. The
point today is that a supervisor-reported crash is no longer
indistinguishable from a clean /end disconnect on the runtime side.

Closes #106."
```

---

## Phase 3: Accept-loop for runtime reconnection (#111)

### Task 9: `acceptLoop` helper

**Files:**
- Modify: `cmd/harness-supervisor/listener.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/harness-supervisor/main_test.go` (the existing test file for shared helpers; if you'd rather create a new `listener_test.go` that's also fine):

```go
func TestAcceptLoop_YieldsConnsAndStopsOnContextCancel(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "loop.sock")
	ln, err := listenUnix(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conns := acceptLoop(ctx, ln)

	// First dial.
	c1, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer func() { _ = c1.Close() }()

	got1 := <-conns
	if got1 == nil {
		t.Fatalf("expected first conn, got nil")
	}
	_ = got1.Close()

	// Second dial — proves it's a loop, not a one-shot.
	c2, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer func() { _ = c2.Close() }()
	got2 := <-conns
	if got2 == nil {
		t.Fatalf("expected second conn, got nil")
	}
	_ = got2.Close()

	// Context cancel closes the channel.
	cancel()
	if _, ok := <-conns; ok {
		t.Errorf("conns channel still open after cancel")
	}
}
```

You'll also need `bufio`, `context`, `filepath`, `net`, `testing` imports — add them as needed.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/harness-supervisor/ -run TestAcceptLoop -v`
Expected: FAIL with `undefined: acceptLoop`.

- [ ] **Step 3: Implement `acceptLoop`**

Append to `cmd/harness-supervisor/listener.go`:

```go
// acceptLoop accepts connections on ln in a goroutine and yields each
// on the returned channel until ctx is cancelled or the listener is
// closed. The channel is closed when the loop exits.
//
// Used in place of acceptOnce to give each mode resilience against
// runtime-sidecar restarts: when the runtime-side conn drops, the
// supervisor's pipe goroutines see EOF and the dispatch loop pulls
// the next conn from the channel without tearing down the harness CLI.
//
// Mid-prompt connection loss is acceptable degradation: the harness
// CLI's stdin pipe survives across reconnects (the supervisor owns
// it), so the next prompt body lands in the same CLI's stdin. If a
// prompt was half-written when the conn dropped, the harness CLI sees
// a torn JSON frame and exits non-zero — surfaced via the #106
// crashed event.
func acceptLoop(ctx context.Context, ln net.Listener) <-chan net.Conn {
	out := make(chan net.Conn)
	go func() {
		defer close(out)
		for {
			c, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
					return
				}
				// Transient accept error — keep looping. (net.Listener's
				// Accept returns net.ErrClosed when the listener has been
				// closed; everything else here is unusual.)
				continue
			}
			select {
			case <-ctx.Done():
				_ = c.Close()
				return
			case out <- c:
			}
		}
	}()
	return out
}
```

Add `"context"` to the imports if not already present.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/harness-supervisor/ -run TestAcceptLoop -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/harness-supervisor/listener.go cmd/harness-supervisor/main_test.go
git commit -m "feat(harness-supervisor): acceptLoop helper for reconnect resilience

Replaces the one-shot acceptOnce pattern with a channel-yielding loop
that survives runtime-sidecar restarts. Foundation for #111."
```

---

### Task 10: Refactor `runPersistent` to use `acceptLoop`

**Files:**
- Modify: `cmd/harness-supervisor/persistent.go`
- Modify: `cmd/harness-supervisor/persistent_test.go`

- [ ] **Step 1: Write the failing reconnect test**

Add to `cmd/harness-supervisor/persistent_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/harness-supervisor/ -run TestPersistent_DataReconnect -v -timeout 15s`
Expected: FAIL — the second dial succeeds (the listener is still open) but the supervisor's pipe goroutines have exited on EOF and never re-attach to the new conn, so `"second"` never reaches the harness CLI's stdin.

- [ ] **Step 3: Refactor `runPersistent` to use `acceptLoop`**

Open `cmd/harness-supervisor/persistent.go` and rewrite it. Key changes:

1. Use `acceptLoop` instead of `acceptOnce` for both data and ctl listeners.
2. The data-pipe goroutines (`stdin`/`stdout` copy) become inner loops keyed on the current `dataConn`. When the current data conn drops (EOF), the inner loop pulls the next conn from the data channel and re-attaches the pipes.
3. The ctl reader becomes the same shape.
4. Drop the dead `ctlErrCh` (#107) — the reader's exit is signalled via `ctlMsgs` channel close.

Replace the body of `runPersistent` with:

```go
func runPersistent(ctx context.Context, logger *log.Logger, cfg Config) error {
	dataLn, err := listenUnix(cfg.DataSocket)
	if err != nil {
		return err
	}
	defer func() { _ = dataLn.Close() }()
	ctlLn, err := listenUnix(cfg.CtlSocket)
	if err != nil {
		return err
	}
	defer func() { _ = ctlLn.Close() }()

	dataConns := acceptLoop(ctx, dataLn)
	ctlConns := acceptLoop(ctx, ctlLn)

	// Wait for the first dial of each before spawning the harness CLI.
	dataConn, ok := <-dataConns
	if !ok {
		return ctx.Err()
	}
	defer func() { _ = dataConn.Close() }()
	ctlConn, ok := <-ctlConns
	if !ok {
		return ctx.Err()
	}
	defer func() { _ = ctlConn.Close() }()

	cmd := exec.Command(cfg.HarnessBin, cfg.HarnessArgs...)
	cmd.Dir = cfg.WorkDir
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start harness: %w", err)
	}

	// dataState holds the current data conn; protected by dataMu so
	// the pipe goroutines can swap it on reconnect.
	var dataMu sync.Mutex
	currentData := dataConn

	// data UDS -> harness stdin: when the current conn drops, pull
	// the next from dataConns and resume.
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		defer func() { _ = stdin.Close() }()
		for {
			dataMu.Lock()
			c := currentData
			dataMu.Unlock()
			_, _ = io.Copy(stdin, c)
			// Current conn drained or errored. Try for a new one.
			select {
			case nc, ok := <-dataConns:
				if !ok {
					return
				}
				dataMu.Lock()
				_ = currentData.Close()
				currentData = nc
				dataMu.Unlock()
				logger.Printf("data UDS reconnected")
			case <-ctx.Done():
				return
			}
		}
	}()

	// harness stdout -> data UDS: same loop shape.
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		// stdout.Close happens when cmd exits.
		for {
			dataMu.Lock()
			c := currentData
			dataMu.Unlock()
			_, _ = io.Copy(c, stdout)
			// stdout closed → CLI exited; we're done. (If the data conn
			// dropped, io.Copy returns and we loop, but the next iter's
			// stdout read will EOF immediately if cmd has exited.)
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
				// Brief pause before retrying — gives the stdin goroutine
				// time to swap currentData.
			}
		}
	}()

	// ctl UDS -> ctlMsgs: same reconnect shape; only the current conn
	// is read.
	ctlMsgs := make(chan ctlMessage, 4)
	go func() {
		defer close(ctlMsgs)
		c := ctlConn
		for {
			if err := readCtlInto(ctx, c, ctlMsgs); err != nil {
				logger.Printf("ctl read: %v", err)
			}
			select {
			case nc, ok := <-ctlConns:
				if !ok {
					return
				}
				_ = c.Close()
				c = nc
				logger.Printf("ctl UDS reconnected")
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = stdin.Close()
			return waitWithTimeout(cmd, shutdownGentle, shutdownHard)
		case msg, ok := <-ctlMsgs:
			if !ok {
				_ = stdin.Close()
				return waitWithTimeout(cmd, shutdownGentle, shutdownHard)
			}
			switch msg.Action {
			case "interrupt":
				if cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
				}
			case "end":
				_ = stdin.Close()
				<-stdoutDone
				return waitWithTimeout(cmd, shutdownGentle, shutdownHard)
			default:
				logger.Printf("unknown ctl action: %q", msg.Action)
			}
		case <-stdoutDone:
			waitErr := waitWithTimeout(cmd, shutdownGentle, shutdownHard)
			if waitErr == nil {
				return nil
			}
			dataMu.Lock()
			c := currentData
			dataMu.Unlock()
			if err := writeEvent(c, ctlMessage{
				Event:    "crashed",
				ExitCode: exitCodeOf(waitErr),
			}); err != nil {
				logger.Printf("write crashed event: %v", err)
			}
			return fmt.Errorf("harness crashed: %w", waitErr)
		}
	}
}
```

Note: the `crashed` event needs to be emitted on the **ctl** conn, not the data conn. Replace the `c := currentData` block in the crash branch with the ctl conn equivalent. Because the ctl conn may also have rotated, track it the same way:

```go
	var ctlMu sync.Mutex
	currentCtl := ctlConn
```

And in the ctl reconnect goroutine, swap `currentCtl` under `ctlMu` when reconnecting. Then the crash branch becomes:

```go
		case <-stdoutDone:
			waitErr := waitWithTimeout(cmd, shutdownGentle, shutdownHard)
			if waitErr == nil {
				return nil
			}
			ctlMu.Lock()
			cc := currentCtl
			ctlMu.Unlock()
			if err := writeEvent(cc, ctlMessage{
				Event:    "crashed",
				ExitCode: exitCodeOf(waitErr),
			}); err != nil {
				logger.Printf("write crashed event: %v", err)
			}
			return fmt.Errorf("harness crashed: %w", waitErr)
		}
```

Add a small helper to `control.go` (factored out of the existing `readCtl`):

```go
// readCtlInto decodes ctl frames from c onto out until c errors or
// ctx is cancelled. Unlike readCtl, it does NOT close out — the caller
// owns out across multiple consecutive conns.
func readCtlInto(ctx context.Context, c net.Conn, out chan<- ctlMessage) error {
	dec := json.NewDecoder(bufio.NewReader(c))
	for {
		var msg ctlMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case out <- msg:
		}
	}
}
```

(The original `readCtl` stays for any callers that still want the close-on-EOF semantics; but per_prompt.go also needs to be updated in Task 11.)

Also add `"sync"` to `persistent.go`'s imports.

- [ ] **Step 4: Run the reconnect test**

Run: `go test ./cmd/harness-supervisor/ -run TestPersistent_DataReconnect -v -timeout 15s`
Expected: PASS.

Run the full supervisor suite:

Run: `go test ./cmd/harness-supervisor/ -v -timeout 30s`
Expected: all tests pass. Pay special attention to `TestPersistent_HarnessCrashSurfaces`, `TestPersistent_CleanExitOnDataEOFIsNotCrash`, `TestPersistent_CrashedEventOnNonZeroExit`, `TestPersistent_BoundedShutdown` — they all depend on subtle ordering of stdout-close vs ctl-end.

- [ ] **Step 5: Commit**

```bash
git add cmd/harness-supervisor/persistent.go cmd/harness-supervisor/persistent_test.go cmd/harness-supervisor/control.go
git commit -m "feat(harness-supervisor): persistent-mode accept-loop for runtime reconnect

runPersistent now accepts data and ctl conns in a loop, swapping the
active conn under a mutex when the runtime-side dial drops and
reconnects. The harness CLI's stdin/stdout pipes survive across
reconnects (the supervisor owns them).

Also drops the dead ctlErrCh channel (closes #107).

Closes part of #111."
```

---

### Task 11: Refactor `runPerPrompt` to use `acceptLoop`

**Files:**
- Modify: `cmd/harness-supervisor/per_prompt.go`
- Modify: `cmd/harness-supervisor/per_prompt_test.go`

- [ ] **Step 1: Write the failing reconnect test**

Add to `cmd/harness-supervisor/per_prompt_test.go`:

```go
func TestPerPrompt_DataReconnectBetweenPrompts(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:       "per-prompt-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPerPrompt(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	ctlConn := dialEventually(t, ctlPath)
	defer func() { _ = ctlConn.Close() }()

	enc := json.NewEncoder(ctlConn)
	scanner := bufio.NewScanner(dataConn)

	// First prompt cycle.
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 1}); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() { // ready
		t.Fatalf("scan ready 1: %v", scanner.Err())
	}
	if _, err := dataConn.Write([]byte(`"first"` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"first"`) {
			break
		}
	}

	// Drop data UDS between prompts.
	_ = dataConn.Close()

	// Second prompt over the reconnected conn.
	dataConn2 := dialEventually(t, dataPath)
	defer func() { _ = dataConn2.Close() }()
	scanner2 := bufio.NewScanner(dataConn2)

	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 2}); err != nil {
		t.Fatal(err)
	}
	if !scanner2.Scan() { // ready of second CLI
		t.Fatalf("scan ready 2: %v", scanner2.Err())
	}
	if _, err := dataConn2.Write([]byte(`"second"` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}
	for scanner2.Scan() {
		if strings.Contains(scanner2.Text(), `"second"`) {
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/harness-supervisor/ -run TestPerPrompt_DataReconnect -v -timeout 15s`
Expected: FAIL — the second `begin-prompt` arrives but the data-UDS reader exited on first conn EOF, so `"second"` never reaches the new CLI's stdin.

- [ ] **Step 3: Refactor `runPerPrompt`**

The `promptState.dataConn` field becomes a function that returns the current conn (or a mu-guarded field that's swapped on reconnect). The `copyFromDataUDS` reader becomes loopy across data conns.

Edit `cmd/harness-supervisor/per_prompt.go`:

Change `promptState.dataConn` to be a `*net.Conn`-style mutable field plus a way to swap it:

```go
type promptState struct {
	mu       sync.Mutex
	cond     *sync.Cond
	dataConn net.Conn // swapped on reconnect; reads under mu

	stdin     io.WriteCloser
	cmd       *exec.Cmd
	doneCh    chan struct{}
	activeSeq int32

	drainAck chan struct{}
}

// swapDataConn replaces the active data conn (used by the accept-loop
// when the previous conn dropped). Wakes the reader so it picks up
// the new conn instead of looping on a dead one.
func (s *promptState) swapDataConn(c net.Conn) {
	s.mu.Lock()
	old := s.dataConn
	s.dataConn = c
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	s.cond.Broadcast()
}

func (s *promptState) currentDataConn() net.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataConn
}
```

`copyFromDataUDS` becomes loopy: when its current conn errors, it parks on the cond until `swapDataConn` wakes it.

```go
func (s *promptState) copyFromDataUDS(ctx context.Context) {
	buf := make([]byte, 4096)
	for {
		c := s.currentDataConn()
		if c == nil {
			s.mu.Lock()
			for s.dataConn == nil {
				s.cond.Wait()
				if ctx.Err() != nil {
					s.mu.Unlock()
					return
				}
			}
			s.mu.Unlock()
			continue
		}
		n, err := c.Read(buf)
		if n > 0 {
			s.writeToStdin(buf[:n])
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if s.runDrain(buf) {
					continue
				}
			}
			// Conn-level error (peer closed, ctx cancelled). Release any
			// pending drain, then drop the conn so swapDataConn can
			// install a new one.
			s.releaseDrain()
			s.mu.Lock()
			if s.dataConn == c {
				s.dataConn = nil
			}
			s.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
		}
	}
}
```

Update `newPromptState`'s signature:

```go
func newPromptState() *promptState {
	s := &promptState{}
	s.cond = sync.NewCond(&s.mu)
	return s
}
```

In `runPerPrompt`, replace the `acceptOnce` block + state setup with `acceptLoop` + reconnect goroutines:

```go
func runPerPrompt(ctx context.Context, logger *log.Logger, cfg Config) error {
	dataLn, err := listenUnix(cfg.DataSocket)
	if err != nil {
		return err
	}
	defer func() { _ = dataLn.Close() }()
	ctlLn, err := listenUnix(cfg.CtlSocket)
	if err != nil {
		return err
	}
	defer func() { _ = ctlLn.Close() }()

	dataConns := acceptLoop(ctx, dataLn)
	ctlConns := acceptLoop(ctx, ctlLn)

	state := newPromptState()
	go state.copyFromDataUDS(ctx)

	// Feed initial data conn + reconnects into state.
	go func() {
		for {
			c, ok := <-dataConns
			if !ok {
				return
			}
			state.swapDataConn(c)
			logger.Printf("data UDS connected")
		}
	}()

	// ctl reader pumps frames into ctlMsgs across multiple conns; the
	// current conn is held in currentCtl so the prompt-crashed write
	// path can find it.
	var ctlMu sync.Mutex
	var currentCtl net.Conn

	ctlMsgs := make(chan ctlMessage, 4)
	go func() {
		defer close(ctlMsgs)
		for {
			c, ok := <-ctlConns
			if !ok {
				return
			}
			ctlMu.Lock()
			currentCtl = c
			ctlMu.Unlock()
			if err := readCtlInto(ctx, c, ctlMsgs); err != nil {
				logger.Printf("ctl read: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()

	getCtl := func() net.Conn {
		ctlMu.Lock()
		defer ctlMu.Unlock()
		return currentCtl
	}

	for {
		select {
		case <-ctx.Done():
			state.endActivePrompt(getCtl(), logger)
			return nil
		case msg, ok := <-ctlMsgs:
			if !ok {
				return nil
			}
			switch msg.Action {
			case "begin-prompt":
				if err := state.beginPrompt(cfg, msg.Seq); err != nil {
					return fmt.Errorf("begin-prompt seq=%d: %w", msg.Seq, err)
				}
			case "end-prompt":
				state.endPrompt(getCtl(), logger)
			case "interrupt":
				state.interrupt()
			case "end":
				state.endActivePrompt(getCtl(), logger)
				return nil
			default:
				logger.Printf("unknown ctl action: %q", msg.Action)
			}
		}
	}
}
```

Add `"context"`, `"sync"` to per_prompt.go's imports if not already present. Remove the `dataConn` parameter from `newPromptState`.

- [ ] **Step 4: Run the reconnect test**

Run: `go test ./cmd/harness-supervisor/ -run TestPerPrompt_DataReconnect -v -timeout 15s`
Expected: PASS.

Run the full supervisor suite:

Run: `go test ./cmd/harness-supervisor/ -v -timeout 30s`
Expected: all tests pass. Particular care for `TestPerPrompt_TwoPromptsTwoProcesses`, `TestPerPrompt_InterruptKillsCurrentProcess`, `TestPerPrompt_PromptCrashedEvent`, `TestPerPrompt_BoundedShutdown` — they all depend on the per-prompt drain handshake, which the refactor must not break.

- [ ] **Step 5: Commit**

```bash
git add cmd/harness-supervisor/per_prompt.go cmd/harness-supervisor/per_prompt_test.go
git commit -m "feat(harness-supervisor): per-prompt-mode accept-loop for runtime reconnect

runPerPrompt's promptState now holds the data conn behind a mutex and
exposes swapDataConn for the accept-loop goroutine. copyFromDataUDS
parks on the cond when the conn is nil and wakes when a reconnect
arrives. Mid-prompt connection loss surfaces as a torn stdin → CLI
exit → prompt-crashed event (already wired in #106).

Closes #111."
```

---

## Phase 4: Integration

### Task 12: Lint + full e2e

**Files:** none (verification only)

- [ ] **Step 1: Run linters**

Run: `golangci-lint run ./cmd/harness-supervisor/... ./internal/runtime/proxy/...`
Expected: clean.

If anything fires (typically `noctx` on the new accept goroutines or `errcheck` on the new writeEvent paths), check `.golangci.yml`'s exclusions — the `cmd/harness-supervisor/.*` block already exempts `noctx` for `net.Listen`. If a new pattern fires, prefer fixing the code over adding an exclusion.

- [ ] **Step 2: Run vet with the e2e build tag**

Run: `go vet -tags=e2e ./...`
Expected: clean.

- [ ] **Step 3: Run the full unit suite**

Run: `go test ./...`
Expected: all packages PASS. Pay attention to `cmd/harness-supervisor/` and `internal/runtime/proxy/`.

- [ ] **Step 4: Run the interactive e2e label**

Run: `LABELS=interactive FAIL_FAST=1 make test-e2e 2>&1 | tee /tmp/e2e.log`
Expected: 6/6 PASS within ~5–6 minutes.

If a spec fails, search `/tmp/e2e.log` for the spec name and the surrounding `kubectl logs` / `kubectl describe` dump that the suite emits on failure. The most likely culprit if the refactor breaks something is the persistent-mode round-trip spec — the `acceptLoop` change altered how stdout-done races against ctl-end.

- [ ] **Step 5: Final commit if any cleanup landed**

If steps 1–4 surfaced minor fixes (lint complaints, test flakes), commit them as a separate `chore` or `test` commit so the feature commits stay readable.

---

### Task 13: Push and open PR

**Files:** none

- [ ] **Step 1: Push the branch**

Run: `git push -u origin feature/supervisor-robustness`
Expected: branch published.

- [ ] **Step 2: Open the PR**

Run:

```bash
gh pr create --title "feat(harness-supervisor): bounded shutdown, crashed events, accept-loop" --body "$(cat <<'EOF'
## Summary

Bundles three deferred follow-ups on `cmd/harness-supervisor/` into a single PR:

- **#108** — bounded `cmd.Wait()` via a new `waitWithTimeout(gentle, hard)` helper that escalates SIGTERM → SIGKILL against the harness CLI's process group. A misbehaving CLI can no longer hang the supervisor past `terminationGracePeriodSeconds`.
- **#106** — supervisor → runtime ctl wire shape extended with `event` and `exit_code` fields. Persistent mode emits `{"event":"crashed","exit_code":N}` on harness crash; per-prompt mode emits `{"event":"prompt-crashed","seq":N,"exit_code":N}`. Runtime sidecar runs a ctl-reader goroutine that logs received events. Fixes the previous behaviour where a supervisor-reported crash and a clean `/end` disconnect were indistinguishable on the runtime side.
- **#111** — replaces the supervisor's `acceptOnce` calls with an `acceptLoop` that yields on a channel. Both modes survive runtime-sidecar restarts: the harness CLI's stdin/stdout pipes are owned by the supervisor process and survive across UDS reconnects. Mid-prompt connection loss is acceptable degradation — the harness CLI sees a torn stdin frame and exits non-zero, surfaced cleanly via the new #106 crashed event.

Also drops the dead `ctlErrCh` channel from #107 as a natural fall-out of the #111 refactor.

## Test plan

- [x] `go test ./cmd/harness-supervisor/ -v` (new tests: bounded shutdown × 2 modes, crashed event × 2 modes, reconnect × 2 modes, accept-loop helper)
- [x] `go test ./internal/runtime/proxy/ -v` (new tests: ctl reader event logging × 2 events)
- [x] `golangci-lint run` clean
- [x] `LABELS=interactive FAIL_FAST=1 make test-e2e` 6/6 PASS

## Out of scope

- Surfacing the supervisor's crashed events as `PaddockEvent` (Type=Error, kind=harness-crashed). The runtime currently only logs them; structured surfacing is left to a follow-up.
- The remaining `harness-supervisor:` follow-ups (#107 partially, #108 partially via timeouts, #109 drain-handshake hardening, #110 event-synced wait test cleanup) — the rest of these will be picked up in a separate hygiene pass.
EOF
)"
```

Expected: PR URL printed. The PR base will be `main`.

---

## Self-Review

**Spec coverage:**

- #108 (bounded shutdown): Tasks 1, 3, 4 — covered.
- #106 (crashed events): Tasks 5, 6, 7, 8 — covered (supervisor write + runtime read + persistent + per-prompt).
- #111 (accept-loop): Tasks 9, 10, 11 — covered (helper + persistent + per-prompt).
- Drop dead `ctlErrCh` (#107): Task 10 calls it out as a fall-out.

**Placeholder scan:** All steps contain actual code or exact commands. No "TBD" / "implement later" / "similar to Task N". The `crashed`-event branch in Task 10 references `currentCtl` whose initialization is shown immediately above.

**Type consistency:**

- `ctlMessage` shape (Action/Event/Reason/Seq/ExitCode) consistent across Tasks 5, 6, 7, 8.
- `promptState` field changes (drop param `dataConn`, add `swapDataConn`/`currentDataConn`/`activeSeq`) consistent across Tasks 7, 11.
- `endPrompt(ctlConn, logger)` signature consistent in Tasks 7, 11.
- `acceptLoop(ctx, ln) <-chan net.Conn` consistent in Tasks 9, 10, 11.
- `waitWithTimeout(cmd, gentle, hard) error` consistent in Tasks 1, 3, 4, 7, 10.
- `shutdownGentle` / `shutdownHard` constants declared in Task 3, reused in Tasks 4, 7, 10, 11.
- `exitCodeOf(err) int` declared in Task 6, reused in Task 7.
- `writeEvent(c, msg) error` declared in Task 5, used in Tasks 6, 7, 10.
- `readCtlInto(ctx, c, out) error` declared in Task 10, used in Task 11.
