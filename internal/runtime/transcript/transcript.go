// Package transcript owns the events.jsonl file on the workspace PVC.
// It is the single writer; concurrent appends from the runtime
// (prompt receipt, output translation) are serialized through Append.
//
// A tail-broadcast subscription (Subscribe) lets the proxy /stream
// handler fan out new lines to WebSocket clients without re-reading
// from disk. Subscribers are best-effort: a slow consumer drops
// frames rather than blocking the writer. The on-disk file remains
// the source of truth, so a client that misses a broadcast frame
// can recover by re-reading events.jsonl.
package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Writer is a single-writer handle to events.jsonl plus its tail-broadcast
// subscriber set. All exported methods are safe for concurrent use:
// Append serializes file writes through mu; Subscribe / Unsubscribe /
// the subscriber-list snapshot in broadcast take a separate subMu so a
// slow consumer cannot block the file writer.
type Writer struct {
	mu          sync.Mutex
	f           *os.File
	subMu       sync.Mutex
	subscribers map[chan<- []byte]struct{}
}

// Open creates or appends to events.jsonl at path, returning a Writer
// ready for Append calls.
func Open(path string) (*Writer, error) {
	// 0o644: events.jsonl lives on a Pod-local volume (the workspace
	// PVC); sibling containers in the same Pod (controllers, future
	// readers) need read access. Same Pod-local audit-trail rationale
	// as internal/runtime/archive.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // G302: see rationale above
	if err != nil {
		return nil, fmt.Errorf("transcript: open %s: %w", path, err)
	}
	return &Writer{
		f:           f,
		subscribers: make(map[chan<- []byte]struct{}),
	}, nil
}

// Close releases the underlying file handle. Subscribers' channels
// are not closed by this call (their owners are responsible for the
// lifecycle of any goroutine consuming them).
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// Append serializes e to one JSONL line, appends to the file, and
// fans the same bytes to every subscriber. Returns on first error;
// the file write is not retried. Safe for concurrent use.
func (w *Writer) Append(e paddockv1alpha1.PaddockEvent) error {
	line, err := json.Marshal(&e)
	if err != nil {
		return fmt.Errorf("transcript: marshal: %w", err)
	}
	line = append(line, '\n')
	w.mu.Lock()
	if _, err := w.f.Write(line); err != nil {
		w.mu.Unlock()
		return fmt.Errorf("transcript: write: %w", err)
	}
	w.mu.Unlock()
	w.broadcast(line)
	return nil
}

// broadcast snapshots the subscriber set under subMu, then fans the
// frame out without holding the lock. A non-blocking send drops the
// frame on slow / unready consumers so a stalled subscriber cannot
// back-pressure the writer.
func (w *Writer) broadcast(line []byte) {
	w.subMu.Lock()
	subs := make([]chan<- []byte, 0, len(w.subscribers))
	for ch := range w.subscribers {
		subs = append(subs, ch)
	}
	w.subMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- line:
		default:
			// drop on slow consumer; subscribers are best-effort by design
		}
	}
}

// Subscribe registers ch to receive every Append line going forward.
// Pass a buffered channel (recommended capacity 64) so a slow consumer
// doesn't drop frames during normal operation. Subscribe never replays
// past lines; clients that need history should read events.jsonl
// directly first, then subscribe.
//
// Call Unsubscribe to detach.
func (w *Writer) Subscribe(ch chan<- []byte) {
	w.subMu.Lock()
	w.subscribers[ch] = struct{}{}
	w.subMu.Unlock()
}

// Unsubscribe detaches ch from the broadcast set. It is safe to call
// with a channel that was never subscribed.
func (w *Writer) Unsubscribe(ch chan<- []byte) {
	w.subMu.Lock()
	delete(w.subscribers, ch)
	w.subMu.Unlock()
}
