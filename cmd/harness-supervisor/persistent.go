package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

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

// runPersistent owns the harness CLI's lifetime: spawn one process,
// pipe data UDS <-> stdio, dispatch ctl messages, exit cleanly when
// stdin closes (end) or fatally when the CLI exits unexpectedly
// (crash).
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

	dataConn, err := acceptOnce(ctx, dataLn)
	if err != nil {
		return fmt.Errorf("accept data: %w", err)
	}
	defer func() { _ = dataConn.Close() }()
	ctlConn, err := acceptOnce(ctx, ctlLn)
	if err != nil {
		return fmt.Errorf("accept ctl: %w", err)
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

	// data UDS -> harness stdin
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		_, _ = io.Copy(stdin, dataConn)
		_ = stdin.Close()
	}()

	// harness stdout -> data UDS
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		_, _ = io.Copy(dataConn, stdout)
	}()

	// ctl UDS -> ctlMessages channel
	ctlMsgs := make(chan ctlMessage, 4)
	ctlErrCh := make(chan error, 1)
	go func() { ctlErrCh <- readCtl(ctx, ctlConn, ctlMsgs) }()

	// ctl dispatch loop runs until end-of-run or fatal CLI exit.
	for {
		select {
		case <-ctx.Done():
			_ = stdin.Close()
			return waitWithTimeout(cmd, shutdownGentle, shutdownHard)
		case msg, ok := <-ctlMsgs:
			if !ok {
				// ctl reader exited; treat as end.
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
				// Don't block on stdoutDone here: a misbehaving harness
				// that ignores stdin EOF would never close stdout and we'd
				// hang forever, never reaching waitWithTimeout. cmd.Wait()
				// inside waitWithTimeout drains the stdout pipe as part of
				// reaping, and the SIGTERM/SIGKILL escalation guarantees
				// the process dies (and therefore stdout closes) within
				// shutdownGentle+shutdownHard.
				return waitWithTimeout(cmd, shutdownGentle, shutdownHard)
			default:
				logger.Printf("unknown ctl action: %q", msg.Action)
			}
		case <-stdoutDone:
			// Harness's stdout closed. This can happen two ways:
			//   1. Adapter called /end → proxy CloseWrites the data UDS →
			//      supervisor's stdin goroutine sees EOF → closes harness's
			//      stdin → harness reads EOF, exits 0. The "end" ctl message
			//      may still be in flight on ctlMsgs; we treat this as a
			//      clean shutdown regardless of arrival ordering.
			//   2. Harness crashed mid-stream without prompting from us.
			//
			// cmd.Wait() returning nil (exit 0) means (1); a non-nil error
			// (non-zero exit, signal, etc.) means (2).
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
	}
}

// acceptOnce calls ln.Accept once or returns an error if ctx is
// cancelled first.
func acceptOnce(ctx context.Context, ln net.Listener) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		_ = ln.Close()
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			if errors.Is(r.err, net.ErrClosed) {
				return nil, ctx.Err()
			}
			return nil, r.err
		}
		return r.c, nil
	}
}

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
