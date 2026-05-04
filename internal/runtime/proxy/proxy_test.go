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

// TestServer_CloseWaitsForDataReaderDrain asserts Close synchronizes
// with the runDataReader goroutine's exit via dataReaderDone. We hold
// runDataReader inside an OnEvent callback that blocks on a test
// channel. While the goroutine is held there, srv.Close() must block
// (waiting on dataReaderDone). After we release the OnEvent block,
// runDataReader returns and Close unblocks.
//
// Without the dataReaderDone wait in Close, Close would return
// immediately while runDataReader is still in flight — the bug this
// test guards against.
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

	// onEventEntered closes when runDataReader hits the OnEvent callback
	// (so we know it's processed at least one line and is now blocked
	// inside our user-controlled callback rather than back at Read).
	// releaseOnEvent unblocks the callback when the test is ready.
	onEventEntered := make(chan struct{})
	releaseOnEvent := make(chan struct{})

	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
		Converter: func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
			return []paddockv1alpha1.PaddockEvent{{Type: "Test", Summary: line}}, nil
		},
		OnEvent: func(e paddockv1alpha1.PaddockEvent) {
			select {
			case <-onEventEntered:
				// already signaled; later events shouldn't re-block
			default:
				close(onEventEntered)
				<-releaseOnEvent
			}
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	supData := <-supDataConnCh
	t.Cleanup(func() { _ = supData.Close() })
	if _, err := supData.Write([]byte(`{"type":"system"}` + "\n")); err != nil {
		t.Fatalf("write supervisor side: %v", err)
	}

	// Wait for runDataReader to enter our OnEvent callback. After this
	// point, the goroutine is blocked inside our callback; it has NOT
	// yet returned, so dataReaderDone is still open.
	select {
	case <-onEventEntered:
	case <-time.After(2 * time.Second):
		t.Fatalf("OnEvent never fired — runDataReader did not deliver the line")
	}

	// Now call Close in a goroutine; it should block on dataReaderDone.
	closeDone := make(chan struct{})
	go func() {
		_ = srv.Close()
		close(closeDone)
	}()

	// Confirm Close is blocked. Use a short window — if Close returns
	// quickly, dataReaderDone isn't synchronizing.
	select {
	case <-closeDone:
		t.Fatalf("Close returned while runDataReader was still in flight — dataReaderDone wait missing or broken")
	case <-time.After(100 * time.Millisecond):
		// Good: Close is blocking.
	}

	// Release the OnEvent block; runDataReader's loop continues, the
	// next Read returns ErrClosed (Close already shut the data conn),
	// and the goroutine exits — closing dataReaderDone.
	close(releaseOnEvent)

	select {
	case <-closeDone:
		// Good: Close unblocked after the goroutine returned.
	case <-time.After(1 * time.Second):
		t.Fatalf("Close did not return after OnEvent released — possible 500ms timeout but should be much faster")
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
