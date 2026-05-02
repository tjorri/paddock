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
	var p struct {
		Seq int32 `json:"_paddock_seq"`
	}
	_ = json.Unmarshal(body, &p)

	if s.cfg.Mode == "per-prompt-process" {
		if err := s.writeCtl(ctlMessage{Action: "begin-prompt", Seq: p.Seq}); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	s.dataWriteMu.Lock()
	_, werr := s.dataConn.Write(append(body, '\n'))
	s.dataWriteMu.Unlock()
	if werr != nil {
		http.Error(w, fmt.Sprintf("write data UDS: %v", werr), http.StatusBadGateway)
		return
	}

	if s.cfg.Mode == "per-prompt-process" {
		if err := s.writeCtl(ctlMessage{Action: "end-prompt", Seq: p.Seq}); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}
