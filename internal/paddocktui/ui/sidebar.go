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

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/paddocktui/app"
)

// SidebarView renders the sidebar from the given model. Returns the
// styled string suitable for placement at the left of a JoinHorizontal.
//
// The "[+ new session]" row is the last entry in visibleNames (the
// app.NewSessionSentinel value). Rendering it inside the loop — rather
// than as a separately-appended static row — lets it pick up
// selection styling when the user arrows down onto it.
func SidebarView(m app.Model) string {
	rows := make([]string, 0, len(m.SessionOrder)+3)
	rows = append(rows, StyleHeader.Render(fmt.Sprintf("Sessions (%d)", len(m.SessionOrder))))
	for _, name := range visibleNames(m) {
		if name == app.NewSessionSentinel {
			rows = append(rows, renderNewSessionRow(name == m.Focused))
			continue
		}
		s := m.Sessions[name]
		row := renderSidebarRow(name, s, name == m.Focused)
		rows = append(rows, row)
	}
	rows = append(rows, StyleStatusBar.Render(fmt.Sprintf("Active: %d", countActive(m))))
	return StyleSidebarFrame.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// renderNewSessionRow renders the sticky "[+ new session]" entry.
// Indented to match renderSidebarRow's two-space glyph + name shape so
// the column lines up.
func renderNewSessionRow(focused bool) string {
	style := StyleSidebarRowNormal
	if focused {
		style = StyleSidebarRowFocused
	}
	return style.Render("  [+ new session]")
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
	return s.Runs[len(s.Runs)-1].Phase == paddockv1alpha1.HarnessRunPhaseFailed
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

// visibleNames mirrors app.visibleSessions (the sidebar reducer's view
// of the navigable list) so the rendered rows match what arrow keys
// will move through. Always ends with app.NewSessionSentinel.
func visibleNames(m app.Model) []string {
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
	out = append(out, app.NewSessionSentinel)
	return out
}
