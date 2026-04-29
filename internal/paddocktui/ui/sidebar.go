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

package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/paddocktui/app"
)

// SidebarView renders the sidebar from the given model. Returns the
// styled string suitable for placement at the left of a JoinHorizontal.
func SidebarView(m app.Model) string {
	rows := make([]string, 0, len(m.SessionOrder)+3)
	rows = append(rows, StyleHeader.Render(fmt.Sprintf("Sessions (%d)", len(m.SessionOrder))))
	for _, name := range visibleNames(m) {
		s := m.Sessions[name]
		row := renderSidebarRow(name, s, name == m.Focused)
		rows = append(rows, row)
	}
	rows = append(rows, "  [+ new session]")
	rows = append(rows, StyleStatusBar.Render(fmt.Sprintf("Active: %d", countActive(m))))
	return StyleSidebarFrame.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

func renderSidebarRow(name string, s *app.SessionState, focused bool) string {
	glyph := " · "
	style := StyleSidebarRowNormal
	if s != nil && s.Session.ActiveRunRef != "" {
		glyph, style = " ▸ ", StyleSidebarRowRunning
	}
	if s != nil && lastRunFailed(s) {
		glyph, style = " ! ", StyleSidebarRowFailed
	}
	if focused {
		style = StyleSidebarRowFocused
	}
	return style.Render(fmt.Sprintf("%s%s", glyph, name))
}

func lastRunFailed(s *app.SessionState) bool {
	if len(s.Runs) == 0 {
		return false
	}
	return s.Runs[0].Phase == paddockv1alpha1.HarnessRunPhaseFailed
}

func countActive(m app.Model) int {
	n := 0
	for _, s := range m.Sessions {
		if s.Session.ActiveRunRef != "" {
			n++
		}
	}
	return n
}

func visibleNames(m app.Model) []string {
	if m.Filter == "" {
		out := make([]string, len(m.SessionOrder))
		copy(out, m.SessionOrder)
		return out
	}
	needle := strings.ToLower(m.Filter)
	out := []string{}
	for _, n := range m.SessionOrder {
		if strings.Contains(strings.ToLower(n), needle) {
			out = append(out, n)
		}
	}
	return out
}
