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

package app

// handlePromptSubmit advances Model on Enter in the prompt input.
//
// Returns:
//   - the next Model with PromptInput cleared
//   - the prompt to submit IMMEDIATELY (empty string when queued)
//
// Slash commands are dispatched separately by handlePromptKey before
// reaching here.
func handlePromptSubmit(m Model) (Model, string) {
	if m.Focused == "" {
		return m, ""
	}
	state := m.Sessions[m.Focused]
	if state == nil {
		return m, ""
	}
	prompt := m.PromptInput
	m.PromptInput = ""
	if prompt == "" {
		return m, ""
	}
	if state.Session.ActiveRunRef != "" {
		state.Queue.Push(prompt)
		return m, ""
	}
	return m, prompt
}
