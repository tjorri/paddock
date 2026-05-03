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
	"strings"
	"testing"
	"time"
)

func TestConvertLine_SystemInit(t *testing.T) {
	now := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	evs, err := convertLine(`{"type":"system","subtype":"init","session_id":"s1","model":"claude-sonnet-4-6","tools":["Read","Edit","Bash"]}`, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "Message" {
		t.Errorf("type = %q, want Message", ev.Type)
	}
	if ev.Summary != "claude-code session started" {
		t.Errorf("summary = %q", ev.Summary)
	}
	if ev.Fields["subtype"] != "init" {
		t.Errorf("subtype = %q", ev.Fields["subtype"])
	}
	if ev.Fields["model"] != "claude-sonnet-4-6" {
		t.Errorf("model = %q", ev.Fields["model"])
	}
	if ev.Fields["tools"] != "Read,Edit,Bash" {
		t.Errorf("tools = %q", ev.Fields["tools"])
	}
}

func TestConvertLine_AssistantText(t *testing.T) {
	now := time.Date(2026, 4, 22, 10, 0, 1, 0, time.UTC)
	evs, err := convertLine(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll help you with that."}]}}`, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "Message" {
		t.Errorf("type = %q", ev.Type)
	}
	if ev.Summary != "I'll help you with that." {
		t.Errorf("summary = %q", ev.Summary)
	}
	if ev.Fields["role"] != "assistant" {
		t.Errorf("role = %q", ev.Fields["role"])
	}
	if ev.Fields["content"] != "I'll help you with that." {
		t.Errorf("content = %q", ev.Fields["content"])
	}
}

func TestConvertLine_AssistantToolUse(t *testing.T) {
	now := time.Date(2026, 4, 22, 10, 0, 2, 0, time.UTC)
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"auth.py"}}]}}`
	evs, err := convertLine(line, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "ToolUse" {
		t.Errorf("type = %q", ev.Type)
	}
	if ev.Summary != "Read" {
		t.Errorf("summary = %q", ev.Summary)
	}
	if ev.Fields["tool"] != "Read" {
		t.Errorf("tool = %q", ev.Fields["tool"])
	}
	if ev.Fields["id"] != "t1" {
		t.Errorf("id = %q", ev.Fields["id"])
	}
	if ev.Fields["input"] != "file_path=auth.py" {
		t.Errorf("input = %q, want 'file_path=auth.py'", ev.Fields["input"])
	}
}

func TestConvertLine_AssistantMixedContent(t *testing.T) {
	now := time.Date(2026, 4, 22, 10, 0, 3, 0, time.UTC)
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Let me read it first"},{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"x.py"}},{"type":"tool_use","id":"t3","name":"Grep","input":{"pattern":"auth"}}]}}`
	evs, err := convertLine(line, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3 (1 text + 2 tool_use)", len(evs))
	}
	want := []struct{ typ, summary string }{
		{"Message", "Let me read it first"},
		{"ToolUse", "Read"},
		{"ToolUse", "Grep"},
	}
	for i, w := range want {
		if evs[i].Type != w.typ {
			t.Errorf("evs[%d].type = %q, want %q", i, evs[i].Type, w.typ)
		}
		if evs[i].Summary != w.summary {
			t.Errorf("evs[%d].summary = %q, want %q", i, evs[i].Summary, w.summary)
		}
	}
}

