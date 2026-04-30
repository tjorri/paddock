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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	paddockbroker "paddock.dev/paddock/internal/paddocktui/broker"
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
		prevActive := previousActiveRunRef(m, msg.Session.Name)
		m = upsertSession(m, msg.Session)
		next, drained := drainQueueIfFreed(m, msg.Session.Name, prevActive, msg.Session.ActiveRunRef)
		if drained != nil {
			return next, tea.Batch(nextSessionEventCmd(m.sessionWatchCh), drained)
		}
		return next, nextSessionEventCmd(m.sessionWatchCh)

	case sessionUpdatedMsg:
		prevActive := previousActiveRunRef(m, msg.Session.Name)
		m = upsertSession(m, msg.Session)
		next, drained := drainQueueIfFreed(m, msg.Session.Name, prevActive, msg.Session.ActiveRunRef)
		if drained != nil {
			return next, tea.Batch(nextSessionEventCmd(m.sessionWatchCh), drained)
		}
		return next, nextSessionEventCmd(m.sessionWatchCh)

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
		// On reattach, the TUI sees in-flight HarnessRuns it didn't
		// create itself. Open an event tail for any non-terminal run
		// without one so the timeline isn't blank.
		if tailCmd := ensureEventTail(&m, msg.Run); tailCmd != nil {
			return m, tea.Batch(nextRunEventCmd(msg.WorkspaceRef, ch), tailCmd)
		}
		return m, nextRunEventCmd(msg.WorkspaceRef, ch)

	case runDeletedMsg:
		m = removeRun(m, msg)
		// Allow the per-run tail goroutine to be reaped on quit by
		// dropping the channel reference; the goroutine itself exits
		// when ctx cancels.
		delete(m.eventTails, msg.Name)
		ch := m.runWatches[msg.WorkspaceRef]
		return m, nextRunEventCmd(msg.WorkspaceRef, ch)

	case eventReceivedMsg:
		m = appendEvent(m, msg)
		ch := m.eventTails[msg.RunName]
		return m, nextEventTailCmd(msg.RunName, ch)

	case templatesLoadedMsg:
		m.availableTemplates = msg.Templates
		// If the new-session modal is currently open with no picks
		// (it was opened before the initial template load completed),
		// patch the picks in so the user doesn't have to reopen.
		if m.Modal == ModalNew && m.ModalNew != nil && len(m.ModalNew.TemplatePicks) == 0 {
			m.ModalNew.TemplatePicks = templateNames(msg.Templates)
		}
		return m, nil

	case runCreatedMsg:
		// No tail opened here. The runs watch will fire runUpdatedMsg
		// for the new run within one poll interval, and ensureEventTail
		// in that path opens the tail. Opening here too races with
		// runUpdatedMsg (the eventTailOpenedMsg registration is async)
		// and produces duplicate tails — every event was being emitted
		// twice.
		//
		// For Interactive runs we arm the session binding and open the
		// WebSocket stream. The binding is set even when BrokerClient is
		// nil (so Reattach in Task 28 can find it); stream opening is
		// skipped and a banner is surfaced instead.
		if msg.Mode != paddockv1alpha1.HarnessRunModeInteractive {
			return m, nil
		}
		state := m.Sessions[msg.WorkspaceRef]
		if state == nil {
			// Session was deleted in the brief window between the run
			// being created and this message arriving — don't crash.
			return m, nil
		}
		state.Interactive = &InteractiveBinding{RunName: msg.RunName}
		if m.BrokerClient == nil {
			m.ErrBanner = "broker client not configured — cannot open interactive stream"
			return m, nil
		}
		return m, openInteractiveStreamCmd(m.ctx, m.BrokerClient, m.Namespace, msg.RunName)

	case runCancelledMsg:
		return m, nil

	case interactiveStreamOpenedMsg:
		if m.interactiveFrames == nil {
			m.interactiveFrames = map[string]<-chan paddockbroker.StreamFrame{}
		}
		m.interactiveFrames[msg.RunName] = msg.Ch
		return m, nextInteractiveFrameCmd(msg.RunName, msg.Ch)

	case interactiveFrameMsg:
		state := m.Sessions[m.Focused]
		if state == nil || state.Interactive == nil || state.Interactive.RunName != msg.RunName {
			return m, nil
		}
		ev := frameToEvent(msg.Frame)
		if state.Events == nil {
			state.Events = map[string][]paddockv1alpha1.PaddockEvent{}
		}
		state.Events[msg.RunName] = appendEventDedup(state.Events[msg.RunName], ev)
		state.Interactive.LastFrameAt = time.Now()
		return m, nextInteractiveFrameCmd(msg.RunName, m.interactiveFrames[msg.RunName])

	case interactiveStreamClosedMsg:
		delete(m.interactiveFrames, msg.RunName)
		return m, nil

	case errMsg:
		m.ErrBanner = msg.Err.Error()
		return m, nil

	case tea.KeyMsg:
		return handleKeyMsg(m, msg)

	case tea.MouseMsg:
		return handleMouseMsg(m, msg)
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
// or per-focus-area handlers. Ctrl-C is handled globally so the user
// can always escape, regardless of focus or modal state. PgUp/PgDn/
// Home/End scroll the main pane and are also global — the user wants
// to review old runs whether they're typing or navigating sessions.
func handleKeyMsg(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Type == tea.KeyCtrlC {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	switch key.Type {
	case tea.KeyPgUp:
		m.MainScrollFromBottom += mainScrollStep
		return m, nil
	case tea.KeyPgDown:
		m.MainScrollFromBottom -= mainScrollStep
		if m.MainScrollFromBottom < 0 {
			m.MainScrollFromBottom = 0
		}
		return m, nil
	case tea.KeyHome:
		// Snap to top (very large value; render-time clamp brings it
		// down to len(lines)-visibleHeight).
		m.MainScrollFromBottom = mainScrollSnapTop
		return m, nil
	case tea.KeyEnd:
		m.MainScrollFromBottom = 0
		return m, nil
	}
	// Palette claims all keys when already open (must run before the
	// modal gate so Esc/Enter inside the palette aren't routed to a
	// background modal).
	if m.Palette.Open() {
		return handlePaletteKey(m, key)
	}
	if m.Modal != ModalNone {
		return handleModalKey(m, key)
	}
	// Palette opener triggers — only when no modal is up: ":" on an
	// empty prompt or Ctrl-K anywhere.
	if key.Type == tea.KeyCtrlK {
		m.Palette = m.Palette.WithOpen(true)
		return m, nil
	}
	if key.Type == tea.KeyRunes && len(key.Runes) == 1 && key.Runes[0] == ':' {
		if m.PromptInput == "" {
			m.Palette = m.Palette.WithOpen(true)
		} else {
			m.PromptInput += ":"
		}
		return m, nil
	}
	// Global Tab cycle: Prompt → Sidebar → MainPane → Prompt.
	if key.Type == tea.KeyTab {
		switch m.FocusArea {
		case FocusPrompt:
			m.FocusArea = FocusSidebar
		case FocusSidebar:
			m.FocusArea = FocusMainPane
		case FocusMainPane:
			m.FocusArea = FocusPrompt
		}
		return m, nil
	}
	// Arrow-key run navigation when MainPane holds focus.
	if m.FocusArea == FocusMainPane {
		state := m.Sessions[m.Focused]
		if state == nil {
			return m, nil
		}
		if key.Type == tea.KeyDown && m.RunCursor+1 < len(state.Runs) {
			m.RunCursor++
			return m, nil
		}
		if key.Type == tea.KeyUp && m.RunCursor > 0 {
			m.RunCursor--
			return m, nil
		}
		return m, nil
	}
	switch m.FocusArea {
	case FocusSidebar:
		return handleSidebarFocusKey(m, key)
	case FocusPrompt:
		return handlePromptFocusKey(m, key)
	}
	return m, nil
}

// handleMouseMsg routes mouse events. Currently only the scroll wheel
// is wired — wheel up/down moves the main pane offset by mainWheelStep
// lines per tick. Mouse mode is enabled at program-init via
// tea.WithMouseCellMotion in cmd/tui.go.
func handleMouseMsg(m Model, msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.MainScrollFromBottom += mainWheelStep
		return m, nil
	case tea.MouseButtonWheelDown:
		m.MainScrollFromBottom -= mainWheelStep
		if m.MainScrollFromBottom < 0 {
			m.MainScrollFromBottom = 0
		}
		return m, nil
	}
	return m, nil
}

