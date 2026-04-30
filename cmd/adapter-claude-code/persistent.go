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

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// fanoutChanBuffer is the per-subscriber outbound channel depth. Slow
// consumers are dropped (broadcast is non-blocking) once their queue
// fills.
const fanoutChanBuffer = 64

// endGracePeriod is how long End() waits after closing stdin before
// SIGTERM-ing the agent's process group, in case the agent does not
// exit on its own when its input pipe closes.
const endGracePeriod = 2 * time.Second

// persistentDriver implements Driver for persistent-process mode.
// A single long-lived `claude --input-format stream-json
// --output-format stream-json` subprocess receives prompts via stdin
// and streams its stream-json output to attached WebSocket clients
// through an in-memory fanout. Each agent stdout line is also
// converted via convertLine and appended to events.jsonl on the
// shared workspace, mirroring the per-prompt driver.
type persistentDriver struct {
	logger    *log.Logger
	workspace string
	runName   string
	claudeBin string
	fanout    *eventFanout

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	started bool
	ended   bool
}

// NewPersistentDriver constructs a persistent-process Driver. Reads
// PADDOCK_WORKSPACE, PADDOCK_RUN_NAME, PADDOCK_CLAUDE_BINARY (defaults
// to "claude") from env. If the underlying process cannot be started,
// the driver is returned in a "broken" state — SubmitPrompt and the
// stream handler will return errors, but End() remains safe to call.
func NewPersistentDriver(logger *log.Logger) Driver {
	bin := os.Getenv("PADDOCK_CLAUDE_BINARY")
	if bin == "" {
		bin = "claude"
	}
	d := &persistentDriver{
		logger:    logger,
		workspace: os.Getenv("PADDOCK_WORKSPACE"),
		runName:   os.Getenv("PADDOCK_RUN_NAME"),
		claudeBin: bin,
		fanout:    newEventFanout(),
	}
	if err := d.start(); err != nil {
		logger.Printf("start persistent agent: %v", err)
		// Driver returned in broken state; SubmitPrompt returns an
		// error and StreamHandler responds 503.
	}
	return d
}

func (d *persistentDriver) start() error {
	// Use exec.Command (no context) so the spawned process is not
	// killed when an HTTP request context cancels. Lifecycle is owned
	// by Interrupt() / End().
	args := []string{"--input-format", "stream-json", "--output-format", "stream-json"}
	cmd := exec.Command(d.claudeBin, args...)
	cmd.Dir = d.workspace
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("start: %w", err)
	}

	d.mu.Lock()
	d.cmd = cmd
	d.stdin = stdin
	d.stdout = stdout
	d.started = true
	d.mu.Unlock()

	go d.demux()
	return nil
}

// demux reads the agent's stdout line-by-line, broadcasting each line
// to attached WebSocket clients via the fanout and appending the
// converted PaddockEvents to events.jsonl on the workspace. Mirrors
// drainStreamJSON from per_prompt.go but multiplexes the fanout side
// channel as well.
func (d *persistentDriver) demux() {
	br := bufio.NewReader(d.stdout)

	eventsPath := ""
	if d.workspace != "" && d.runName != "" {
		eventsPath = filepath.Join(d.workspace, ".paddock", "runs", d.runName, "events.jsonl")
	}
	var out *os.File
	var enc *json.Encoder
	if eventsPath != "" {
		if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
			d.logger.Printf("mkdir events: %v", err)
		} else {
			f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				d.logger.Printf("open events: %v", err)
			} else {
				out = f
				enc = json.NewEncoder(out)
				defer out.Close()
			}
		}
	}

	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			d.fanout.broadcast(line)
			if enc != nil {
				events, cerr := convertLine(string(line), time.Now().UTC())
				if cerr != nil {
					d.logger.Printf("convertLine: %v", cerr)
				}
				for _, ev := range events {
					if werr := enc.Encode(ev); werr != nil {
						d.logger.Printf("write event: %v", werr)
						break
					}
				}
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				d.logger.Printf("agent stdout closed: %v", rerr)
			}
			return
		}
	}
}

