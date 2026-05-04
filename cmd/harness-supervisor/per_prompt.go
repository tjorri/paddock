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

// runPerPrompt loops awaiting begin-prompt / end-prompt ctl pairs.
// Each begin-prompt spawns a fresh harness CLI; data UDS bytes are
// piped into its stdin until end-prompt; CLI stdout is mirrored back
// to data UDS until the CLI exits; loop awaits the next begin-prompt.
//
// Concurrent prompts on the same run are prevented upstream by the
// broker's CurrentTurnSeq guard, so the data-UDS reader's
// active-pipe synchronization is one-prompt-at-a-time.
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

	state := newPromptState(dataConn)

	// data UDS reader: pipe bytes to whichever stdin pipe is current.
	go state.copyFromDataUDS()

	// ctl reader feeds messages one at a time.
	ctlMsgs := make(chan ctlMessage, 4)
	go func() { _ = readCtl(ctx, ctlConn, ctlMsgs) }()

	for {
		select {
		case <-ctx.Done():
			state.endActivePrompt()
			return nil
		case msg, ok := <-ctlMsgs:
			if !ok {
				return nil
			}
			switch msg.Action {
			case "begin-prompt":
				if err := state.beginPrompt(cfg); err != nil {
					return fmt.Errorf("begin-prompt seq=%d: %w", msg.Seq, err)
				}
			case "end-prompt":
				state.endPrompt()
			case "interrupt":
				state.interrupt()
			case "end":
				state.endActivePrompt()
				return nil
			default:
				logger.Printf("unknown ctl action: %q", msg.Action)
			}
		}
	}
}

// promptState owns the lifecycle of the currently-active per-prompt
// CLI: stdin pipe (writable by the data-UDS reader), stdout drain
// goroutine, process handle. Methods are mutex-guarded.
//
// dataConn is the adapter↔supervisor data UDS. We hold it as a
// net.Conn so endPrompt can call SetReadDeadline on it to flush the
// data reader through a drain handshake before closing stdin.
type promptState struct {
	dataConn net.Conn

	mu     sync.Mutex
	cond   *sync.Cond
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	doneCh chan struct{}

	// drainAck, when non-nil, signals an end-prompt drain handshake
	// is in flight: endPrompt has requested the data reader to drain
	// any kernel-buffered bytes; the reader closes drainAck when the
	// drain is complete.
	drainAck chan struct{}
}

func newPromptState(dataConn net.Conn) *promptState {
	s := &promptState{dataConn: dataConn}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// beginPrompt spawns a fresh CLI and wires its pipes.
func (s *promptState) beginPrompt(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return errors.New("begin-prompt while another prompt is active")
	}

	cmd := exec.Command(cfg.HarnessBin, cfg.HarnessArgs...)
	cmd.Dir = cfg.WorkDir
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}

	s.cmd = cmd
	s.stdin = stdin
	s.doneCh = make(chan struct{})

	// stdout -> data UDS, exits when CLI closes stdout (typically on exit).
	go func() {
		defer close(s.doneCh)
		_, _ = io.Copy(s.dataConn, stdout)
	}()

	// Wake the data-UDS reader if it's parked waiting for a stdin.
	s.cond.Broadcast()
	return nil
}

