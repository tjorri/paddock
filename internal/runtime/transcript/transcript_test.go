package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAppend_WritesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	e := paddockv1alpha1.PaddockEvent{
		SchemaVersion: "1",
		Timestamp:     metav1.NewTime(time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)),
		Type:          "Message",
		Summary:       "hi",
	}
	if err := w.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path is a t.TempDir() join, not user input
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is a t.TempDir() join, not user input
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatalf("no line written, raw=%s", string(raw))
	}
	var got paddockv1alpha1.PaddockEvent
	if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != "Message" || got.Summary != "hi" {
		t.Fatalf("round-trip: %#v", got)
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second Close must not return an os.ErrClosed; it should reuse
	// the recorded result (nil) so `defer w.Close()` paired with an
	// explicit shutdown is safe.
	if err := w.Close(); err != nil {
		t.Fatalf("second Close should return same err as first; got %v", err)
	}
}

func TestAppend_FansOutToSubscribers(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	ch := make(chan []byte, 8)
	w.Subscribe(ch)
	defer w.Unsubscribe(ch)
	for i := 0; i < 3; i++ {
		if err := w.Append(paddockv1alpha1.PaddockEvent{Type: "Message", Summary: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		select {
		case b := <-ch:
			if len(b) == 0 || b[len(b)-1] != '\n' {
				t.Fatalf("expected trailing newline, got %q", b)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("subscriber missed frame %d", i)
		}
	}
}

func TestAppend_DoesNotBlockOnSlowSubscriber(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	slow := make(chan []byte) // unbuffered; drops on first Append
	w.Subscribe(slow)
	defer w.Unsubscribe(slow)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_ = w.Append(paddockv1alpha1.PaddockEvent{Type: "Message"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Append blocked on slow subscriber - should have dropped instead")
	}
}

// TestOpen_ErrorOnUnwritablePath verifies the error path when the
// target directory does not exist.
func TestOpen_ErrorOnUnwritablePath(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-dir", "events.jsonl")
	if _, err := Open(bogus); err == nil {
		t.Fatal("expected Open to fail for nonexistent parent dir, got nil")
	}
}

// TestUnsubscribe_StopsDelivery verifies that an unsubscribed channel
// no longer receives Append broadcasts.
func TestUnsubscribe_StopsDelivery(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	ch := make(chan []byte, 4)
	w.Subscribe(ch)
	if err := w.Append(paddockv1alpha1.PaddockEvent{Type: "Message"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected first frame before unsubscribe")
	}
	w.Unsubscribe(ch)
	if err := w.Append(paddockv1alpha1.PaddockEvent{Type: "Message"}); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-ch:
		t.Fatalf("expected no delivery after Unsubscribe, got %q", b)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// TestAppend_ConcurrentWritersProduceWellFormedLines exercises the
// internal write mutex: many goroutines appending in parallel must
// produce complete JSONL lines (no torn writes).
func TestAppend_ConcurrentWritersProduceWellFormedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				if err := w.Append(paddockv1alpha1.PaddockEvent{Type: "Message", Summary: "x"}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	f, err := os.Open(path) //nolint:gosec // G304: path is a t.TempDir() join, not user input
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	count := 0
	for sc.Scan() {
		var e paddockv1alpha1.PaddockEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("torn line at %d: %v: %q", count, err, sc.Bytes())
		}
		count++
	}
	if count != writers*perWriter {
		t.Fatalf("expected %d lines, got %d", writers*perWriter, count)
	}
}
