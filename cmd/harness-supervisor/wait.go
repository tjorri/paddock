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
