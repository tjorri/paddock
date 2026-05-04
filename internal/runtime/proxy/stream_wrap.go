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
	"bytes"
	"encoding/json"
)

// wrapStreamLine adapts a single raw harness-output line into the
// {"type": ..., "data": ...} envelope the TUI's paddockbroker.StreamFrame
// decoder expects. The runtime broadcasts harness output verbatim
// today (events.go's runDataReader -> fan.broadcast); the TUI's
// StreamFrame decode silently leaves Data nil because the inner shape
// has no top-level "data" field, so the user sees blank events.
//
// Wrapping shape:
//
//   - Inner JSON parses and has a string "type" field:
//     {"type": <inner.type>, "data": <inner JSON>}
//   - Inner JSON parses but has no "type" field, OR is unparseable:
//     {"type": "raw", "data": {"raw": "<original bytes as string>"}}
//
// The original bytes are preserved verbatim under "data" so consumers
// have full context — the wrapping is purely additive and the outer
// envelope is the only thing the TUI's frameToEvent reads (frame.Type
// + frame.Data subfields).
//
// The function always emits a single newline-terminated frame. Input
// trailing newlines (if any) are stripped before re-encoding.
func wrapStreamLine(raw []byte) []byte {
	trimmed := bytes.TrimRight(raw, "\n")

	// Try to extract a top-level "type" string field from the inner
	// JSON. Use a narrow shape so we don't allocate the full structure.
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &probe); err == nil && probe.Type != "" {
		// Pass the inner JSON through as RawMessage to avoid a re-encode.
		out, mErr := json.Marshal(struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}{
			Type: probe.Type,
			Data: trimmed,
		})
		if mErr == nil {
			return append(out, '\n')
		}
		// Re-encode failed (vanishingly unlikely with a valid Unmarshal
		// upstream) — fall through to the raw envelope below.
	}

	// Fallback: wrap the literal bytes as a {raw: "..."} string so the
	// wire stays valid JSON even for malformed harness output.
	out, _ := json.Marshal(struct {
		Type string `json:"type"`
		Data struct {
			Raw string `json:"raw"`
		} `json:"data"`
	}{
		Type: "raw",
		Data: struct {
			Raw string `json:"raw"`
		}{Raw: string(trimmed)},
	})
	return append(out, '\n')
}
