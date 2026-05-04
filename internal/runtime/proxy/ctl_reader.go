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