// endPrompt synchronizes with the data reader to flush any in-flight
// data-UDS bytes into stdin, then closes stdin (signals EOF), waits
// for stdout to drain and the process to exit, and resets state.
//
// The drain handshake is essential: the broker's per-prompt protocol
// is "begin-prompt (ctl) → body bytes (data) → end-prompt (ctl)",
// but the data and ctl UDSes have no cross-socket ordering. Without
// this, a body write that landed in the data-UDS kernel buffer just
// before end-prompt can be dropped if the reader goroutine is
// scheduled after stdin is closed.
func (s *promptState) endPrompt() {
	s.mu.Lock()
	if s.cmd == nil {
		s.mu.Unlock()
		return
	}
	drainAck := make(chan struct{})
	s.drainAck = drainAck
	// Trip any in-flight Read so the reader observes drainAck quickly.
	// The reader catches the timeout error, runs the drain, and clears
	// the deadline before resuming.
	_ = s.dataConn.SetReadDeadline(time.Now())
	s.mu.Unlock()

	<-drainAck

	s.mu.Lock()
	stdin, cmd, doneCh := s.stdin, s.cmd, s.doneCh
	s.stdin = nil
	s.drainAck = nil
	s.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil {
		// Bound the shutdown: a misbehaving CLI that ignores stdin EOF
		// (and its own SIGTERM trap) would otherwise hang us forever.
		// Wait first so the SIGTERM/SIGKILL escalation can run; that
		// guarantees stdout closes and the doneCh goroutine exits.
		_ = waitWithTimeout(cmd, shutdownGentle, shutdownHard)
	}
	if doneCh != nil {
		// Safe to block here now: the process is dead, so its stdout
		// pipe is closed and the io.Copy goroutine has returned (or is
		// about to). Reading doneCh after Wait avoids losing trailing
		// harness output that landed in the pipe before SIGKILL.
		<-doneCh
	}

	s.mu.Lock()
	s.cmd = nil
	s.doneCh = nil
	s.mu.Unlock()
}

// endActivePrompt is endPrompt without distinguishing "no active prompt".
func (s *promptState) endActivePrompt() {
	s.mu.Lock()
	hasActive := s.cmd != nil
	s.mu.Unlock()
	if hasActive {
		s.endPrompt()
	}
}

// interrupt sends SIGINT to the active CLI's process group, if any.
func (s *promptState) interrupt() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

// copyFromDataUDS is the persistent reader for the data UDS. While a
// prompt is active (stdin != nil), bytes pump straight into stdin.
// Between prompts, the goroutine parks on s.cond. When endPrompt
// requests a drain (sets s.drainAck and trips a read deadline), the
// reader collects any remaining kernel-buffered bytes into stdin and
// closes drainAck to release endPrompt.
func (s *promptState) copyFromDataUDS() {
	buf := make([]byte, 4096)
	for {
		n, err := s.dataConn.Read(buf)
		if n > 0 {
			s.writeToStdin(buf[:n])
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// Tripped by endPrompt's SetReadDeadline. Run the
				// drain handshake, then resume blocking reads.
				if s.runDrain(buf) {
					continue
				}
			}
			// Real I/O error (peer closed, ctx cancelled, etc.):
			// release any pending drain so endPrompt isn't stuck,
			// then exit the goroutine.
			s.releaseDrain()
			return
		}
	}
}

// writeToStdin pumps bytes into the active stdin, parking if no
// prompt is active. Drops bytes silently if a write races with stdin
// closure (end-prompt).
func (s *promptState) writeToStdin(p []byte) {
	s.mu.Lock()
	for s.stdin == nil && s.drainAck == nil {
		s.cond.Wait()
	}
	w := s.stdin
	s.mu.Unlock()
	if w == nil {
		return
	}
	if _, err := w.Write(p); err != nil {
		_ = err // stdin closed mid-write; drop.
	}
}

// runDrain handles a single drain handshake: reads any remaining
// kernel-buffered bytes into stdin (non-blocking via short deadline),
// clears the deadline, then closes drainAck. Returns true iff a
// drain was actually pending.
func (s *promptState) runDrain(buf []byte) bool {
	s.mu.Lock()
	drainAck := s.drainAck
	stdin := s.stdin
	s.mu.Unlock()
	if drainAck == nil {
		return false
	}

	for {
		_ = s.dataConn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		n, err := s.dataConn.Read(buf)
		if n > 0 && stdin != nil {
			if _, werr := stdin.Write(buf[:n]); werr != nil {
				_ = werr
			}
		}
		if err != nil {
			break
		}
	}
	_ = s.dataConn.SetReadDeadline(time.Time{})

	close(drainAck)
	return true
}

// releaseDrain unblocks endPrompt when the data UDS has gone away
// (real read error). Without this, endPrompt would block on
// <-drainAck during shutdown.
func (s *promptState) releaseDrain() {
	s.mu.Lock()
	drainAck := s.drainAck
	s.drainAck = nil
	s.mu.Unlock()
	if drainAck != nil {
		close(drainAck)
	}
}
