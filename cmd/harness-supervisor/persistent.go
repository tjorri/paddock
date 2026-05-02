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
			return cmd.Wait()
		case msg, ok := <-ctlMsgs:
			if !ok {
				// ctl reader exited; treat as end.
				_ = stdin.Close()
				return cmd.Wait()
			}
			switch msg.Action {
			case "interrupt":
				if cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
				}
			case "end":
				_ = stdin.Close()
				<-stdoutDone
				return cmd.Wait()
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
			waitErr := cmd.Wait()
			if waitErr == nil {
				return nil
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
