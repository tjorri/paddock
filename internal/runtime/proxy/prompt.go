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
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// handlePrompts forwards POST /prompts as bytes on the data UDS, with
// per-prompt-process boundary delimitation on the ctl UDS.
//
// In per-prompt-process mode the handler emits, sequentially:
//
//	begin-prompt (ctl) -> body bytes (data) -> end-prompt (ctl)
//
// Data and ctl are separate UDS connections with no cross-socket
// ordering guarantee at the kernel level; the supervisor's drain
// handshake on endPrompt (cmd/harness-supervisor/per_prompt.go) is
// the receiving-side counterpart that resolves the race. Beyond the
// existing dataWriteMu / ctlWriteMu serialization, no additional
// synchronization is required here.
//
// Failure mode: when any UDS write below fails (begin-prompt ctl,
// data body, or end-prompt ctl), the supervisor's prompt-boundary
// state is desynchronized. The handler returns 502, but there is no
// recovery path on the proxy side: a subsequent /prompts will issue
// another begin-prompt against a connection the supervisor has
// already given up on, and that call will also fail. Treat 502 from
// this endpoint as run-fatal at the broker layer, not as a retryable
// transient. The cleaner recovery -- close the entire Server on UDS
// write error so subsequent calls hard-fail with a definitive error
// -- is a planned follow-up tracked alongside the supervisor-side
// {"event":"crashed"} ctl emission.
// promptRequest is the wire shape the broker (internal/broker/interactive.go
// handlePrompts) forwards on POST /prompts. `seq` is broker-assigned; we
// echo it back in the response body so the caller can correlate a turn
// without holding the WS open for the round-trip.
type promptRequest struct {
	Text      string `json:"text"`
	Seq       int32  `json:"seq"`
	Submitter string `json:"submitter,omitempty"`
}

func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, paddockv1alpha1.MaxInlinePromptBytes+1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req promptRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Notify the runtime so it can persist the prompt to the
	// transcript before any UDS I/O happens. We do this even if the
	// downstream write fails — the input record reflects what was
	// received from the broker, not what landed on the agent's stdin.
	if s.cfg.OnPromptReceived != nil {
		s.cfg.OnPromptReceived(req.Text, req.Seq, req.Submitter)
	}

	// Format the prompt for the harness CLI's stdin. The proxy is
	// harness-agnostic; the per-harness shim (cmd/runtime-claude-code/
	// main.go for claude) supplies a PromptFormatter that wraps the
	// user's text into the harness's native stream-json shape. When
	// PromptFormatter is nil, fall back to writing the request body
	// verbatim — useful for harnesses that already accept Paddock's
	// {text,seq,submitter} wire shape on stdin (and for tests).
	harnessLine := body
	if s.cfg.PromptFormatter != nil {
		harnessLine, err = s.cfg.PromptFormatter(req.Text, req.Seq)
		if err != nil {
			http.Error(w, "format prompt: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if s.cfg.Mode == "per-prompt-process" {
		if err := s.writeCtl(ctlMessage{Action: "begin-prompt", Seq: req.Seq}); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	s.dataWriteMu.Lock()
	_, werr := s.dataConn.Write(append(harnessLine, '\n'))
	s.dataWriteMu.Unlock()
	if werr != nil {
		http.Error(w, fmt.Sprintf("write data UDS: %v", werr), http.StatusBadGateway)
		return
	}

	if s.cfg.Mode == "per-prompt-process" {
		if err := s.writeCtl(ctlMessage{Action: "end-prompt", Seq: req.Seq}); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(struct {
		Seq int32 `json:"seq"`
	}{Seq: req.Seq})
}