const (
	// mainScrollStep is how many lines PgUp/PgDn moves the main pane.
	// Roughly half a typical 24-row terminal so the user keeps a few
	// lines of overlap as anchor.
	mainScrollStep = 10

	// mainWheelStep is how many lines a single mouse-wheel tick moves
	// the main pane. Smaller than mainScrollStep so wheel feels
	// fine-grained next to the bigger keyboard step.
	mainWheelStep = 3

	// mainScrollSnapTop is a sentinel offset for "scroll to the top".
	// MainPaneView clamps to len(lines)-visibleHeight at render time,
	// so this just needs to be larger than any plausible run history.
	mainScrollSnapTop = 1 << 20
)

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
		// Open new-session modal with the cached template list and
		// kick off a refresh so any HarnessTemplates added since the
		// TUI started show up. A pending templatesLoadedMsg patches
		// the modal in place if the picks are still empty.
		return openNewSessionModal(m, templateNames(m.availableTemplates)),
			loadTemplatesCmd(m.Client, m.Namespace)
	case key.Type == tea.KeyRunes && string(key.Runes) == "e":
		if m.Focused == "" || m.Focused == NewSessionSentinel {
			m.ErrBanner = errNoSessionFocused
			return m, nil
		}
		return openEndSessionModal(m, m.Focused), nil
	case key.Type == tea.KeyEnter:
		// Enter on the [+ new session] sentinel row opens the new-session
		// modal — same behaviour as pressing 'n', so a user who arrowed
		// down to the sticky row at the bottom of the sidebar doesn't
		// also have to learn a separate keybinding. Enter on a real
		// session moves focus to the prompt input so the user can start
		// typing immediately.
		if m.Focused == NewSessionSentinel {
			return openNewSessionModal(m, templateNames(m.availableTemplates)),
				loadTemplatesCmd(m.Client, m.Namespace)
		}
		if m.Focused != "" {
			m.FocusArea = FocusPrompt
		}
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
	case tea.KeyEnter:
		return handlePromptSubmit(m)
	case tea.KeyRunes:
		m.PromptInput += string(key.Runes)
		return m, nil
	case tea.KeySpace:
		m.PromptInput += " "
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

