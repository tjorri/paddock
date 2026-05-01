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

import (
	tea "github.com/charmbracelet/bubbletea"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// handlePromptSubmit advances Model on Enter in the prompt input.
//
// Returns:
//   - the next Model with PromptInput cleared
//   - a tea.Cmd to execute, or nil when the input was empty or queued
//
// Dispatch is keyed on focused.Mode():
//
//   - SessionBound + turn in flight  → buffer to PendingPrompt (nil cmd)
//   - SessionBound + idle            → submitInteractivePromptCmd
//   - SessionArmed                   → submitRunCmd with Interactive mode;
//     clears Armed
//   - SessionBatch (default)         → queue if a run is in flight, or
//     submitRunCmd with empty mode (preserves pre-Task-24 behaviour
//     exactly for the non-interactive path)
func handlePromptSubmit(m Model) (Model, tea.Cmd) {
	if m.Focused == "" {
		return m, nil
	}
	focused := m.Sessions[m.Focused]
	if focused == nil {
		return m, nil
	}
	prompt := m.PromptInput
	m.PromptInput = ""
	if prompt == "" {
		return m, nil
	}

	switch focused.Mode() {
	case SessionBound:
		// Turn in flight: buffer the prompt so it can be replayed once
		// the broker-side turn completes.
		if focused.Interactive.CurrentTurnSeq != nil {
			m.PendingPrompt = prompt
			return m, nil
		}
		// Idle interactive run: submit via the broker. Guard the nil
		// BrokerClient: until Phase 5 wires it, this dispatch could
		// otherwise panic if a session somehow reaches SessionBound
		// without the broker being connected.
		if m.BrokerClient == nil {
			m.ErrBanner = "broker not connected"
			return m, nil
		}
		return m, submitInteractivePromptCmd(
			m.BrokerClient,
			m.Namespace,
			focused.Interactive.RunName,
			prompt,
			m.Focused,
		)

	case SessionArmed:
		// Kick-off: create the interactive HarnessRun and clear Armed.
		focused.Armed = false
		return m, submitRunCmd(
			m.Client, m.Namespace, m.Focused,
			focused.Session.LastTemplate, prompt,
			paddockv1alpha1.HarnessRunModeInteractive,
		)

	default: // SessionBatch
		// Queue if a batch run is in flight; otherwise submit immediately.
		if focused.Session.ActiveRunRef != "" {
			focused.Queue.Push(prompt)
			return m, nil
		}
		return m, submitRunCmd(
			m.Client, m.Namespace, m.Focused,
			focused.Session.LastTemplate, prompt,
			"",
		)
	}
}
