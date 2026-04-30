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
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleSidebarKey processes a KeyMsg when FocusArea==FocusSidebar.
// Pure: returns the next Model without side effects.
func handleSidebarKey(m Model, key tea.KeyMsg) Model {
	switch key.Type {
	case tea.KeyUp, tea.KeyRunes:
		if key.Type == tea.KeyRunes && string(key.Runes) != "k" {
			break
		}
		m = moveSelection(m, -1)
	case tea.KeyDown:
		m = moveSelection(m, +1)
	}
	if key.Type == tea.KeyRunes && string(key.Runes) == "j" {
		m = moveSelection(m, +1)
	}
	return m
}

func moveSelection(m Model, delta int) Model {
	visible := visibleSessions(m)
	if len(visible) == 0 {
		return m
	}
	idx := 0
	for i, n := range visible {
		if n == m.Focused {
			idx = i
		}
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(visible) {
		idx = len(visible) - 1
	}
	m.Focused = visible[idx]
	return m
}

// NewSessionSentinel is a synthetic entry placed at the end of the
// visible sidebar list, representing the "[+ new session]" row. It's
// a valid value of m.Focused — pressing Enter on it opens the
// new-session modal. The string is deliberately unrepresentable as a
// Workspace name (Workspace names are DNS labels: lowercase
// alphanumerics and hyphens) so it can never collide with a real
// session.
const NewSessionSentinel = "__paddock_tui_new_session__"

// visibleSessions returns the SessionOrder names filtered by m.Filter
// (substring match, case-insensitive), with NewSessionSentinel
// appended as the last navigable entry so the user can arrow down to
// the "[+ new session]" row and press Enter to open the modal.
func visibleSessions(m Model) []string {
	out := []string{}
	if m.Filter == "" {
		out = append(out, m.SessionOrder...)
	} else {
		needle := strings.ToLower(m.Filter)
		for _, n := range m.SessionOrder {
			if strings.Contains(strings.ToLower(n), needle) {
				out = append(out, n)
			}
		}
	}
	out = append(out, NewSessionSentinel)
	return out
}