// handlePaletteKey routes keystrokes while the palette is open: Esc
// closes; Enter executes the parsed command; Backspace edits;
// printable runes append to the input. Tab autocompletes the unique
// matching command name when exactly one matches.
func handlePaletteKey(m Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.Palette = m.Palette.WithOpen(false)
		return m, nil
	case tea.KeyEnter:
		cmd, arg := ParsePalette(m.Palette.Input())
		m.Palette = m.Palette.WithOpen(false)
		return dispatchPalette(m, cmd, arg)
	case tea.KeyBackspace:
		in := m.Palette.Input()
		if len(in) > 0 {
			m.Palette = m.Palette.WithInput(in[:len(in)-1])
		}
		return m, nil
	case tea.KeyTab:
		m.Palette = m.Palette.WithInput(autocompletePalette(m.Palette.Input()))
		return m, nil
	case tea.KeySpace:
		m.Palette = m.Palette.WithInput(m.Palette.Input() + " ")
		return m, nil
	case tea.KeyRunes:
		m.Palette = m.Palette.WithInput(m.Palette.Input() + string(msg.Runes))
		return m, nil
	}
	return m, nil
}

// autocompletePalette returns the unique prefix-matching command
// name when exactly one candidate matches; otherwise returns input
// unchanged.
func autocompletePalette(input string) string {
	var match string
	n := 0
	for _, c := range []string{"cancel", "end", "interactive", "template ", "reattach", "status", "edit", "help"} {
		if strings.HasPrefix(c, input) {
			match = c
			n++
		}
	}
	if n == 1 {
		return match
	}
	return input
}

