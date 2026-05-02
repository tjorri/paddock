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

// Package proxy is the adapter-side HTTP+WS frontend that forwards
// stream-json frames between the broker and the per-run harness
// supervisor over a pair of unix-domain sockets.
//
// The adapter is harness-agnostic — it knows nothing about the
// underlying CLI. The caller (e.g. cmd/adapter-claude-code) wires a
// harness-specific Converter that maps each line from the data UDS
// to zero-or-more PaddockEvents written to events.jsonl.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Config carries the proxy server's runtime configuration.
type Config struct {
	Mode       string // "per-prompt-process" | "persistent-process"
	DataSocket string
	CtlSocket  string
	EventsPath string // path for events.jsonl translation (PADDOCK_EVENTS_PATH)
	Backoff    BackoffConfig
	// Converter is the harness-specific line-to-PaddockEvent translator
	// (e.g. cmd/adapter-claude-code/convert.go). May be nil for tests.
	Converter func(line string) ([]paddockv1alpha1.PaddockEvent, error)
}

// Server wraps the adapter's HTTP+WS surface and the dialed UDS pair.
type Server struct {
	cfg      Config
	mux      *http.ServeMux
	dataConn net.Conn
	ctlConn  net.Conn
	fanout   *fanout

	mu          sync.Mutex
	dataWriteMu sync.Mutex // serializes writes from concurrent /prompts (defense in depth)
	ctlWriteMu  sync.Mutex
}

// ctlMessage is the wire shape for control frames emitted to the supervisor.
type ctlMessage struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Seq    int32  `json:"seq,omitempty"`
}

// NewServer dials both UDS (with backoff), wires HTTP routes, and
// returns a ready-to-serve Server. The caller passes the dialed
// connections via the returned Server's lifetime.
func NewServer(ctx context.Context, cfg Config) (*Server, error) {
	dctx, cancel := context.WithTimeout(ctx, totalBackoff(cfg.Backoff))
	defer cancel()

	dataConn, err := dialUDSWithBackoff(dctx, cfg.DataSocket, cfg.Backoff)
	if err != nil {
		return nil, fmt.Errorf("dial data UDS: %w", err)
	}
	ctlConn, err := dialUDSWithBackoff(dctx, cfg.CtlSocket, cfg.Backoff)
	if err != nil {
		_ = dataConn.Close()
		return nil, fmt.Errorf("dial ctl UDS: %w", err)
	}

	s := &Server{
		cfg:      cfg,
		mux:      http.NewServeMux(),
		dataConn: dataConn,
		ctlConn:  ctlConn,
		fanout:   newFanout(),
	}
	s.mux.HandleFunc("/prompts", s.handlePrompts)
	s.mux.HandleFunc("/interrupt", s.handleInterrupt)
	s.mux.HandleFunc("/end", s.handleEnd)
	s.mux.Handle("/stream", s.streamHandler())

	go func() {
		// runDataReader takes ownership of reading from dataConn for
		// the lifetime of the Server. It returns on EOF (supervisor
		// closed the connection) or any I/O error; the Server's
		// callers observe failure via subsequent /prompts errors.
		_ = runDataReader(dataConn, s.fanout, cfg.EventsPath, cfg.Converter)
	}()

	return s, nil
}

// Handler returns the HTTP handler for serving over a net.Listener.
func (s *Server) Handler() http.Handler { return s.mux }

// Close drops both UDS connections.
func (s *Server) Close() error {
	var firstErr error
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataConn != nil {
		if err := s.dataConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.dataConn = nil
	}
	if s.ctlConn != nil {
		if err := s.ctlConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.ctlConn = nil
	}
	return firstErr
}

// totalBackoff approximates the wall-clock cost of cfg.Tries, used
// to size the dial context.
func totalBackoff(cfg BackoffConfig) time.Duration {
	d, total := cfg.Initial, cfg.Initial
	for i := 1; i < cfg.Tries; i++ {
		if d = d * 2; d > cfg.Max {
			d = cfg.Max
		}
		total += d
	}
	return total + 5*time.Second // headroom for the dial calls themselves
}

func (s *Server) handleEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Decode optional reason; tolerate empty body.
	var body struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if err := s.writeCtl(ctlMessage{Action: "end", Reason: body.Reason}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Close write half of data UDS so the supervisor sees EOF on stdin.
	if cw, ok := s.dataConn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.writeCtl(ctlMessage{Action: "interrupt"}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) writeCtl(msg ctlMessage) error {
	s.ctlWriteMu.Lock()
	defer s.ctlWriteMu.Unlock()
	enc := json.NewEncoder(s.ctlConn)
	if err := enc.Encode(msg); err != nil {
		return fmt.Errorf("write ctl: %w", err)
	}
	return nil
}
