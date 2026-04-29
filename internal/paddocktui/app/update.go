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
	"fmt"
	"sort"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/api/resource"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	pdkruns "paddock.dev/paddock/internal/paddocktui/runs"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

// errNoSessionFocused is the user-facing banner shown when an action
// that needs a focused session is invoked while m.Focused is empty.
const errNoSessionFocused = "no session focused"

// Update dispatches messages to per-area handlers and returns the next
// Model + a tea.Cmd. Watch commands re-issue themselves on every
// message they produce so streams stay alive.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case sessionsLoadedMsg:
		for _, s := range msg.Sessions {
			m = upsertSession(m, s)
		}
		// After initial load, focus the first session if any. If a
		// session is now focused and we don't have a run-watch open
		// for it yet, open one.
		if m.Focused == "" && len(m.SessionOrder) > 0 {
			m.Focused = m.SessionOrder[0]
		}
		if cmd := ensureRunWatch(&m, m.Focused); cmd != nil {
			return m, cmd
		}
		return m, nil

	case sessionWatchOpenedMsg:
		m.sessionWatchCh = msg.Ch
		return m, nextSessionEventCmd(m.sessionWatchCh)

	case runWatchOpenedMsg:
		if m.runWatches == nil {
			m.runWatches = map[string]<-chan pdkruns.Event{}
		}
		m.runWatches[msg.WorkspaceRef] = msg.Ch
		return m, nextRunEventCmd(msg.WorkspaceRef, msg.Ch)

	case eventTailOpenedMsg:
		if m.eventTails == nil {
			m.eventTails = map[string]<-chan paddockv1alpha1.PaddockEvent{}
		}
		m.eventTails[msg.RunName] = msg.Ch
		return m, nextEventTailCmd(msg.RunName, msg.Ch)

	case sessionAddedMsg:
		m = upsertSession(m, msg.Session)
		return m, nextSessionEventCmd(m.sessionWatchCh)

	case sessionUpdatedMsg:
		m = upsertSession(m, msg.Session)
		return m, nextSessionEventCmd(m.sessionWatchCh)

	case sessionDeletedMsg:
		delete(m.Sessions, msg.Name)
		m.SessionOrder = removeFromOrder(m.SessionOrder, msg.Name)
		if m.Focused == msg.Name {
			m.Focused = ""
		}
		return m, nextSessionEventCmd(m.sessionWatchCh)

	case runUpdatedMsg:
		m = upsertRun(m, msg)
		ch := m.runWatches[msg.WorkspaceRef]
		return m, nextRunEventCmd(msg.WorkspaceRef, ch)

	case runDeletedMsg:
		m = removeRun(m, msg)
		ch := m.runWatches[msg.WorkspaceRef]
		return m, nextRunEventCmd(msg.WorkspaceRef, ch)

	case eventReceivedMsg:
		m = appendEvent(m, msg)
		ch := m.eventTails[msg.RunName]
		return m, nextEventTailCmd(msg.RunName, ch)

	case runCreatedMsg:
		// Open a long-lived event tail for this new run; the
		// workspace-runs watch will pick up the run object itself.
		return m, openEventTailCmd(m.ctx, m.Client, m.Namespace, msg.RunName)

	case runCancelledMsg:
		return m, nil

	case errMsg:
		m.ErrBanner = msg.Err.Error()
		return m, nil

	case tea.KeyMsg:
		return handleKeyMsg(m, msg)
	}
	return m, nil
}

// ensureRunWatch opens a run watch for workspaceRef if one isn't
// already open. Returns the open command, or nil when nothing needs
// opening (empty ref or watch already present). Cleanup of stale
// run watches on focus change is intentionally out of scope here —
// see code-review fix-up for d5e44f3.
func ensureRunWatch(m *Model, workspaceRef string) tea.Cmd {
	if workspaceRef == "" {
		return nil
	}
	if m.runWatches == nil {
		m.runWatches = map[string]<-chan pdkruns.Event{}
	}
	if _, ok := m.runWatches[workspaceRef]; ok {
		return nil
	}
	return openRunWatchCmd(m.ctx, m.Client, m.Namespace, workspaceRef)
}

