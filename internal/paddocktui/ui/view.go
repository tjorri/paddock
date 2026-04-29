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
	"github.com/charmbracelet/lipgloss"

	"paddock.dev/paddock/internal/paddocktui/app"
)

// View renders the full TUI: sidebar | main pane, with footer status
// bar and an optional modal overlay.
func View(m app.Model, width, height int) string {
	sidebar := SidebarView(m)
	main := MainPaneView(m, width-30)
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
	footer := StyleStatusBar.Render(footerHints(m))
	if m.ErrBanner != "" {
		footer = StyleErrBanner.Render(m.ErrBanner) + "\n" + footer
	}
	composed := lipgloss.JoinVertical(lipgloss.Left, body, footer)
	switch m.Modal {
	case app.ModalNew:
		return overlay(composed, NewSessionModalView(m.ModalNew))
	case app.ModalEnd:
		return overlay(composed, EndSessionModalView(m.ModalEnd))
	case app.ModalHelp:
		return overlay(composed, HelpModalView())
	}
	return composed
}

func footerHints(m app.Model) string {
	switch m.FocusArea {
	case app.FocusSidebar:
		return "↑↓ select · Enter focus · n new · e end · / search · q quit · ? help"
	case app.FocusPrompt:
		return "Enter submit · Esc unfocus · :help · Ctrl-X cancel run"
	}
	return ""
}

// overlay places the modal in the centre of the composed view. Naive
// implementation: append after the body. A fancier overlay using
// lipgloss.Place can replace this later without changing callers.
func overlay(body, modal string) string {
	return body + "\n\n" + modal
}
