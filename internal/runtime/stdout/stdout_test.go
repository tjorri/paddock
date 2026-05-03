package stdout

import (
	"bytes"
	"errors"
	"testing"
)

func TestPump_WritesEachFrame(t *testing.T) {
	in := make(chan []byte, 4)
	in <- []byte(`{"type":"PromptSubmitted"}` + "\n")
	in <- []byte(`{"type":"Message"}` + "\n")
	close(in)
	var buf bytes.Buffer
	if err := Pump(in, &buf); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	got := buf.String()
	want := `{"type":"PromptSubmitted"}` + "\n" + `{"type":"Message"}` + "\n"
	if got != want {
		t.Fatalf("\ngot:  %q\nwant: %q", got, want)
	}
}

func TestPump_StopsOnClose(t *testing.T) {
	in := make(chan []byte)
	close(in)
	if err := Pump(in, &bytes.Buffer{}); err != nil {
		t.Fatalf("Pump: %v", err)
	}
}

// errWriter returns errWrite from every Write call.
type errWriter struct{}

var errWrite = errors.New("write failed")

func (errWriter) Write(p []byte) (int, error) { return 0, errWrite }

func TestPump_ReturnsWriteError(t *testing.T) {
	in := make(chan []byte, 2)
	in <- []byte("hello\n")
	in <- []byte("world\n") // would be written if Pump did not bail
	close(in)
	err := Pump(in, errWriter{})
	if !errors.Is(err, errWrite) {
		t.Fatalf("Pump err: got %v, want %v", err, errWrite)
	}
}

func TestPump_EmptyInput(t *testing.T) {
	// A channel that's already closed with no frames: Pump must return
	// nil immediately without ever calling Write.
	in := make(chan []byte)
	close(in)
	var buf bytes.Buffer
	if err := Pump(in, &buf); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("buf should be empty, got %q", buf.String())
	}
}