// handleKeyMsg routes key events to modal handlers (which take priority)
// or per-focus-area handlers.
func handleKeyMsg(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.Modal != ModalNone {
		return handleModalKey(m, key)
	}
	switch m.FocusArea {
	case FocusSidebar:
		return handleSidebarFocusKey(m, key)
	case FocusPrompt:
		return handlePromptFocusKey(m, key)
	}
	return m, nil
}

// handleSidebarFocusKey maps top-level keystrokes when the sidebar
// holds focus.
func handleSidebarFocusKey(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Type == tea.KeyRunes && string(key.Runes) == "q":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case key.Type == tea.KeyRunes && string(key.Runes) == "n":
		// Open new-session modal; template list populated separately
		// by a ModalOpen-side command in a later task.
		return openNewSessionModal(m, []string{}), nil
	case key.Type == tea.KeyRunes && string(key.Runes) == "e":
		if m.Focused == "" {
			m.ErrBanner = errNoSessionFocused
			return m, nil
		}
		return openEndSessionModal(m, m.Focused), nil
	case key.Type == tea.KeyTab:
		m.FocusArea = FocusPrompt
		return m, nil
	case key.Type == tea.KeyRunes && string(key.Runes) == "?":
		return openHelpModal(m), nil
	}
	m = handleSidebarKey(m, key)
	cmd := ensureRunWatch(&m, m.Focused)
	return m, cmd
}

// handlePromptFocusKey edits the prompt buffer and dispatches Enter.
func handlePromptFocusKey(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.FocusArea = FocusSidebar
		return m, nil
	case tea.KeyTab:
		m.FocusArea = FocusSidebar
		return m, nil
	case tea.KeyEnter:
		// Slash command? Dispatch.
		cmd, arg, ok := ParseSlash(m.PromptInput)
		if ok {
			next, ext := dispatchSlash(m, cmd, arg)
			next.PromptInput = ""
			return next, ext
		}
		next, prompt := handlePromptSubmit(m)
		if prompt == "" {
			return next, nil
		}
		focused := next.Sessions[next.Focused]
		template := focused.Session.LastTemplate
		return next, submitRunCmd(next.Client, next.Namespace, next.Focused, template, prompt)
	case tea.KeyRunes:
		m.PromptInput += string(key.Runes)
		return m, nil
	case tea.KeyBackspace:
		if len(m.PromptInput) > 0 {
			m.PromptInput = m.PromptInput[:len(m.PromptInput)-1]
		}
		return m, nil
	}
	return m, nil
}

// handleModalKey routes keys to the active modal's handler and turns
// modal-confirmed events into the appropriate cluster commands.
func handleModalKey(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Modal {
	case ModalNew:
		m = handleNewSessionModalKey(m, key)
		// On Enter on the last field, submit. We snapshot inputs before
		// closeModal nils ModalNew.
		if key.Type == tea.KeyEnter && m.ModalNew != nil && m.ModalNew.Field == 3 {
			snapshot := *m.ModalNew
			storage, err := parseQuantity(snapshot.StorageInput)
			if err != nil {
				// Surface the bad input and keep the modal open so the
				// user can fix it. Do NOT closeModal — that would
				// silently drop their entries.
				m.ErrBanner = fmt.Sprintf("invalid storage size: %v", err)
				return m, nil
			}
			template := ""
			if snapshot.TemplateIdx >= 0 && snapshot.TemplateIdx < len(snapshot.TemplatePicks) {
				template = snapshot.TemplatePicks[snapshot.TemplateIdx]
			}
			cmd := createSessionCmd(m.Client, m.Namespace,
				snapshot.NameInput, template, storage, snapshot.SeedRepoInput)
			m = closeModal(m)
			return m, cmd
		}
		return m, nil
	case ModalEnd:
		next, confirmed := handleEndSessionModalKey(m, key)
		if confirmed {
			cmd := endSessionCmd(m.Client, m.Namespace, m.ModalEnd.TargetName)
			return next, cmd
		}
		return next, nil
	case ModalHelp:
		if key.Type == tea.KeyEsc || (key.Type == tea.KeyRunes && string(key.Runes) == "?") {
			return closeModal(m), nil
		}
		return m, nil
	}
	return m, nil
}

