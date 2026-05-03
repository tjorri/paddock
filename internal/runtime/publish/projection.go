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

import paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"

// Project transforms a full-fidelity PaddockEvent into the
// ConfigMap-bound shape: drops Fields["text"] from PromptSubmitted
// events and Fields["content"] from assistant Message events. All
// other fields and the canonical event metadata (Type, Summary,
// Timestamp, SchemaVersion) are preserved.
//
// The full event remains in the workspace events.jsonl; this is the
// summary view powering Status.RecentEvents.
//
// Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §7.1.
func Project(e paddockv1alpha1.PaddockEvent) paddockv1alpha1.PaddockEvent {
	out := e
	if out.Fields == nil {
		return out
	}
	cleaned := make(map[string]string, len(out.Fields))
	for k, v := range out.Fields {
		if e.Type == "PromptSubmitted" && k == "text" {
			continue
		}
		if e.Type == "Message" && k == "content" && out.Fields["role"] == "assistant" {
			continue
		}
		cleaned[k] = v
	}
	out.Fields = cleaned
	return out
}
