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

package publish

import (
	"testing"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestProject_DropsPromptText(t *testing.T) {
	in := paddockv1alpha1.PaddockEvent{
		Type:    "PromptSubmitted",
		Summary: "hello world",
		Fields:  map[string]string{"text": "the full prompt", "length": "15", "hash": "sha256:abc"},
	}
	out := Project(in)
	if _, ok := out.Fields["text"]; ok {
		t.Fatal("Fields[text] must be dropped from PromptSubmitted projection")
	}
	if out.Fields["length"] != "15" || out.Fields["hash"] != "sha256:abc" {
		t.Fatalf("non-text fields must survive projection, got %#v", out.Fields)
	}
	if out.Summary != "hello world" {
		t.Fatalf("summary must survive, got %q", out.Summary)
	}
}

func TestProject_DropsAssistantContent(t *testing.T) {
	in := paddockv1alpha1.PaddockEvent{
		Type:    "Message",
		Summary: "Hi",
		Fields:  map[string]string{"role": "assistant", "content": "Hi there!"},
	}
	out := Project(in)
	if _, ok := out.Fields["content"]; ok {
		t.Fatal("Fields[content] must be dropped from assistant Message projection")
	}
	if out.Fields["role"] != "assistant" {
		t.Fatalf("role must survive, got %#v", out.Fields)
	}
}

func TestProject_LeavesOtherFieldsIntact(t *testing.T) {
	in := paddockv1alpha1.PaddockEvent{
		Type:   "ToolUse",
		Fields: map[string]string{"tool": "Read", "id": "tool-1"},
	}
	out := Project(in)
	if out.Fields["tool"] != "Read" || out.Fields["id"] != "tool-1" {
		t.Fatalf("ToolUse fields must pass through, got %#v", out.Fields)
	}
}
