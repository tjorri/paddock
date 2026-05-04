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
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestServer_PromptWritesToDataUDS(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, err := net.Listen("unix", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dataLn.Close() }()
	ctlLn, err := net.Listen("unix", ctlPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctlLn.Close() }()

	// Fake supervisor: accept once on each, capture data writes, close.
	dataReceived := make(chan []byte, 1)
	go func() {
		c, _ := dataLn.Accept()
		if c == nil {
			return
		}
		defer func() { _ = c.Close() }()
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		dataReceived <- buf[:n]
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			_ = c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{
		"type":         "user",
		"_paddock_seq": 1,
		"message":      map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi"}}},
	})
	r := httptest.NewRequest(http.MethodPost, "/prompts", bytes.NewReader(body))
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}

	select {
	case got := <-dataReceived:
		if !strings.Contains(string(got), `"text":"hi"`) {
			t.Errorf("data UDS got %q, want substring \"text\":\"hi\"", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no data on UDS after 2s")
	}
}

// TestServer_PromptInvokesOnPromptReceived asserts the new hook is
// called once per /prompts request with the parsed text/seq/
// submitter, and that it fires before the data UDS write so the
// runtime can record the prompt even if downstream I/O fails.
func TestServer_PromptInvokesOnPromptReceived(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, err := net.Listen("unix", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataLn.Close() })
	ctlLn, err := net.Listen("unix", ctlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctlLn.Close() })

	go func() {
		c, _ := dataLn.Accept()
		if c != nil {
			defer func() { _ = c.Close() }()
			_, _ = c.Read(make([]byte, 4096))
		}
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			_ = c.Close()
		}
	}()

	type promptCall struct {
		text      string
		seq       int32
		submitter string
	}
	calls := make(chan promptCall, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
		OnPromptReceived: func(text string, seq int32, submitter string) {
			calls <- promptCall{text: text, seq: seq, submitter: submitter}
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	body, _ := json.Marshal(map[string]any{
		"text":      "hello",
		"seq":       int32(7),
		"submitter": "alice",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/prompts", bytes.NewReader(body))
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	select {
	case got := <-calls:
		if got.text != "hello" || got.seq != 7 || got.submitter != "alice" {
			t.Errorf("OnPromptReceived = %+v, want hello/7/alice", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnPromptReceived not called within 2s")
	}
}

func TestServer_InterruptWritesToCtl(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, _ := net.Listen("unix", dataPath)
	defer func() { _ = dataLn.Close() }()
	ctlLn, _ := net.Listen("unix", ctlPath)
	defer func() { _ = ctlLn.Close() }()

	go func() {
		c, _ := dataLn.Accept()
		if c != nil {
			defer func() { _ = c.Close() }()
			_, _ = c.Read(make([]byte, 1))
		}
	}()

	ctlReceived := make(chan ctlMessage, 1)
	go func() {
		c, _ := ctlLn.Accept()
		if c == nil {
			return
		}
		defer func() { _ = c.Close() }()
		var msg ctlMessage
		_ = json.NewDecoder(bufio.NewReader(c)).Decode(&msg)
		ctlReceived <- msg
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/interrupt", nil)
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}

	select {
	case msg := <-ctlReceived:
		if msg.Action != "interrupt" {
			t.Errorf("action = %q, want interrupt", msg.Action)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ctl message after 2s")
	}
}

// TestServer_CloseWaitsForDataReaderDrain asserts Close blocks until
// the runDataReader goroutine has exited (and therefore the deferred
// events.jsonl close has fired). Without this, the last few lines
// written to events.jsonl can be lost on graceful shutdown.
func TestServer_CloseWaitsForDataReaderDrain(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, err := net.Listen("unix", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataLn.Close() })
	ctlLn, err := net.Listen("unix", ctlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctlLn.Close() })

	// Fake supervisor: hold both conns open. The data conn will be
	// half-closed by the test once we want runDataReader to exit.
	supDataConnCh := make(chan net.Conn, 1)
	go func() {
		c, _ := dataLn.Accept()
		if c != nil {
			supDataConnCh <- c
		}
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventsPath := filepath.Join(dir, "events.jsonl")
	// Legacy events-file path is gated on conv != nil; supply a
	// minimal converter that emits one PaddockEvent per line so the
	// data reader actually opens and writes events.jsonl, exercising
	// the deferred-close synchronization Close() must wait on.
	conv := func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
		return []paddockv1alpha1.PaddockEvent{{Type: "system", Summary: line}}, nil
	}
	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		EventsPath: eventsPath,
		Converter:  conv,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	supData := <-supDataConnCh
	// Send one line so runDataReader has something to flush before EOF.
	if _, err := supData.Write([]byte(`{"type":"system","subtype":"init"}` + "\n")); err != nil {
		t.Fatalf("write supervisor side: %v", err)
	}
	// Closing the supervisor side gives runDataReader an EOF; it then
	// drains its internal buffers and returns. Close() must wait for
	// that exit before its own return. Yield briefly so the EOF
	// propagates to runDataReader via the kernel before Close races
	// it by closing the runtime end of the conn (without this nudge,
	// Close's s.dataConn.Close() can abort a still-blocked Read on
	// the runtime side, and the buffered line we just wrote is lost
	// regardless of the dataReaderDone wait — that's a separate
	// Close-ordering bug, not what this test is meant to assert).
	_ = supData.Close()
	time.Sleep(50 * time.Millisecond)

	// Close should observe runDataReader's exit (via dataReaderDone)
	// rather than racing it. Bound this with a generous test timeout.
	closeDone := make(chan struct{})
	go func() {
		_ = srv.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("Close did not return within 3s — likely waiting on dataReaderDone with no goroutine to close it")
	}

	// After Close returns, the events.jsonl file must contain the line
	// runDataReader processed. If Close raced the deferred file close,
	// this assertion exposes the lost-bytes bug.
	contents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(contents), `"system"`) {
		t.Errorf("events.jsonl missing system frame; got %q", contents)
	}
}

func TestServer_DataUDSLinesFanOutToStream(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, _ := net.Listen("unix", dataPath)
	defer func() { _ = dataLn.Close() }()
	ctlLn, _ := net.Listen("unix", ctlPath)
	defer func() { _ = ctlLn.Close() }()

	// Gate the supervisor's write on the test having subscribed to the
	// fanout. Otherwise runDataReader can drain and broadcast both
	// frames before the subscriber registers, dropping them on the
	// floor (the fanout has no replay buffer).
	startWrite := make(chan struct{})

	// Fake supervisor: accept data, push two newline-delimited frames.
	go func() {
		c, _ := dataLn.Accept()
		if c == nil {
			return
		}
		defer func() { _ = c.Close() }()
		<-startWrite
		_, _ = c.Write([]byte(`{"type":"first"}` + "\n" + `{"type":"second"}` + "\n"))
		// Hold open until the test closes us.
		<-time.After(2 * time.Second)
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			defer func() { _ = c.Close() }()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// Subscribe to the fanout directly (analogous to what the WS handler does).
	ch := srv.fanout.subscribe()
	defer srv.fanout.unsubscribe(ch)
	close(startWrite)

	got := []string{}
	for i := 0; i < 2; i++ {
		select {
		case line := <-ch:
			got = append(got, strings.TrimSpace(string(line)))
		case <-time.After(2 * time.Second):
			t.Fatalf("fanout receive timeout after %d/%d", i, 2)
		}
	}
	if got[0] != `{"type":"first"}` || got[1] != `{"type":"second"}` {
		t.Errorf("fanout lines = %v, want [first, second]", got)
	}
}
