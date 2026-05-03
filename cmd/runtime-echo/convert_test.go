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
	"testing"
	"time"
)

func TestConvertLine(t *testing.T) {
	now := time.Date(2026, 4, 22, 14, 3, 22, 0, time.UTC)

	cases := []struct {
		name       string
		line       string
		wantErr    bool
		wantType   string
		wantSum    string
		wantFields map[string]string
	}{
		{
			name:     "message",
			line:     `{"kind":"message","text":"hello"}`,
			wantType: "Message",
			wantSum:  "hello",
			wantFields: map[string]string{
				"role":    "assistant",
				"content": "hello",
			},
		},
		{
			name:     "tool with path",
			line:     `{"kind":"tool","tool":"read","path":"foo.txt"}`,
			wantType: "ToolUse",
			wantSum:  "read",
			wantFields: map[string]string{
				"tool": "read",
				"path": "foo.txt",
			},
		},
		{
			name:     "tool without path",
			line:     `{"kind":"tool","tool":"search"}`,
			wantType: "ToolUse",
			wantSum:  "search",
			wantFields: map[string]string{
				"tool": "search",
			},
		},
		{
			name:     "result",
			line:     `{"kind":"result","summary":"done","filesChanged":3}`,
			wantType: "Result",
			wantSum:  "done",
			wantFields: map[string]string{
				"summary":      "done",
				"filesChanged": "3",
			},
		},
		{
			name:     "error",
			line:     `{"kind":"error","message":"boom"}`,
			wantType: "Error",
			wantSum:  "boom",
			wantFields: map[string]string{
				"message": "boom",
			},
		},
		{
			name:     "unknown kind is lossless Message",
			line:     `{"kind":"mystery"}`,
			wantType: "Message",
			wantSum:  "(unknown echo raw kind: mystery)",
		},
		{
			name:    "empty line errors",
			line:    "   ",
			wantErr: true,
		},
		{
			name:    "invalid JSON errors",
			line:    `{not json`,
			wantErr: true,
		},
		{
			name:     "trailing whitespace trimmed",
			line:     "  {\"kind\":\"message\",\"text\":\"x\"}\n",
			wantType: "Message",
			wantSum:  "x",
			wantFields: map[string]string{
				"role":    "assistant",
				"content": "x",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := convertLine(tc.line, now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", ev)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ev.SchemaVersion != "1" {
				t.Errorf("schemaVersion = %q, want 1", ev.SchemaVersion)
			}
			if ev.Type != tc.wantType {
				t.Errorf("type = %q, want %q", ev.Type, tc.wantType)
			}
			if ev.Summary != tc.wantSum {
				t.Errorf("summary = %q, want %q", ev.Summary, tc.wantSum)
			}
			if !ev.Timestamp.Time.Equal(now) {
				t.Errorf("timestamp = %v, want %v", ev.Timestamp.Time, now)
			}
			for k, v := range tc.wantFields {
				if got := ev.Fields[k]; got != v {
					t.Errorf("fields[%q] = %q, want %q", k, got, v)
				}
			}
			if tc.wantFields != nil && len(ev.Fields) != len(tc.wantFields) {
				t.Errorf("fields = %v, want %v", ev.Fields, tc.wantFields)
			}
		})
	}
}
