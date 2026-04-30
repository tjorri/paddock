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

// MainPaneView renders the focused session's run timeline plus the
// prompt input.
//
// height bounds how many lines are visible at once. When the rendered
// content exceeds height, the slice shown is anchored to the BOTTOM
// (so the latest run is always in view by default), shifted upward by
// m.MainScrollFromBottom so the user can review older history with
// PgUp/PgDn. height <= 0 disables clipping (useful for tests that
// don't care about layout).
func MainPaneView(m app.Model, width, height int) string {
	content := mainPaneContent(m)
	if height <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= height {
		return content
	}
	// Clamp scroll-from-bottom so PgUp can't run off the top.
	maxScroll := len(lines) - height
	scroll := m.MainScrollFromBottom
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	bottom := len(lines) - scroll
	top := bottom - height
	if top < 0 {
		top = 0
	}
	if bottom > len(lines) {
		bottom = len(lines)
	}
	return strings.Join(lines[top:bottom], "\n")
}

// mainPaneContent returns the unclipped main-pane content. Splitting
// rendering from clipping keeps the snapshot tests for MainPaneView's
// content stable while still giving the live view a viewport-style
// scrollable region.
func mainPaneContent(m app.Model) string {
	if m.Focused == "" {
		return StyleHeader.Render("(no session selected — pick one in the sidebar or press n to create)")
	}
	if m.Focused == app.NewSessionSentinel {
		return StyleHeader.Render("(press Enter or n to create a new session)")
	}
	s := m.Sessions[m.Focused]
	if s == nil {
		return StyleHeader.Render("(session not loaded)")
	}
	var sections []string
	sections = append(sections, StyleHeader.Render(fmt.Sprintf("%s · %s", s.Session.Name, s.Session.LastTemplate)))
	// Render oldest at the top, newest just above the prompt input —
	// the prompt is the user's anchor at the bottom of the screen so
	// the latest run reads adjacent to where they're typing.
	for i := range s.Runs {
		cursor := m.FocusArea == app.FocusMainPane && i == m.RunCursor
		sections = append(sections, renderRun(s.Runs[i], s.Events[s.Runs[i].Name], cursor))
	}
	prompt := renderPromptArea(m)
	sections = append(sections, prompt)
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func renderRun(r app.RunSummary, events []paddockv1alpha1.PaddockEvent, cursor bool) string {
	headerStyle := StyleRunHeader
	if cursor {
		headerStyle = StyleRunCursor
	}
	header := headerStyle.Render(fmt.Sprintf("╭─ %s · %s ─%s", r.Name, r.StartTime.Format("15:04:05"), strings.Repeat("─", 8)))
	body := make([]string, 0, len(events))
	for _, ev := range events {
		if skipInBody(ev) {
			continue
		}
		body = append(body, prefixBoxLines("│ ", "│ ", renderEvent(ev)))
	}
	footer := StyleRunFooter.Render(fmt.Sprintf("╰─ %s · %s ", phaseLabel(r), durationLabel(r)))
	out := []string{header}
	if r.Prompt != "" {
		// Continuation lines align past the "> " marker so a multi-line
		// prompt reads as one block rather than re-greeting on each row.
		out = append(out, prefixBoxLines("│ > ", "│   ", r.Prompt))
	}
	out = append(out, body...)
	out = append(out, footer)
	return strings.Join(out, "\n")
}

// prefixBoxLines applies first to the opening line and cont to every
// subsequent line of s. Used so a multi-line event summary or prompt
// keeps the run's left vertical bar (│) on every row instead of having
// the box shape break wherever the content embeds a newline.
func prefixBoxLines(first, cont, s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = first + ln
		} else {
			lines[i] = cont + ln
		}
	}
	return strings.Join(lines, "\n")
}

// skipInBody reports whether an event should be omitted from the run's
// body rendering. Result events are filtered: their summary mirrors
// the last Message for narrative-style harnesses (notably claude-code)
// and the run's terminal phase + duration in the footer already
// conveys "the run finished, here's how it ended". Structured outcome
// data lives in HarnessRun.status.outputs, not in the events ring.
func skipInBody(ev paddockv1alpha1.PaddockEvent) bool {
	return ev.Type == "Result"
}

func renderEvent(ev paddockv1alpha1.PaddockEvent) string {
	switch ev.Type {
	case "ToolUse":
		return "• " + ev.Summary
	case "Message":
		return ev.Summary
	case "Error":
		return StyleSidebarRowFailed.Render("⚠ " + ev.Summary)
	default:
		return "  " + ev.Summary
	}
}

func renderPromptArea(m app.Model) string {
	cursor := ""
	if m.FocusArea == app.FocusPrompt {
		cursor = "_"
	}
	return StylePromptArea.Render(fmt.Sprintf("> %s%s", m.PromptInput, cursor))
}

func phaseLabel(r app.RunSummary) string { return string(r.Phase) }
func durationLabel(r app.RunSummary) string {
	if r.CompletionTime.IsZero() || r.StartTime.IsZero() {
		return "..."
	}
	return r.CompletionTime.Sub(r.StartTime).Truncate(1e9).String()
}
