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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Summary cap for text blocks — full content stays in Fields so
// consumers can read it verbatim; Summary is for the one-liner rendering
// in `kubectl paddock events`.
const summaryCap = 200

// claudeStream is the shape of one line from
// `claude -p --output-format stream-json --verbose`. Fields we don't
// know about are permitted and ignored — the schema is not stable
// across Claude Code majors (see ADR-0001 on PaddockEvent.schemaVersion).
type claudeStream struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype,omitempty"`
	Message *claudeMessage `json:"message,omitempty"`

	// Result-event fields.
	Result       string  `json:"result,omitempty"`
	ErrorMessage string  `json:"error,omitempty"`
	IsError      bool    `json:"is_error,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`

	// System init fields.
	Model string   `json:"model,omitempty"`
	Tools []string `json:"tools,omitempty"`
}

type claudeMessage struct {
	Role    string          `json:"role,omitempty"`
	Content []claudeContent `json:"content,omitempty"`
}

// claudeContent is one block inside a message. A single assistant
// message may carry mixed blocks (a text block plus N tool_use
// blocks), each of which becomes its own PaddockEvent.
type claudeContent struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	ToolResult json.RawMessage `json:"content,omitempty"`
}

// convertLine parses one stream-json line and returns the resulting
// PaddockEvents. A single line can produce zero, one, or many events
// (mixed-block assistant messages are the multi-event case).
func convertLine(line string, now time.Time) ([]paddockv1alpha1.PaddockEvent, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("empty line")
	}

	var raw claudeStream
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, fmt.Errorf("parse %q: %w", truncate(line, 120), err)
	}

	ts := metav1.NewTime(now)
	switch raw.Type {
	case "system":
		return []paddockv1alpha1.PaddockEvent{systemEvent(raw, ts)}, nil

	case "assistant":
		if raw.Message == nil {
			return nil, nil
		}
		return assistantEvents(raw.Message, ts), nil

	case "user":
		// tool_result lives on user messages and is already covered by
		// the preceding ToolUse event and the subsequent assistant
		// follow-up. Surfacing it as its own event would be noise.
		return nil, nil

	case "result":
		return []paddockv1alpha1.PaddockEvent{resultEvent(raw, ts)}, nil

	default:
		return []paddockv1alpha1.PaddockEvent{{
			SchemaVersion: "1",
			Timestamp:     ts,
			Type:          "Message",
			Summary:       fmt.Sprintf("(unknown claude-code event: %s)", raw.Type),
			Fields:        map[string]string{"raw": truncate(line, 512)},
		}}, nil
	}
}

func systemEvent(raw claudeStream, ts metav1.Time) paddockv1alpha1.PaddockEvent {
	fields := map[string]string{}
	if raw.Subtype != "" {
		fields["subtype"] = raw.Subtype
	}
	if raw.Model != "" {
		fields["model"] = raw.Model
	}
	if len(raw.Tools) > 0 {
		fields["tools"] = strings.Join(raw.Tools, ",")
	}
	if raw.SessionID != "" {
		fields["sessionId"] = raw.SessionID
	}
	return paddockv1alpha1.PaddockEvent{
		SchemaVersion: "1",
		Timestamp:     ts,
		Type:          "Message",
		Summary:       "claude-code session started",
		Fields:        fields,
	}
}

func assistantEvents(msg *claudeMessage, ts metav1.Time) []paddockv1alpha1.PaddockEvent {
	events := make([]paddockv1alpha1.PaddockEvent, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			events = append(events, paddockv1alpha1.PaddockEvent{
				SchemaVersion: "1",
				Timestamp:     ts,
				Type:          "Message",
				Summary:       truncate(strings.TrimSpace(block.Text), summaryCap),
				Fields: map[string]string{
					"role":    "assistant",
					"content": block.Text,
				},
			})
		case "tool_use":
			fields := map[string]string{"tool": block.Name}
			if block.ID != "" {
				fields["id"] = block.ID
			}
			if summary := summariseToolInput(block.Input); summary != "" {
				fields["input"] = summary
			}
			events = append(events, paddockv1alpha1.PaddockEvent{
				SchemaVersion: "1",
				Timestamp:     ts,
				Type:          "ToolUse",
				Summary:       block.Name,
				Fields:        fields,
			})
		default:
			// Thinking / redacted_thinking / unknown: surface something
			// so the event count matches the block count, but keep it
			// small. Full content is in raw.jsonl on the workspace.
			events = append(events, paddockv1alpha1.PaddockEvent{
				SchemaVersion: "1",
				Timestamp:     ts,
				Type:          "Message",
				Summary:       fmt.Sprintf("(assistant block: %s)", block.Type),
				Fields: map[string]string{
					"role":  "assistant",
					"block": block.Type,
				},
			})
		}
	}
	return events
}

func resultEvent(raw claudeStream, ts metav1.Time) paddockv1alpha1.PaddockEvent {
	summary := raw.Result
	if summary == "" {
		summary = raw.ErrorMessage
	}
	if summary == "" {
		summary = "claude-code run completed"
	}

	fields := map[string]string{}
	if raw.SessionID != "" {
		fields["sessionId"] = raw.SessionID
	}
	if raw.NumTurns > 0 {
		fields["turns"] = fmt.Sprintf("%d", raw.NumTurns)
	}
	if raw.DurationMS > 0 {
		fields["durationMs"] = fmt.Sprintf("%d", raw.DurationMS)
	}
	cost := raw.TotalCostUSD
	if cost == 0 {
		cost = raw.CostUSD
	}
	if cost > 0 {
		fields["costUsd"] = fmt.Sprintf("%.6f", cost)
	}

	evType := "Result"
	if raw.IsError {
		evType = "Error"
	}
	return paddockv1alpha1.PaddockEvent{
		SchemaVersion: "1",
		Timestamp:     ts,
		Type:          evType,
		Summary:       truncate(summary, summaryCap),
		Fields:        fields,
	}
}

// summariseToolInput renders tool_use.input as a compact one-liner,
// prioritising fields harness consumers care most about (file_path,
// command, query) so `kubectl paddock events` shows something useful
// at a glance. Full input is always in raw.jsonl.
func summariseToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return truncate(string(raw), summaryCap)
	}
	for _, k := range []string{"file_path", "path", "command", "query", "pattern"} {
		if v, ok := m[k]; ok {
			return fmt.Sprintf("%s=%v", k, v)
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%v", k, m[k])
	}
	return truncate(b.String(), summaryCap)
}

func truncate(s string, cap int) string {
	if cap <= 0 || len(s) <= cap {
		return s
	}
	return s[:cap] + "…"
}
