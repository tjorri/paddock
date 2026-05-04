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

	dataConns := acceptLoop(ctx, dataLn)
	ctlConns := acceptLoop(ctx, ctlLn)

	state := newPromptState()
	go state.copyFromDataUDS(ctx)

	// Feed the initial data conn + reconnects into state.
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

// promptState owns the lifecycle of the currently-active per-prompt
// CLI: stdin pipe (writable by the data-UDS reader), stdout drain
// goroutine, process handle. Methods are mutex-guarded.
//
// dataConn is swapped under mu when the runtime-side conn drops and
// reconnects. copyFromDataUDS parks on s.cond when dataConn is nil
// (between conns) and resumes when swapDataConn installs a new one.
type promptState struct {
	mu       sync.Mutex
	cond     *sync.Cond
	dataConn net.Conn // swapped on reconnect; reads under mu

	stdin     io.WriteCloser
	cmd       *exec.Cmd
	doneCh    chan struct{}
	activeSeq int32 // seq of the currently-active prompt; 0 between prompts.

	// drainAck, when non-nil, signals an end-prompt drain handshake
	// is in flight: endPrompt has requested the data reader to drain
	// any kernel-buffered bytes; the reader closes drainAck when the
	// drain is complete.
	drainAck chan struct{}
}

func newPromptState() *promptState {
	s := &promptState{}
	s.cond = sync.NewCond(&s.mu)
	return s
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

// currentDataConn returns the active data conn under mu, or nil if
// no conn is currently installed.
func (s *promptState) currentDataConn() net.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataConn
}

// beginPrompt spawns a fresh CLI and wires its pipes.
func (s *promptState) beginPrompt(cfg Config, seq int32) error {
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
	s.activeSeq = seq

	// stdout -> data UDS. The data conn may swap mid-prompt
	// (runtime-sidecar restart); re-fetch the current conn each
	// iteration. We can't use io.Copy directly: when the conn is
	// swapped mid-stream, io.Copy would have already consumed bytes
	// from stdout into its internal buffer and then failed to write
	// them to the now-closed old conn. Instead, read into our own
	// buffer and retry the Write against the (re-fetched) current
	// conn until it succeeds.
	go func() {
		defer close(s.doneCh)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				for written := 0; written < n; {
					c := s.currentDataConn()
					if c == nil {
						// No conn installed; park briefly and retry.
						s.mu.Lock()
						for s.dataConn == nil {
							s.cond.Wait()
						}
						s.mu.Unlock()
						continue
					}
					w, werr := c.Write(buf[written:n])
					if w > 0 {
						written += w
					}
					if werr == nil {
						continue
					}
					// Write failed (likely closed conn). Wait briefly
					// for the accept-loop to install the next conn,
					// then re-fetch and retry the unwritten remainder.
					time.Sleep(50 * time.Millisecond)
				}
			}
			if rerr != nil {
				// stdout closed → CLI exited; we're done.
				return
			}
		}
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
func (s *promptState) endPrompt(ctlConn net.Conn, logger *log.Logger) {
	s.mu.Lock()
	if s.cmd == nil {
		s.mu.Unlock()
		return
	}
	drainAck := make(chan struct{})
	if s.dataConn != nil {
		s.drainAck = drainAck
		// Trip any in-flight Read so the reader observes drainAck
		// quickly. The reader catches the timeout error, runs the
		// drain, and clears the deadline before resuming.
		_ = s.dataConn.SetReadDeadline(time.Now())
		s.mu.Unlock()
		<-drainAck
	} else {
		// No conn currently installed (runtime sidecar dropped
		// between prompts); nothing to drain. Skip the handshake.
		s.mu.Unlock()
	}

	s.mu.Lock()
	stdin, cmd, doneCh, seq := s.stdin, s.cmd, s.doneCh, s.activeSeq
	s.stdin = nil
	s.drainAck = nil
	s.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	var waitErr error
	if cmd != nil {
		// Bound the shutdown: a misbehaving CLI that ignores stdin EOF
		// (and its own SIGTERM trap) would otherwise hang us forever.
		// Wait first so the SIGTERM/SIGKILL escalation can run; that
		// guarantees stdout closes and the doneCh goroutine exits.
		waitErr = waitWithTimeout(cmd, shutdownGentle, shutdownHard)
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

// endActivePrompt is endPrompt without distinguishing "no active prompt".
func (s *promptState) endActivePrompt(ctlConn net.Conn, logger *log.Logger) {
	s.mu.Lock()
	hasActive := s.cmd != nil
	s.mu.Unlock()
	if hasActive {
		s.endPrompt(ctlConn, logger)
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

// copyFromDataUDS is the persistent reader for the data UDS. It parks
// on s.cond when no conn is installed (between accepts) and resumes
// when swapDataConn wires a new one. While a prompt is active
// (stdin != nil), bytes pump straight into stdin. When endPrompt
// requests a drain (sets s.drainAck and trips a read deadline), the
// reader collects any remaining kernel-buffered bytes into stdin and
// closes drainAck to release endPrompt.
func (s *promptState) copyFromDataUDS(ctx context.Context) {
	buf := make([]byte, 4096)
	for {
		c := s.currentDataConn()
		if c == nil {
			// No conn installed yet; park until one arrives or ctx
			// cancels.
			s.mu.Lock()
			for s.dataConn == nil && ctx.Err() == nil {
				s.cond.Wait()
			}
			if ctx.Err() != nil {
				s.mu.Unlock()
				return
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
				// Tripped by endPrompt's SetReadDeadline. Run the
				// drain handshake, then resume blocking reads.
				if s.runDrain(buf, c) {
					continue
				}
			}
			// Conn-level error (peer closed, ctx cancelled, etc.):
			// release any pending drain so endPrompt isn't stuck,
			// then drop the conn so swapDataConn can install a new
			// one. Also broadcast cond so anyone parked in
			// writeToStdin wakes up if the conn went away mid-write.
			s.releaseDrain()
			s.mu.Lock()
			if s.dataConn == c {
				s.dataConn = nil
			}
			s.mu.Unlock()
			s.cond.Broadcast()
			_ = c.Close()
			if ctx.Err() != nil {
				return
			}
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
//
// drainAck is consumed (set to nil under mu) before close so a
// subsequent conn-level error in the outer reader loop doesn't see a
// stale drainAck and double-close it.
func (s *promptState) runDrain(buf []byte, c net.Conn) bool {
	s.mu.Lock()
	drainAck := s.drainAck
	stdin := s.stdin
	if drainAck != nil {
		s.drainAck = nil
	}
	s.mu.Unlock()
	if drainAck == nil {
		return false
	}

	for {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		n, err := c.Read(buf)
		if n > 0 && stdin != nil {
			if _, werr := stdin.Write(buf[:n]); werr != nil {
				_ = werr
			}
		}
		if err != nil {
			break
		}
	}
	_ = c.SetReadDeadline(time.Time{})

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
