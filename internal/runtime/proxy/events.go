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
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// turnTerminalEventType reports whether a PaddockEvent type signals
// the end of a turn (claude's stream-json closes a turn with either a
// final Result frame or an Error frame). The adapter shim's
// OnTurnComplete hook fires once per turn-terminal event so the
// broker can clear Status.Interactive.CurrentTurnSeq.
func turnTerminalEventType(t string) bool {
	return t == "Result" || t == "Error"
}

// runDataReader reads the data UDS line-by-line, broadcasts each
// line to subscribers (for /stream WS clients) and translates each
// line via conv, dispatching the resulting PaddockEvents.
//
// Event sink selection: when onEvent is non-nil, every converted
// event is delivered through it (the unified runtime wires this to
// transcript.Writer.Append). When onEvent is nil but eventsPath is
// set, events are JSON-encoded directly to the file at eventsPath
// (a direct-write fallback retained for tests). The two paths are
// mutually exclusive in practice; if both are set, onEvent wins and
// the file is left untouched.
//
// onTurnComplete, when non-nil, is invoked in a goroutine once per
// turn-terminal PaddockEvent (Type=Result or Type=Error) so a slow
// broker callback cannot stall the data reader. May fire multiple
// times if the converter emits more than one turn-terminal event for
// a single line; the broker handler is idempotent.
//
// Returns when the data UDS read returns an error (typically EOF
// after the supervisor closes the connection).
func runDataReader(
	r io.Reader,
	fan *fanout,
	eventsPath string,
	conv func(string) ([]paddockv1alpha1.PaddockEvent, error),
	onEvent func(paddockv1alpha1.PaddockEvent),
	onTurnComplete func(ctx context.Context),
) error {
	var (
		out *os.File
		enc *json.Encoder
	)
	// Legacy file-write path: enabled only when the caller has not
	// supplied OnEvent. New callers (the unified runtime) leave
	// EventsPath empty and route every event through OnEvent.
	if onEvent == nil && eventsPath != "" && conv != nil {
		if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
			return fmt.Errorf("mkdir events: %w", err)
		}
		f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open events: %w", err)
		}
		out = f
		enc = json.NewEncoder(out)
		defer func() { _ = out.Close() }()
	}

	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fan.broadcast(line)
			if conv != nil {
				events, cerr := conv(string(line))
				if cerr != nil {
					log.Printf("convert line: %v", cerr)
				}
				if onEvent != nil {
					for _, ev := range events {
						onEvent(ev)
					}
				} else if enc != nil {
					for _, ev := range events {
						if werr := enc.Encode(ev); werr != nil {
							log.Printf("write event: %v", werr)
							break
						}
					}
					if len(events) > 0 {
						_ = out.Sync()
					}
				}
				if onTurnComplete != nil {
					for _, ev := range events {
						if turnTerminalEventType(ev.Type) {
							// Fire-and-forget: don't let a slow
							// broker stall data-reader throughput.
							go onTurnComplete(context.Background())
						}
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read data UDS: %w", err)
		}
	}
}