// dispatchPalette routes a parsed palette command. Branches that
// require interactive-mode plumbing (Cancel/End/Interactive/Reattach)
// are placeholders here; later phase tasks fill them.
func dispatchPalette(m Model, cmd PaletteCmd, arg string) (tea.Model, tea.Cmd) {
	switch cmd {
	case PaletteEmpty:
		return m, nil
	case PaletteUnknown:
		m.ErrBanner = fmt.Sprintf("unknown command: %s", arg)
		return m, nil
	case PaletteHelp:
		return openHelpModal(m), nil
	case PaletteStatus, PaletteEdit:
		// No-op today: the prior slash dispatcher had no body for
		// these either; placeholders kept for parity until a future
		// task fills them.
		return m, nil
	case PaletteTemplate:
		if arg == "" {
			m.ErrBanner = "template requires a template name"
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
		return m, patchLastTemplateCmd(m.Client, m.Namespace, m.Focused, arg)
	case PaletteCancel:
		focused := m.Sessions[m.Focused]
		if focused == nil {
			m.ErrBanner = errNoSessionFocused
			return m, nil
		}
		if focused.Session.ActiveRunRef == "" {
			m.ErrBanner = "nothing to cancel"
			return m, nil
		}
		return m, cancelRunCmd(m.Client, m.Namespace, focused.Session.ActiveRunRef)
	case PaletteInteractive:
		focused := m.Sessions[m.Focused]
		if focused == nil {
			m.ErrBanner = errNoSessionFocused
			return m, nil
		}
		if focused.Interactive != nil {
			m.ErrBanner = "session already bound to an interactive run"
			return m, nil
		}
		focused.Armed = true
		return m, nil
	case PaletteEnd, PaletteReattach:
		// Filled by later phase tasks (27, 31).
		return m, nil
	}
	return m, nil
}

// upsertSession inserts a new session or refreshes the projection of
// an existing one. SessionOrder is sorted by LastActivity desc (with
// CreationTime as the tiebreaker), matching session.List's sort key.
func upsertSession(m Model, s pdksession.Session) Model {
	if _, exists := m.Sessions[s.Name]; !exists {
		m.SessionOrder = append(m.SessionOrder, s.Name)
		m.Sessions[s.Name] = &SessionState{
			Session: s,
			Events:  map[string][]paddockv1alpha1.PaddockEvent{},
		}
	} else {
		m.Sessions[s.Name].Session = s
	}
	sortSessionOrder(m)
	return m
}

// sortSessionOrder reorders m.SessionOrder by LastActivity desc with
// CreationTime as a tiebreaker. Names with both keys zero (unlikely
// in practice) fall through to alphabetical to keep output stable.
func sortSessionOrder(m Model) {
	sort.SliceStable(m.SessionOrder, func(i, j int) bool {
		a := m.Sessions[m.SessionOrder[i]]
		b := m.Sessions[m.SessionOrder[j]]
		if a == nil || b == nil {
			return m.SessionOrder[i] < m.SessionOrder[j]
		}
		ka := sessionSortKey(a.Session)
		kb := sessionSortKey(b.Session)
		if ka.Equal(kb) {
			return m.SessionOrder[i] < m.SessionOrder[j]
		}
		return ka.After(kb)
	})
}

// sessionSortKey returns LastActivity if set, falling back to
// CreationTime — same as session.List's activitySortKey.
func sessionSortKey(s pdksession.Session) time.Time {
	if !s.LastActivity.IsZero() {
		return s.LastActivity
	}
	return s.CreationTime
}

// previousActiveRunRef returns the ActiveRunRef stored in m.Sessions
// for name BEFORE an upsert overwrites it. Empty string if the
// session is not yet known.
func previousActiveRunRef(m Model, name string) string {
	if prev, ok := m.Sessions[name]; ok && prev != nil {
		return prev.Session.ActiveRunRef
	}
	return ""
}

// drainQueueIfFreed inspects an ActiveRunRef transition for the
// session named. When it goes from non-empty to empty, the session
// has just become idle — Pop the next queued prompt (if any) and
// return a command to submit it. Otherwise return (m, nil).
func drainQueueIfFreed(m Model, name, prevActive, newActive string) (Model, tea.Cmd) {
	if prevActive == "" || newActive != "" {
		return m, nil
	}
	state := m.Sessions[name]
	if state == nil || state.Queue.Len() == 0 {
		return m, nil
	}
	prompt, ok := state.Queue.Pop()
	if !ok {
		return m, nil
	}
	template := state.Session.LastTemplate
	return m, submitRunCmd(m.Client, m.Namespace, name, template, prompt, "")
}

// ensureEventTail opens a per-run event tail for hr if it's
// non-terminal and we don't already have one open. Used on
// runUpdatedMsg so reattach sees events for in-flight runs the TUI
// did not create itself.
func ensureEventTail(m *Model, hr paddockv1alpha1.HarnessRun) tea.Cmd {
	if hr.Name == "" {
		return nil
	}
	if isTerminalPhase(hr.Status.Phase) {
		return nil
	}
	if m.eventTails == nil {
		m.eventTails = map[string]<-chan paddockv1alpha1.PaddockEvent{}
	}
	if _, ok := m.eventTails[hr.Name]; ok {
		return nil
	}
	return openEventTailCmd(m.ctx, m.Client, m.Namespace, hr.Name)
}

// isTerminalPhase mirrors RunSummary.IsTerminal but operates on the
// raw CR phase so callers don't need to round-trip through a
// projection.
func isTerminalPhase(p paddockv1alpha1.HarnessRunPhase) bool {
	switch p {
	case paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed,
		paddockv1alpha1.HarnessRunPhaseCancelled:
		return true
	}
	return false
}

// upsertRun inserts or updates the RunSummary for the run carried in
// msg under the workspace it belongs to. Also seeds the SessionState
// Events map from Status.RecentEvents (deduped) so that:
//
//   - On reattach, terminal runs render their full body without
//     requiring a fresh tail (which ensureEventTail correctly skips).
//   - In-flight runs whose tail hasn't started yet still show events
//     captured in the ring buffer.
//
// Events arriving via the live tail (eventReceivedMsg) go through the
// same dedupe so a tail and an upsert seeing the same event from the
// ring buffer don't duplicate.
func upsertRun(m Model, msg runUpdatedMsg) Model {
	state := m.Sessions[msg.WorkspaceRef]
	if state == nil {
		return m
	}
	summary := runSummaryFromCR(msg.Run)
	found := false
	for i := range state.Runs {
		if state.Runs[i].Name == summary.Name {
			state.Runs[i] = summary
			found = true
			break
		}
	}
	if !found {
		state.Runs = append(state.Runs, summary)
	}
	sortRuns(state.Runs)
	if state.Events == nil {
		state.Events = map[string][]paddockv1alpha1.PaddockEvent{}
	}
	for _, ev := range msg.Run.Status.RecentEvents {
		state.Events[msg.Run.Name] = appendEventDedup(state.Events[msg.Run.Name], ev)
	}
	return m
}

// sortRuns orders runs chronologically by CreationTime, with the run
// Name as the tiebreaker so the order is deterministic when two runs
// share a CreationTimestamp (which can happen at one-second
// granularity). Watches list runs in arbitrary order — without an
// explicit sort the slice would reflect arrival order, which causes
// the main pane (rendering newest at the top by walking the slice
// backwards) to show runs out of chronological order on reattach.
func sortRuns(runs []RunSummary) {
	sort.SliceStable(runs, func(i, j int) bool {
		if !runs[i].CreationTime.Equal(runs[j].CreationTime) {
			return runs[i].CreationTime.Before(runs[j].CreationTime)
		}
		return runs[i].Name < runs[j].Name
	})
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
	if msg.WorkspaceRef == m.Focused && m.RunCursor >= len(state.Runs) && len(state.Runs) > 0 {
		m.RunCursor = len(state.Runs) - 1
	}
	return m
}

// appendEvent appends an event to the focused session's Events map.
// Only the focused session is tailed, so unfocused sessions are a
// no-op. Goes through appendEventDedup so an event already captured
// via Status.RecentEvents on a prior runUpdatedMsg isn't duplicated.
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
	state.Events[msg.RunName] = appendEventDedup(state.Events[msg.RunName], msg.Event)
	return m
}

