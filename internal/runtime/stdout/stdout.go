// Package stdout emits the runtime's transcript to its standard out
// as JSONL. It is the operational stream consumed by `kubectl logs`
// and external log aggregators (Fluent Bit, Vector, Promtail).
//
// Bytes emitted to stdout are identical to bytes written to
// events.jsonl by the transcript package; consumers that read both
// see the same sequence.
//
// Concurrency: Pump is intended for use in a single goroutine. The
// runtime invokes PumpToStdout in a `go` block alongside the
// transcript writer; one goroutine per subscriber pulls frames from
// its own channel and writes them to its own writer, so no
// synchronization is needed inside this package.
package stdout

import (
	"io"
	"os"
)

// Pump consumes from in and writes each frame verbatim to w. Returns
// nil when in is closed, or the first write error encountered.
//
// Production wires `in` to a transcript subscriber and `w` to
// os.Stdout. Tests use a bytes.Buffer.
func Pump(in <-chan []byte, w io.Writer) error {
	for line := range in {
		if _, err := w.Write(line); err != nil {
			return err
		}
	}
	return nil
}

// PumpToStdout is the production wiring helper. Returns when in is
// closed. Write errors against os.Stdout are dropped on the floor:
// if stdout is broken the runtime has bigger problems, and the
// transcript package still has the canonical record on disk.
func PumpToStdout(in <-chan []byte) {
	_ = Pump(in, os.Stdout)
}
