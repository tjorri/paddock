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