// appendEventDedup appends ev to existing only if no event with the
// same identity is already present. Identity is (timestamp, type,
// summary, fields) — the same key shape events.Dedupe uses, but
// inlined to avoid plumbing per-run Dedupe state through SessionState.
func appendEventDedup(existing []paddockv1alpha1.PaddockEvent, ev paddockv1alpha1.PaddockEvent) []paddockv1alpha1.PaddockEvent {
	for i := range existing {
		if eventsEqual(existing[i], ev) {
			return existing
		}
	}
	return append(existing, ev)
}

// eventsEqual reports whether two PaddockEvents represent the same
// occurrence: identical timestamp, type, summary, and fields. Used
// only by appendEventDedup; does not need to be cryptographically
// strong because the inputs come from a single trusted source (the
// HarnessRun.status.recentEvents ring).
func eventsEqual(a, b paddockv1alpha1.PaddockEvent) bool {
	if !a.Timestamp.Equal(&b.Timestamp) {
		return false
	}
	if a.Type != b.Type || a.Summary != b.Summary {
		return false
	}
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for k, av := range a.Fields {
		if bv, ok := b.Fields[k]; !ok || bv != av {
			return false
		}
	}
	return true
}

// templateNames extracts the Name field of each TemplateInfo. The
// new-session modal uses []string for the picks slice so the
// underlying TemplateInfo metadata stays an app-package detail.
func templateNames(ts []pdksession.TemplateInfo) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
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

