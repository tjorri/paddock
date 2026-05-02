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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// echoRaw is the on-wire shape the paddock-echo harness emits. Kept
// intentionally thin — this is a fixture, not a harness protocol.
type echoRaw struct {
	Kind         string `json:"kind"`
	Text         string `json:"text,omitempty"`
	Tool         string `json:"tool,omitempty"`
	Path         string `json:"path,omitempty"`
	Summary      string `json:"summary,omitempty"`
	FilesChanged int32  `json:"filesChanged,omitempty"`
	Message      string `json:"message,omitempty"`
}

// convertLine parses one raw JSONL line from paddock-echo and returns
// the equivalent PaddockEvent. Unknown `kind` values are surfaced as
// Type=Message with a diagnostic summary so ingestion is lossless.
func convertLine(line string, now time.Time) (paddockv1alpha1.PaddockEvent, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return paddockv1alpha1.PaddockEvent{}, fmt.Errorf("empty line")
	}
	var raw echoRaw
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return paddockv1alpha1.PaddockEvent{}, fmt.Errorf("parse %q: %w", line, err)
	}

	ev := paddockv1alpha1.PaddockEvent{
		SchemaVersion: "1",
		Timestamp:     metav1.NewTime(now),
	}
	switch raw.Kind {
	case "message":
		ev.Type = "Message"
		ev.Summary = raw.Text
		ev.Fields = map[string]string{"role": "assistant", "content": raw.Text}
	case "tool":
		ev.Type = "ToolUse"
		ev.Summary = raw.Tool
		ev.Fields = map[string]string{"tool": raw.Tool}
		if raw.Path != "" {
			ev.Fields["path"] = raw.Path
		}
	case "result":
		ev.Type = "Result"
		ev.Summary = raw.Summary
		ev.Fields = map[string]string{
			"summary":      raw.Summary,
			"filesChanged": fmt.Sprintf("%d", raw.FilesChanged),
		}
	case "error":
		ev.Type = "Error"
		ev.Summary = raw.Message
		ev.Fields = map[string]string{"message": raw.Message}
	default:
		ev.Type = "Message"
		ev.Summary = fmt.Sprintf("(unknown echo raw kind: %s)", raw.Kind)
	}
	return ev, nil
}
