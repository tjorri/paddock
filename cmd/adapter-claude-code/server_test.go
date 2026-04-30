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

package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubDriver is a test-only Driver implementation that records calls.
type stubDriver struct {
	prompted   int
	interrupted int
	ended      int
	lastPrompt Prompt
}

func (s *stubDriver) SubmitPrompt(_ context.Context, p Prompt) error {
	s.prompted++
	s.lastPrompt = p
	return nil
}

func (s *stubDriver) Interrupt(_ context.Context) error {
	s.interrupted++
	return nil
}

func (s *stubDriver) End(_ context.Context) error {
	s.ended++
	return nil
}

func (s *stubDriver) StreamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func stringReader(s string) *strings.Reader {
	return strings.NewReader(s)
}

// TestServer_PromptsRouted verifies that POST /prompts decodes the body
// and calls Driver.SubmitPrompt.
func TestServer_PromptsRouted(t *testing.T) {
	drv := &stubDriver{}
	srv := NewServer(Config{Mode: "per-prompt-process", Driver: drv})

	body := `{"text":"hello world","seq":1,"submitter":"broker"}`
	req := httptest.NewRequest(http.MethodPost, "/prompts", stringReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if drv.prompted != 1 {
		t.Fatalf("SubmitPrompt called %d times, want 1", drv.prompted)
	}
	if drv.lastPrompt.Text != "hello world" {
		t.Errorf("prompt.Text = %q, want %q", drv.lastPrompt.Text, "hello world")
	}
	if drv.lastPrompt.Seq != 1 {
		t.Errorf("prompt.Seq = %d, want 1", drv.lastPrompt.Seq)
	}
}

// TestServer_InterruptRouted verifies that POST /interrupt calls
// Driver.Interrupt and returns 202.
func TestServer_InterruptRouted(t *testing.T) {
	drv := &stubDriver{}
	srv := NewServer(Config{Mode: "per-prompt-process", Driver: drv})

	req := httptest.NewRequest(http.MethodPost, "/interrupt", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if drv.interrupted != 1 {
		t.Fatalf("Interrupt called %d times, want 1", drv.interrupted)
	}
}

// TestServer_LoopbackOnly verifies that Listen binds to the loopback
// interface (127.0.0.1), not all interfaces.
func TestServer_LoopbackOnly(t *testing.T) {
	drv := &stubDriver{}
	srv := NewServer(Config{Mode: "per-prompt-process", Driver: drv})

	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() {
		t.Errorf("bound address %v is not loopback", addr.IP)
	}
}

// TestServer_GracefulShutdown verifies that Serve + Shutdown returns nil.
func TestServer_GracefulShutdown(t *testing.T) {
	drv := &stubDriver{}
	srv := NewServer(Config{Mode: "per-prompt-process", Driver: drv})

	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Serve did not exit after Shutdown")
	}
}