// streamControl is the stream-json control message we write to the
// agent's stdin. The custom _paddock_seq field is a local correlation
// marker — the agent ignores unknown fields.
type streamControl struct {
	Type    string               `json:"type"`
	Message streamControlMessage `json:"message"`
	Seq     int32                `json:"_paddock_seq"`
}

type streamControlMessage struct {
	Content []streamControlContent `json:"content"`
}

type streamControlContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (d *persistentDriver) SubmitPrompt(_ context.Context, p Prompt) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started || d.stdin == nil {
		return fmt.Errorf("agent not running")
	}
	msg := streamControl{
		Type: "user",
		Seq:  p.Seq,
		Message: streamControlMessage{
			Content: []streamControlContent{{Type: "text", Text: p.Text}},
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal control: %w", err)
	}
	if _, err := d.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}
	return nil
}

func (d *persistentDriver) Interrupt(_ context.Context) error {
	d.mu.Lock()
	cmd := d.cmd
	d.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// SIGINT to the process group — the leader alone is insufficient
	// if the agent spawns helpers.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		// ESRCH means the group is already gone; treat as success.
		if err == syscall.ESRCH {
			return nil
		}
		return fmt.Errorf("signal: %w", err)
	}
	return nil
}

func (d *persistentDriver) End(_ context.Context) error {
	d.mu.Lock()
	if d.ended {
		d.mu.Unlock()
		return nil
	}
	d.ended = true
	cmd := d.cmd
	stdin := d.stdin
	d.stdin = nil
	d.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// Wait in a goroutine so we can fall back to SIGTERM after a grace
	// period if the agent doesn't exit on its own.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case <-waitCh:
		return nil
	case <-time.After(endGracePeriod):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		<-waitCh
		return nil
	}
}

func (d *persistentDriver) StreamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		ready := d.started && !d.ended
		d.mu.Unlock()
		if !ready {
			http.Error(w, "agent not running", http.StatusServiceUnavailable)
			return
		}

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"paddock.stream.v1"},
		})
		if err != nil {
			d.logger.Printf("websocket accept: %v", err)
			return
		}
		defer func() { _ = c.Close(websocket.StatusInternalError, "exit") }()

		ch := d.fanout.subscribe()
		defer d.fanout.unsubscribe(ch)

		ctx := r.Context()

		// Outbound: drain the fanout to the client. When the inbound
		// loop terminates (client disconnect or read error), ctx is
		// canceled and Write returns; the goroutine exits.
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case line, ok := <-ch:
					if !ok {
						return
					}
					if err := c.Write(ctx, websocket.MessageText, line); err != nil {
						return
					}
				}
			}
		}()

		// Inbound: write client messages to agent stdin (stream-json
		// control). Each message is treated as one line.
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			d.mu.Lock()
			stdin := d.stdin
			d.mu.Unlock()
			if stdin == nil {
				return
			}
			if _, err := stdin.Write(append(msg, '\n')); err != nil {
				return
			}
		}
	})
}

// eventFanout broadcasts each agent stdout line to all subscribed
// channels. Subscribers receive a copy; broadcast is non-blocking so
// a slow consumer is dropped rather than stalling the agent.
type eventFanout struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newEventFanout() *eventFanout {
	return &eventFanout{subs: map[chan []byte]struct{}{}}
}

func (f *eventFanout) subscribe() chan []byte {
	ch := make(chan []byte, fanoutChanBuffer)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch
}

func (f *eventFanout) unsubscribe(ch chan []byte) {
	f.mu.Lock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
	f.mu.Unlock()
}

func (f *eventFanout) broadcast(line []byte) {
	cp := make([]byte, len(line))
	copy(cp, line)
	f.mu.Lock()
	for ch := range f.subs {
		select {
		case ch <- cp:
		default:
			// Drop on slow consumer.
		}
	}
	f.mu.Unlock()
}
