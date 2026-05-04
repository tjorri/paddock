package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
)

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

// readCtl decodes ctl frames from c and emits them on out until c is
// closed or ctx is canceled. Returns nil on graceful EOF, the underlying
// error otherwise.
func readCtl(ctx context.Context, c net.Conn, out chan<- ctlMessage) error {
	defer close(out)
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