// frameToEvent converts a StreamFrame into a PaddockEvent. The frame's
// Type field becomes the event Type; the frame's Data is unmarshalled
// into the remaining PaddockEvent fields (summary, ts, fields,
// schemaVersion). Unknown keys in Data are silently ignored. If Data is
// malformed the returned event carries only the Type and a zero timestamp
// so the TUI can still display something rather than dropping the frame.
func frameToEvent(frame paddockbroker.StreamFrame) paddockv1alpha1.PaddockEvent {
	// frameData is the portion of a PaddockEvent that travels in
	// StreamFrame.Data. We use a local struct with identical JSON tags so
	// json.Unmarshal maps the wire fields without importing PaddockEvent's
	// full validation machinery.
	var fd struct {
		SchemaVersion string            `json:"schemaVersion"`
		Timestamp     metav1.Time       `json:"ts"`
		Summary       string            `json:"summary,omitempty"`
		Fields        map[string]string `json:"fields,omitempty"`
	}
	if len(frame.Data) > 0 {
		_ = json.Unmarshal(frame.Data, &fd)
	}
	return paddockv1alpha1.PaddockEvent{
		SchemaVersion: fd.SchemaVersion,
		Timestamp:     fd.Timestamp,
		Type:          frame.Type,
		Summary:       fd.Summary,
		Fields:        fd.Fields,
	}
}

// runSummaryFromCR projects a HarnessRun into the TUI-shaped
// RunSummary used by the View layer.
func runSummaryFromCR(hr paddockv1alpha1.HarnessRun) RunSummary {
	r := RunSummary{
		Name:         hr.Name,
		CreationTime: hr.CreationTimestamp.Time,
		Phase:        hr.Status.Phase,
		Prompt:       hr.Spec.Prompt,
		Template:     hr.Spec.TemplateRef.Name,
	}
	if hr.Status.StartTime != nil {
		r.StartTime = hr.Status.StartTime.Time
	}
	if hr.Status.CompletionTime != nil {
		r.CompletionTime = hr.Status.CompletionTime.Time
	}
	return r
}
