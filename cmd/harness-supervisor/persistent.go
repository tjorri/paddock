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
	"sync"
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

	dataConns := acceptLoop(ctx, dataLn)
	ctlConns := acceptLoop(ctx, ctlLn)

	// Wait for the first dial of each before spawning the harness CLI.
	dataConn, ok := <-dataConns
	if !ok {
		return ctx.Err()
	}
	ctlConn, ok := <-ctlConns
	if !ok {
		_ = dataConn.Close()
		return ctx.Err()
	}

	cmd := exec.Command(cfg.HarnessBin, cfg.HarnessArgs...)
	cmd.Dir = cfg.WorkDir
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = dataConn.Close()
		_ = ctlConn.Close()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = dataConn.Close()
		_ = ctlConn.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = dataConn.Close()
		_ = ctlConn.Close()
		return fmt.Errorf("start harness: %w", err)
	}

	// dataMu/ctlMu guard the active conns. Reconnect goroutines swap
	// the field when the previous conn drops; the crash-event path
	// reads ctlMu to find the live ctl conn for writeEvent.
	var dataMu sync.Mutex
	currentData := dataConn
	var ctlMu sync.Mutex
	currentCtl := ctlConn

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

	// harness stdout -> data UDS. We can't use io.Copy here because
	// when the data conn is swapped mid-stream, io.Copy would have
	// already consumed bytes from stdout into its internal buffer and
	// then failed to write them to the now-closed old conn. Instead,
	// read into our own buffer and retry the Write against the
	// (re-fetched) current conn until it succeeds.
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				// Write the chunk, retrying against whichever conn is
				// current. A retry happens when the data UDS has been
				// dropped but a new one hasn't been installed yet.
				for written := 0; written < n; {
					dataMu.Lock()
					c := currentData
					dataMu.Unlock()
					w, werr := c.Write(buf[written:n])
					if w > 0 {
						written += w
					}
					if werr == nil {
						continue
					}
					// Write failed (likely closed conn). Wait briefly for
					// the stdin goroutine to install the next conn, then
					// re-fetch and retry the unwritten remainder.
					select {
					case <-ctx.Done():
						return
					case <-time.After(50 * time.Millisecond):
					}
				}
			}
			if rerr != nil {
				// stdout closed → CLI exited; we're done.
				return
			}
		}
	}()

	// ctl UDS -> ctlMsgs across multiple consecutive conns. The
	// current ctl conn is held in currentCtl so the crash-event path
	// can find it for writeEvent.
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
				ctlMu.Lock()
				currentCtl = nc
				ctlMu.Unlock()
				c = nc
				logger.Printf("ctl UDS reconnected")
			case <-ctx.Done():
				return
			}
		}
	}()

	// Make sure the conns get closed on any return path.
	defer func() {
		dataMu.Lock()
		if currentData != nil {
			_ = currentData.Close()
		}
		dataMu.Unlock()
		ctlMu.Lock()
		if currentCtl != nil {
			_ = currentCtl.Close()
		}
		ctlMu.Unlock()
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
