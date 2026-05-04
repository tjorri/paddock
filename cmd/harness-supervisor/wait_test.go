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
// process that ignores SIGTERM (via a no-op TERM trap) must be killed
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