// dispatchSlash maps recognised slash commands to side-effect commands
// or in-Model state changes.
func dispatchSlash(m Model, cmd SlashCmd, arg string) (Model, tea.Cmd) {
	switch cmd {
	case SlashCancel:
		if m.Focused == "" {
			m.ErrBanner = errNoSessionFocused
			return m, nil
		}
		focused := m.Sessions[m.Focused]
		if focused == nil {
			m.ErrBanner = errNoSessionFocused
			return m, nil
		}
		if focused.Session.ActiveRunRef != "" {
			return m, cancelRunCmd(m.Client, m.Namespace, focused.Session.ActiveRunRef)
		}
	case SlashHelp:
		return openHelpModal(m), nil
	case SlashTemplate:
		if arg == "" {
			m.ErrBanner = ":template requires a template name"
			return m, nil
		}
		if m.Focused == "" {
			m.ErrBanner = errNoSessionFocused
			return m, nil
		}
		focused := m.Sessions[m.Focused]
		if focused == nil {
			m.ErrBanner = errNoSessionFocused
			return m, nil
		}
		focused.Session.LastTemplate = arg
		// Persist via annotation patch so reattach restores the
		// override.
		return m, patchLastTemplateCmd(m.Client, m.Namespace, m.Focused, arg)
	case SlashInteractive:
		m.ErrBanner = "interactive mode is not yet implemented"
	}
	return m, nil
}

// upsertSession inserts a new session or refreshes the projection of
// an existing one. SessionOrder is sorted lexically — activity-based
// sorting can be layered on later.
func upsertSession(m Model, s pdksession.Session) Model {
	if _, exists := m.Sessions[s.Name]; !exists {
		m.SessionOrder = append(m.SessionOrder, s.Name)
		sort.Strings(m.SessionOrder)
		m.Sessions[s.Name] = &SessionState{
			Session: s,
			Events:  map[string][]paddockv1alpha1.PaddockEvent{},
		}
	} else {
		m.Sessions[s.Name].Session = s
	}
	return m
}

// upsertRun inserts or updates the RunSummary for the run carried in
// msg under the workspace it belongs to.
func upsertRun(m Model, msg runUpdatedMsg) Model {
	state := m.Sessions[msg.WorkspaceRef]
	if state == nil {
		return m
	}
	summary := runSummaryFromCR(msg.Run)
	for i := range state.Runs {
		if state.Runs[i].Name == summary.Name {
			state.Runs[i] = summary
			return m
		}
	}
	state.Runs = append(state.Runs, summary)
	return m
}

// removeRun drops the named run from its workspace's Runs slice.
func removeRun(m Model, msg runDeletedMsg) Model {
	state := m.Sessions[msg.WorkspaceRef]
	if state == nil {
		return m
	}
	out := state.Runs[:0]
	for _, r := range state.Runs {
		if r.Name != msg.Name {
			out = append(out, r)
		}
	}
	state.Runs = out
	return m
}

// appendEvent appends an event to the focused session's Events map.
// Only the focused session is tailed, so unfocused sessions are a
// no-op.
func appendEvent(m Model, msg eventReceivedMsg) Model {
	if m.Focused == "" {
		return m
	}
	state := m.Sessions[m.Focused]
	if state == nil {
		return m
	}
	if state.Events == nil {
		state.Events = map[string][]paddockv1alpha1.PaddockEvent{}
	}
	state.Events[msg.RunName] = append(state.Events[msg.RunName], msg.Event)
	return m
}

// removeFromOrder returns a new slice with the named entry removed.
func removeFromOrder(slice []string, name string) []string {
	out := make([]string, 0, len(slice))
	for _, n := range slice {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

// parseQuantity wraps resource.ParseQuantity so callers can stay
// decoupled from k8s.io/apimachinery imports.
func parseQuantity(s string) (resource.Quantity, error) {
	return resource.ParseQuantity(s)
}

// runSummaryFromCR projects a HarnessRun into the TUI-shaped
// RunSummary used by the View layer.
func runSummaryFromCR(hr paddockv1alpha1.HarnessRun) RunSummary {
	r := RunSummary{
		Name:     hr.Name,
		Phase:    hr.Status.Phase,
		Prompt:   hr.Spec.Prompt,
		Template: hr.Spec.TemplateRef.Name,
	}
	if hr.Status.StartTime != nil {
		r.StartTime = hr.Status.StartTime.Time
	}
	if hr.Status.CompletionTime != nil {
		r.CompletionTime = hr.Status.CompletionTime.Time
	}
	return r
}
