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
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Driver is the interface that per-prompt-process and persistent-process
// mode implementations satisfy.
type Driver interface {
	// SubmitPrompt delivers a prompt to the underlying Claude process.
	SubmitPrompt(ctx context.Context, p Prompt) error
	// Interrupt signals the active Claude process to abort.
	Interrupt(ctx context.Context) error
	// End signals that the harness run is over; the driver should clean up.
	End(ctx context.Context) error
	// StreamHandler returns an http.Handler that streams events to the caller.
	StreamHandler() http.Handler
}

// Prompt is the wire type for POST /prompts.
type Prompt struct {
	Text      string `json:"text"`
	Seq       int32  `json:"seq"`
	Submitter string `json:"submitter,omitempty"`
}

// Config holds the configuration for a Server.
type Config struct {
	Mode   string
	Driver Driver
}

// Server is the loopback HTTP server that receives control messages from
// the broker sidecar and dispatches them to a Driver.
type Server struct {
	cfg        Config
	mux        *http.ServeMux
	httpServer *http.Server
}

// NewServer creates a Server, registers all routes, and returns it.
func NewServer(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	// ReadHeaderTimeout guards against Slowloris (gosec G112): the
	// broker is the only legitimate client and finishes its headers
	// promptly. ReadTimeout / WriteTimeout cover full request bodies
	// for /prompts (capped at MaxInlinePromptBytes upstream).
	s.httpServer = &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	s.mux.HandleFunc("/prompts", s.handlePrompts)
	s.mux.HandleFunc("/interrupt", s.handleInterrupt)
	s.mux.HandleFunc("/end", s.handleEnd)
	s.mux.Handle("/stream", cfg.Driver.StreamHandler())

	return s
}

// Handler returns the underlying http.Handler (used by tests via httptest).
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Listen creates a TCP listener on addr.
//
// Production binds ":8431" (all interfaces). The broker connects from
// another pod via the run pod's eth0 IP, so a loopback-only listener
// would be unreachable. The per-run NetworkPolicy (controller Task 12)
// is the load-bearing security gate, restricting the peer set to
// broker-namespace + broker-pod labels.
//
// Tests pass "127.0.0.1:0" to bind an ephemeral local port.
func (s *Server) Listen(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp", addr)
}

// Serve starts the HTTP server on ln. It returns nil when the server is
// shut down gracefully (http.ErrServerClosed is suppressed).
func (s *Server) Serve(ln net.Listener) error {
	err := s.httpServer.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown performs a graceful shutdown with the supplied context. The
// context's deadline is the per-request grace period: in-flight handlers
// have until the deadline to return, then http.Server force-closes
// connections and Shutdown returns context.DeadlineExceeded.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// handlePrompts handles POST /prompts.
func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Cap the request body at MaxInlinePromptBytes + 1 (the +1 distinguishes
	// "exactly the cap" from "exceeded"). Same boundary the CRD/CLI/broker
	// pipeline enforces upstream; this is defense in depth at the adapter.
	r.Body = http.MaxBytesReader(w, r.Body, paddockv1alpha1.MaxInlinePromptBytes+1)
	var p Prompt
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.cfg.Driver.SubmitPrompt(r.Context(), p); err != nil {
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleInterrupt handles POST /interrupt.
func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.cfg.Driver.Interrupt(r.Context()); err != nil {
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleEnd handles POST /end.
func (s *Server) handleEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.cfg.Driver.End(r.Context()); err != nil {
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