func TestConvertLine_UserSkipsToolResult(t *testing.T) {
	now := time.Date(2026, 4, 22, 10, 0, 4, 0, time.UTC)
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"42"}]}}`
	evs, err := convertLine(line, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("user tool_result should emit 0 events; got %d", len(evs))
	}
}

func TestConvertLine_ResultSuccess(t *testing.T) {
	now := time.Date(2026, 4, 22, 10, 0, 5, 0, time.UTC)
	line := `{"type":"result","subtype":"success","is_error":false,"cost_usd":0.0123,"total_cost_usd":0.0456,"duration_ms":4500,"num_turns":3,"result":"Done. Implemented async auth.","session_id":"s1"}`
	evs, err := convertLine(line, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Type != "Result" {
		t.Errorf("type = %q, want Result", ev.Type)
	}
	if ev.Summary != "Done. Implemented async auth." {
		t.Errorf("summary = %q", ev.Summary)
	}
	if ev.Fields["turns"] != "3" {
		t.Errorf("turns = %q", ev.Fields["turns"])
	}
	if ev.Fields["durationMs"] != "4500" {
		t.Errorf("durationMs = %q", ev.Fields["durationMs"])
	}
	// Prefer total_cost_usd over cost_usd.
	if ev.Fields["costUsd"] != "0.045600" {
		t.Errorf("costUsd = %q, want 0.045600", ev.Fields["costUsd"])
	}
	if ev.Fields["sessionId"] != "s1" {
		t.Errorf("sessionId = %q", ev.Fields["sessionId"])
	}
}

func TestConvertLine_ResultError(t *testing.T) {
	now := time.Date(2026, 4, 22, 10, 0, 6, 0, time.UTC)
	line := `{"type":"result","is_error":true,"error":"rate limited","duration_ms":100}`
	evs, err := convertLine(line, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "Error" {
		t.Fatalf("expected 1 Error event; got %+v", evs)
	}
	if evs[0].Summary != "rate limited" {
		t.Errorf("summary = %q", evs[0].Summary)
	}
}

// TestConvertLine_ResultError_BillingShape locks in the exact shape
// Claude Code 2.1.117 emits for a billing error: subtype="success"
// is misleading, is_error=true is the authoritative failure flag,
// and the human message lives on .result (not .error).
func TestConvertLine_ResultError_BillingShape(t *testing.T) {
	now := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	line := `{"type":"result","subtype":"success","is_error":true,"api_error_status":400,"duration_ms":199,"num_turns":1,"result":"Credit balance is too low","session_id":"s1","total_cost_usd":0}`
	evs, err := convertLine(line, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Type != "Error" {
		t.Errorf("type = %q, want Error — subtype=success is misleading; is_error is the authority", evs[0].Type)
	}
	if evs[0].Summary != "Credit balance is too low" {
		t.Errorf("summary = %q — must surface .result, not fall back to generic text", evs[0].Summary)
	}
}

func TestConvertLine_UnknownType(t *testing.T) {
	now := time.Date(2026, 4, 22, 10, 0, 7, 0, time.UTC)
	evs, err := convertLine(`{"type":"hypothetical_future_type","foo":"bar"}`, now)
	if err != nil {
		t.Fatalf("convertLine: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1 (lossy pass-through)", len(evs))
	}
	if !strings.Contains(evs[0].Summary, "hypothetical_future_type") {
		t.Errorf("summary should mention unknown type; got %q", evs[0].Summary)
	}
	if evs[0].Fields["raw"] == "" {
		t.Error("fields.raw should carry the original line")
	}
}

func TestConvertLine_EmptyErrors(t *testing.T) {
	_, err := convertLine("   ", time.Now())
	if err == nil {
		t.Fatal("empty line should error")
	}
	_, err = convertLine(`{malformed`, time.Now())
	if err == nil {
		t.Fatal("malformed JSON should error")
	}
}

func TestSummariseToolInput_PrefersKnownKeys(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"file_path":"x.py","limit":10}`, "file_path=x.py"},
		{`{"command":"ls -la","cwd":"/tmp"}`, "command=ls -la"},
		{`{"pattern":"foo"}`, "pattern=foo"},
		{`{"query":"async"}`, "query=async"},
	}
	for _, tc := range cases {
		got := summariseToolInput([]byte(tc.in))
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSummariseToolInput_FallsBackToSortedKeys(t *testing.T) {
	got := summariseToolInput([]byte(`{"b":2,"a":1}`))
	if got != "a=1 b=2" {
		t.Errorf("got %q, want 'a=1 b=2'", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hi", 10); got != "hi" {
		t.Errorf("short truncate = %q", got)
	}
	got := truncate(strings.Repeat("x", 250), 200)
	if len(got) != 200+len("…") {
		t.Errorf("truncate length = %d, want 200 + ellipsis", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate should end with ellipsis")
	}
}
