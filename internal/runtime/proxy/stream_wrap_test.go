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
	"strings"
	"testing"
)

// streamFrame is the same shape the TUI's paddockbroker.StreamFrame
// decodes into. Defined locally so tests verify the wire contract
// without importing the TUI package (which would create a layering
// inversion).
type streamFrame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// TestWrapStreamLine_WrapsClaudeAssistantFrame asserts the wrapping
// pulls the inner frame's "type" up to the outer envelope and stuffs
// the original JSON into "data" verbatim — the shape the TUI expects.
func TestWrapStreamLine_WrapsClaudeAssistantFrame(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
	got := wrapStreamLine(raw)

	var frame streamFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatalf("wrapped output is not valid JSON: %v\noutput=%q", err, got)
	}
	if frame.Type != "assistant" {
		t.Errorf("frame.Type = %q, want %q", frame.Type, "assistant")
	}
	if !strings.Contains(string(frame.Data), `"role":"assistant"`) {
		t.Errorf("frame.Data missing inner content; got %q", frame.Data)
	}
}

// TestWrapStreamLine_WrapsResultFrame covers the turn-terminating
// claude shape.
func TestWrapStreamLine_WrapsResultFrame(t *testing.T) {
	raw := []byte(`{"type":"result","subtype":"success","num_turns":1,"is_error":false}`)
	got := wrapStreamLine(raw)

	var frame streamFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatalf("wrapped output is not valid JSON: %v\noutput=%q", err, got)
	}
	if frame.Type != "result" {
		t.Errorf("frame.Type = %q, want %q", frame.Type, "result")
	}
	if !strings.Contains(string(frame.Data), `"subtype":"success"`) {
		t.Errorf("frame.Data missing subtype; got %q", frame.Data)
	}
}

// TestWrapStreamLine_FallsBackOnMalformedInput asserts unparseable
// input still produces a valid wrapped frame so /stream subscribers
// don't crash on garbage from a misbehaving harness.
func TestWrapStreamLine_FallsBackOnMalformedInput(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{`) // truncated
	got := wrapStreamLine(raw)

	var frame streamFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatalf("fallback output is not valid JSON: %v\noutput=%q", err, got)
	}
	if frame.Type != "raw" {
		t.Errorf("frame.Type = %q, want %q (fallback)", frame.Type, "raw")
	}
	// The raw bytes should be preserved as a string under data.raw.
	var inner struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(frame.Data, &inner); err != nil {
		t.Fatalf("frame.Data is not the {raw: \"...\"} shape: %v", err)
	}
	if inner.Raw != string(raw) {
		t.Errorf("frame.Data.raw = %q, want %q", inner.Raw, raw)
	}
}

// TestWrapStreamLine_FallsBackOnMissingType asserts JSON without a
// top-level "type" string field also falls back to the "raw" envelope.
func TestWrapStreamLine_FallsBackOnMissingType(t *testing.T) {
	raw := []byte(`{"foo":"bar"}`)
	got := wrapStreamLine(raw)

	var frame streamFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput=%q", err, got)
	}
	if frame.Type != "raw" {
		t.Errorf("frame.Type = %q, want %q (no top-level type)", frame.Type, "raw")
	}
}

// TestWrapStreamLine_PreservesTrailingNewline asserts the wrapping
// strips an incoming trailing newline and emits its own — the fanout
// expects line-delimited frames and the inner JSON shouldn't be
// double-newlined.
func TestWrapStreamLine_PreservesTrailingNewline(t *testing.T) {
	raw := []byte(`{"type":"assistant"}` + "\n")
	got := wrapStreamLine(raw)

	if got[len(got)-1] != '\n' {
		t.Errorf("wrapped output missing trailing newline; got %q", got)
	}
	if got[len(got)-2] == '\n' {
		t.Errorf("wrapped output has double trailing newline; got %q", got)
	}
}
