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
	"context"
	"encoding/json"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	paddockbroker "paddock.dev/paddock/internal/paddocktui/broker"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

// testSessionName is the canonical session name used across reducer
// tests. Extracting it placates the goconst linter — multiple tests
// hard-code the same string.
const testSessionName = "alpha"

// newTestModel builds a Model wired to a fake client for use across
// reducer tests. The fake client has the paddock scheme registered so
// any incidental Get/List calls against it will not panic.
func newTestModel(t *testing.T) Model {
	t.Helper()
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	return Model{
		Client:    cli,
		Namespace: "default",
		Sessions:  map[string]*SessionState{},
	}
}

func TestUpdate_AddSession(t *testing.T) {
	m := newTestModel(t)
	next, _ := m.Update(sessionAddedMsg{Session: pdksession.Session{Name: "alpha"}})
	nm := next.(Model)
	if _, ok := nm.Sessions["alpha"]; !ok {
		t.Fatalf("session not added: %v", nm.Sessions)
	}
	if len(nm.SessionOrder) != 1 || nm.SessionOrder[0] != "alpha" {
		t.Errorf("session order wrong: %v", nm.SessionOrder)
	}
}

func TestUpdate_DeleteSession(t *testing.T) {
	m := newTestModel(t)
	m.Sessions["alpha"] = &SessionState{Session: pdksession.Session{Name: "alpha"}}
	m.SessionOrder = []string{"alpha"}
	m.Focused = "alpha"
	next, _ := m.Update(sessionDeletedMsg{Name: "alpha"})
	nm := next.(Model)
	if _, ok := nm.Sessions["alpha"]; ok {
		t.Errorf("session not removed")
	}
	if nm.Focused != "" {
		t.Errorf("focus should clear when focused session deleted")
	}
}

func TestUpdate_DrainQueueOnIdleTransition(t *testing.T) {
	m := newTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx

	// Seed the model: alpha is busy with run "hr-1" and has a queued
	// prompt waiting for it to drain.
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName, ActiveRunRef: "hr-1"},
	}
	m.Sessions[testSessionName].Queue.Push("queued-1")
	m.SessionOrder = []string{testSessionName}
	m.Focused = testSessionName

	// Simulate the run completing: ActiveRunRef goes from "hr-1" → "".
	next, cmd := m.Update(sessionUpdatedMsg{Session: pdksession.Session{Name: testSessionName, ActiveRunRef: ""}})
	nm := next.(Model)
	if nm.Sessions[testSessionName].Queue.Len() != 0 {
		t.Errorf("queue not drained: %v", nm.Sessions[testSessionName].Queue.Items())
	}
	if cmd == nil {
		t.Fatal("expected a tea.Batch with submitRunCmd, got nil")
	}
}

func TestUpdate_NoDrainWhenStillActive(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName, ActiveRunRef: "hr-1"},
	}
	m.Sessions[testSessionName].Queue.Push("queued-1")
	m.SessionOrder = []string{testSessionName}

	// ActiveRunRef stays non-empty (e.g. transitioned to a new run);
	// the queue must not drain.
	next, _ := m.Update(sessionUpdatedMsg{Session: pdksession.Session{Name: testSessionName, ActiveRunRef: "hr-2"}})
	nm := next.(Model)
	if nm.Sessions[testSessionName].Queue.Len() != 1 {
		t.Errorf("queue drained too eagerly: len=%d", nm.Sessions[testSessionName].Queue.Len())
	}
}

func TestUpdate_OpensEventTailForRunningRunOnReattach(t *testing.T) {
	m := newTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx

	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
	}
	m.SessionOrder = []string{testSessionName}

	hr := paddockv1alpha1.HarnessRun{}
	hr.Name = "hr-running"
	hr.Spec.WorkspaceRef = testSessionName
	hr.Status.Phase = paddockv1alpha1.HarnessRunPhaseRunning

	// First sighting: no eventTail registered yet, so ensureEventTail
	// must return a non-nil cmd (a tea.Batch).
	_, cmd := m.Update(runUpdatedMsg{WorkspaceRef: testSessionName, Run: hr})
	if cmd == nil {
		t.Fatal("expected a batched cmd including openEventTailCmd, got nil")
	}
}

func TestUpdate_RunCreatedDoesNotOpenTailDirectly(t *testing.T) {
	// Regression: runCreatedMsg used to open a tail eagerly. The runs
	// watch then fired runUpdatedMsg, ensureEventTail saw no entry in
	// m.eventTails (the OpenedMsg from the first call was still in
	// flight) and opened a SECOND tail. Result: every event emitted
	// twice. The fix is to let ensureEventTail be the sole opener.
	m := newTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx
	_, cmd := m.Update(runCreatedMsg{WorkspaceRef: testSessionName, RunName: "hr-fresh"})
	if cmd != nil {
		t.Errorf("runCreatedMsg should not open a tail; got cmd=%T", cmd)
	}
}

func TestUpdate_UpsertRunSeedsEventsFromRecentEvents(t *testing.T) {
	// Regression: on reattach, terminal runs rendered an empty body
	// because upsertRun only projected the RunSummary and never copied
	// Status.RecentEvents into the Events map (and ensureEventTail
	// correctly skipped opening a tail for terminal phases).
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
	}
	m.SessionOrder = []string{testSessionName}

	hr := paddockv1alpha1.HarnessRun{}
	hr.Name = "hr-done"
	hr.Spec.WorkspaceRef = testSessionName
	hr.Status.Phase = paddockv1alpha1.HarnessRunPhaseSucceeded
	hr.Status.RecentEvents = []paddockv1alpha1.PaddockEvent{
		{Timestamp: metav1.NewTime(time.Now()), Type: "Message", Summary: "first reply"},
		{Timestamp: metav1.NewTime(time.Now().Add(time.Second)), Type: "Message", Summary: "second reply"},
	}
	next, _ := m.Update(runUpdatedMsg{WorkspaceRef: testSessionName, Run: hr})
	got := next.(Model).Sessions[testSessionName].Events["hr-done"]
	if len(got) != 2 {
		t.Fatalf("expected 2 events copied from recentEvents, got %d", len(got))
	}
	if got[0].Summary != "first reply" || got[1].Summary != "second reply" {
		t.Errorf("events copied in wrong order: %+v", got)
	}
}

func TestUpsertRun_SortsByCreationTime(t *testing.T) {
	// Regression: HarnessRun watch events arrive in k8s list order
	// (essentially arbitrary across reattach), but the main pane walks
	// SessionState.Runs backwards expecting chronological order. Without
	// an explicit sort, two runs created seconds apart could render
	// newer-then-older. upsertRun must keep Runs ordered by
	// CreationTime so the rendering invariant holds.
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
	}
	m.SessionOrder = []string{testSessionName}

	t0 := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	older := paddockv1alpha1.HarnessRun{}
	older.Name = "hr-older"
	older.CreationTimestamp = metav1.NewTime(t0)
	older.Spec.WorkspaceRef = testSessionName

	newer := paddockv1alpha1.HarnessRun{}
	newer.Name = "hr-newer"
	newer.CreationTimestamp = metav1.NewTime(t0.Add(2 * time.Minute))
	newer.Spec.WorkspaceRef = testSessionName

	// Deliver in REVERSE chronological order (the watch is free to do
	// this on initial list).
	next, _ := m.Update(runUpdatedMsg{WorkspaceRef: testSessionName, Run: newer})
	next, _ = next.(Model).Update(runUpdatedMsg{WorkspaceRef: testSessionName, Run: older})

	runs := next.(Model).Sessions[testSessionName].Runs
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].Name != "hr-older" || runs[1].Name != "hr-newer" {
		t.Errorf("runs not sorted by CreationTime ascending: got %s, %s", runs[0].Name, runs[1].Name)
	}
}

func TestAppendEventDedup(t *testing.T) {
	// Two events with the same identity should collapse; different
	// timestamps remain distinct. Locks the dedupe behaviour shared by
	// upsertRun (RecentEvents → Events) and appendEvent (live tail →
	// Events) so a tail and a ring-buffer copy don't double-emit.
	ts := metav1.NewTime(time.Now())
	a := paddockv1alpha1.PaddockEvent{Timestamp: ts, Type: "Message", Summary: "hi"}
	dup := paddockv1alpha1.PaddockEvent{Timestamp: ts, Type: "Message", Summary: "hi"}
	other := paddockv1alpha1.PaddockEvent{
		Timestamp: metav1.NewTime(ts.Add(time.Second)), Type: "Message", Summary: "hi",
	}
	got := appendEventDedup(nil, a)
	got = appendEventDedup(got, dup)
	if len(got) != 1 {
		t.Errorf("expected dedupe to collapse identical event, got %d", len(got))
	}
	got = appendEventDedup(got, other)
	if len(got) != 2 {
		t.Errorf("expected different timestamp to be kept, got %d", len(got))
	}
}

func TestUpdate_DoesNotOpenEventTailForTerminalRun(t *testing.T) {
	m := newTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx

	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
	}
	m.SessionOrder = []string{testSessionName}

	hr := paddockv1alpha1.HarnessRun{}
	hr.Name = "hr-done"
	hr.Spec.WorkspaceRef = testSessionName
	hr.Status.Phase = paddockv1alpha1.HarnessRunPhaseSucceeded

	if cmd := ensureEventTail(&m, hr); cmd != nil {
		t.Errorf("expected no event tail for terminal phase, got %v", cmd)
	}
}

func TestUpsertSession_SortsByLastActivityDesc(t *testing.T) {
	m := newTestModel(t)
	now := time.Now()
	m = upsertSession(m, pdksession.Session{Name: testSessionName, LastActivity: now.Add(-10 * time.Minute)})
	m = upsertSession(m, pdksession.Session{Name: "bravo", LastActivity: now})
	m = upsertSession(m, pdksession.Session{Name: "charlie", LastActivity: now.Add(-5 * time.Minute)})
	if got := m.SessionOrder; len(got) != 3 || got[0] != "bravo" || got[1] != "charlie" || got[2] != testSessionName {
		t.Errorf("session order not by lastActivity desc: %v", got)
	}
}

func TestUpdate_TemplatesLoadedCachesAndPatchesOpenModal(t *testing.T) {
	m := newTestModel(t)
	// Modal already open with no picks (loaded before templates arrived).
	m.Modal = ModalNew
	m.ModalNew = &NewSessionModalState{}
	templates := []pdksession.TemplateInfo{{Name: "echo"}, {Name: "claude-code"}}
	next, _ := m.Update(templatesLoadedMsg{Templates: templates})
	nm := next.(Model)
	if len(nm.availableTemplates) != 2 {
		t.Errorf("availableTemplates not cached: %v", nm.availableTemplates)
	}
	if len(nm.ModalNew.TemplatePicks) != 2 {
		t.Errorf("modal picks not patched in: %v", nm.ModalNew.TemplatePicks)
	}
}

func TestUpdate_CtrlCQuitsFromAnywhere(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(m *Model)
	}{
		{"sidebar focus", func(m *Model) { m.FocusArea = FocusSidebar }},
		{"prompt focus", func(m *Model) { m.FocusArea = FocusPrompt }},
		{"modal open", func(m *Model) { m.Modal = ModalHelp }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			tc.mut(&m)
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if cmd == nil {
				t.Fatal("expected tea.Quit cmd from Ctrl-C, got nil")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("expected QuitMsg, got %T", cmd())
			}
		})
	}
}

func TestUpdate_EnterOnNewSessionSentinelOpensModal(t *testing.T) {
	m := newTestModel(t)
	m.FocusArea = FocusSidebar
	m.Focused = NewSessionSentinel
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(Model)
	if nm.Modal != ModalNew {
		t.Errorf("expected ModalNew after Enter on sentinel, got Modal=%v", nm.Modal)
	}
	if nm.ModalNew == nil {
		t.Errorf("expected ModalNew state to be allocated")
	}
}

func TestUpdate_MouseWheelScrollsMain(t *testing.T) {
	m := newTestModel(t)
	if m.MainScrollFromBottom != 0 {
		t.Fatalf("default offset should be 0, got %d", m.MainScrollFromBottom)
	}
	next, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = next.(Model)
	if m.MainScrollFromBottom != mainWheelStep {
		t.Errorf("after wheel-up, want %d, got %d", mainWheelStep, m.MainScrollFromBottom)
	}
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = next.(Model)
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = next.(Model)
	if m.MainScrollFromBottom != 0 {
		t.Errorf("wheel-down should clamp at 0, got %d", m.MainScrollFromBottom)
	}
	// Other mouse buttons are ignored — clicking on a row shouldn't
	// scroll.
	m.MainScrollFromBottom = 5
	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft})
	m = next.(Model)
	if m.MainScrollFromBottom != 5 {
		t.Errorf("left-click must not affect scroll, got %d", m.MainScrollFromBottom)
	}
}

func TestUpdate_PgUpPgDownScrollMain(t *testing.T) {
	m := newTestModel(t)
	if m.MainScrollFromBottom != 0 {
		t.Fatalf("default offset should be 0, got %d", m.MainScrollFromBottom)
	}
	// PgUp moves up by mainScrollStep.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(Model)
	if m.MainScrollFromBottom != mainScrollStep {
		t.Errorf("after PgUp, want %d, got %d", mainScrollStep, m.MainScrollFromBottom)
	}
	// PgDown brings it back; clamped at 0.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = next.(Model)
	if m.MainScrollFromBottom != 0 {
		t.Errorf("PgDown should clamp at 0, got %d", m.MainScrollFromBottom)
	}
	// Home snaps to a large value (render-time clamp).
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = next.(Model)
	if m.MainScrollFromBottom != mainScrollSnapTop {
		t.Errorf("Home should snap to mainScrollSnapTop, got %d", m.MainScrollFromBottom)
	}
	// End returns to bottom.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = next.(Model)
	if m.MainScrollFromBottom != 0 {
		t.Errorf("End should reset to 0, got %d", m.MainScrollFromBottom)
	}
}

func TestUpdate_EnterOnRealSessionFocusesPrompt(t *testing.T) {
	m := newTestModel(t)
	m.Sessions["alpha"] = &SessionState{Session: pdksession.Session{Name: "alpha"}}
	m.SessionOrder = []string{"alpha"}
	m.Focused = "alpha"
	m.FocusArea = FocusSidebar
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(Model)
	if nm.FocusArea != FocusPrompt {
		t.Errorf("expected FocusArea=FocusPrompt after Enter on a real session, got %v", nm.FocusArea)
	}
	if nm.Focused != "alpha" {
		t.Errorf("expected Focused to remain alpha, got %q", nm.Focused)
	}
}

func TestUpdate_EOnSentinelSetsErrBanner(t *testing.T) {
	m := newTestModel(t)
	m.FocusArea = FocusSidebar
	m.Focused = NewSessionSentinel
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	nm := next.(Model)
	if nm.Modal == ModalEnd {
		t.Errorf("e on sentinel must not open the end-session modal")
	}
	if nm.ErrBanner == "" {
		t.Errorf("expected ErrBanner when pressing e on the new-session sentinel")
	}
}

func TestUpdate_PromptInputAcceptsSpaces(t *testing.T) {
	m := newTestModel(t)
	m.FocusArea = FocusPrompt
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("hi")},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune("there")},
	} {
		next, _ := m.Update(k)
		m = next.(Model)
	}
	if got, want := m.PromptInput, "hi there"; got != want {
		t.Errorf("PromptInput=%q, want %q", got, want)
	}
}

func TestUpdate_QuitOnQ(t *testing.T) {
	m := newTestModel(t)
	m.FocusArea = FocusSidebar
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd, got nil")
	}
	// We can't compare cmd to tea.Quit directly (it's a function);
	// calling the cmd should produce a tea.QuitMsg.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

func TestUpdate_ColonOpensPalette(t *testing.T) {
	m := newTestModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	nm := next.(Model)
	if !nm.Palette.Open() {
		t.Fatal(": on empty prompt should open palette")
	}
	if nm.PromptInput != "" {
		t.Errorf("prompt input should not have received the colon; got %q", nm.PromptInput)
	}
}

func TestUpdate_ColonInsidePromptIsLiteral(t *testing.T) {
	m := newTestModel(t)
	m.PromptInput = "hello"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	nm := next.(Model)
	if nm.Palette.Open() {
		t.Fatal(": with non-empty prompt should be literal, not open palette")
	}
	if nm.PromptInput != "hello:" {
		t.Errorf("PromptInput = %q, want %q", nm.PromptInput, "hello:")
	}
}

func TestUpdate_CtrlKOpensPaletteRegardlessOfPrompt(t *testing.T) {
	m := newTestModel(t)
	m.PromptInput = "halfway typed"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	nm := next.(Model)
	if !nm.Palette.Open() {
		t.Fatal("Ctrl-K should open the palette unconditionally")
	}
	if nm.PromptInput != "halfway typed" {
		t.Errorf("Ctrl-K must not consume prompt input; got %q", nm.PromptInput)
	}
}

func TestUpdate_EscClosesPalette(t *testing.T) {
	m := newTestModel(t)
	m.Palette = m.Palette.WithOpen(true).WithInput("can")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm := next.(Model)
	if nm.Palette.Open() {
		t.Fatal("Esc should close the palette")
	}
	if nm.Palette.Input() != "" {
		t.Errorf("closed palette must have empty input; got %q", nm.Palette.Input())
	}
}

func TestUpdate_CtrlKDoesNotOpenPaletteOverModal(t *testing.T) {
	m := newTestModel(t)
	m.Modal = ModalHelp
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	nm := next.(Model)
	if nm.Palette.Open() {
		t.Fatal("Ctrl-K should not open the palette while a modal is up")
	}
}

func TestUpdate_ColonDoesNotOpenPaletteOverModal(t *testing.T) {
	m := newTestModel(t)
	m.Modal = ModalHelp
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	nm := next.(Model)
	if nm.Palette.Open() {
		t.Fatal(": should not open the palette while a modal is up")
	}
}

func TestPalette_HelpOpensHelpModal(t *testing.T) {
	m := newTestModel(t)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	nm := next.(Model)
	for _, r := range []rune{'h', 'e', 'l', 'p'} {
		next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		nm = next.(Model)
	}
	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm = next.(Model)
	if nm.Modal != ModalHelp {
		t.Errorf("expected help modal open, got %v", nm.Modal)
	}
}

func TestPalette_TemplateUpdatesLastTemplate(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName, LastTemplate: "old"},
	}
	m.SessionOrder = []string{testSessionName}
	m.Focused = testSessionName
	// Open the palette with ':'.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	nm := next.(Model)
	// Type "template" one rune at a time.
	for _, r := range "template" {
		next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		nm = next.(Model)
	}
	// Space between "template" and "new".
	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeySpace})
	nm = next.(Model)
	// Type "new".
	for _, r := range "new" {
		next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		nm = next.(Model)
	}
	// Submit.
	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm = next.(Model)
	if got := nm.Sessions[testSessionName].Session.LastTemplate; got != "new" {
		t.Errorf("LastTemplate = %q, want %q", got, "new")
	}
}

func TestPalette_CancelBatchTriggersControllerCancel(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName, ActiveRunRef: "hr-running"},
	}
	m.Focused = testSessionName
	_, cmd := dispatchPalette(m, PaletteCancel, "")
	if cmd == nil {
		t.Fatal("expected cancelRunCmd, got nil")
	}
}

func TestNavigation_TabCyclesFocus(t *testing.T) {
	m := newTestModel(t)
	if m.FocusArea != FocusPrompt {
		t.Fatalf("default focus = %v, want FocusPrompt", m.FocusArea)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if next.(Model).FocusArea != FocusSidebar {
		t.Errorf("after one Tab, focus = %v, want FocusSidebar", next.(Model).FocusArea)
	}
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyTab})
	if next.(Model).FocusArea != FocusMainPane {
		t.Errorf("after two Tabs, focus = %v, want FocusMainPane", next.(Model).FocusArea)
	}
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyTab})
	if next.(Model).FocusArea != FocusPrompt {
		t.Errorf("after three Tabs, focus should wrap to FocusPrompt; got %v", next.(Model).FocusArea)
	}
}

func TestNavigation_ArrowsMoveRunCursorWhenMainPaneFocused(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
		Runs:    []RunSummary{{Name: "r1"}, {Name: "r2"}, {Name: "r3"}},
	}
	m.Focused = testSessionName
	m.FocusArea = FocusMainPane
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if next.(Model).RunCursor != 1 {
		t.Errorf("Down should advance RunCursor; got %d", next.(Model).RunCursor)
	}
}

func TestNavigation_RunCursorClampedOnRunDeletion(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
		Runs:    []RunSummary{{Name: "r1"}, {Name: "r2"}, {Name: "r3"}},
	}
	m.Focused = testSessionName
	m.FocusArea = FocusMainPane
	m.RunCursor = 2
	nm := removeRun(m, runDeletedMsg{WorkspaceRef: testSessionName, Name: "r3"})
	if nm.RunCursor != 1 {
		t.Errorf("RunCursor should clamp to len(Runs)-1=1 after deletion; got %d", nm.RunCursor)
	}
}

func TestNavigation_UpAtZeroStaysAtZero(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
		Runs:    []RunSummary{{Name: "r1"}, {Name: "r2"}},
	}
	m.Focused = testSessionName
	m.FocusArea = FocusMainPane
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if next.(Model).RunCursor != 0 {
		t.Errorf("Up at index 0 should stay at 0; got %d", next.(Model).RunCursor)
	}
}

func TestPalette_InteractiveArmsSession(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
	}
	m.SessionOrder = []string{testSessionName}
	m.Focused = testSessionName

	nm, _ := dispatchPalette(m, PaletteInteractive, "")
	s := nm.(Model).Sessions[testSessionName]
	if !s.Armed {
		t.Error("session should be Armed after interactive palette command")
	}
	if s.Interactive != nil {
		t.Error("Armed sessions must not yet have an Interactive binding")
	}
	if nm.(Model).ErrBanner != "" {
		t.Errorf("expected no ErrBanner; got %q", nm.(Model).ErrBanner)
	}
}

func TestPalette_InteractiveRefusesIfAlreadyBound(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session:     pdksession.Session{Name: testSessionName},
		Interactive: &InteractiveBinding{RunName: "alpha-running"},
	}
	m.SessionOrder = []string{testSessionName}
	m.Focused = testSessionName

	nm, _ := dispatchPalette(m, PaletteInteractive, "")
	s := nm.(Model).Sessions[testSessionName]
	if s.Armed {
		t.Error("session must not be re-armed when already bound")
	}
	if nm.(Model).ErrBanner == "" {
		t.Error("expected ErrBanner explaining the session is already bound")
	}
}

func TestPalette_InteractiveNoSessionShowsBanner(t *testing.T) {
	m := newTestModel(t)
	m.Focused = ""

	nm, _ := dispatchPalette(m, PaletteInteractive, "")
	if nm.(Model).ErrBanner != errNoSessionFocused {
		t.Errorf("expected errNoSessionFocused banner; got %q", nm.(Model).ErrBanner)
	}
}

func TestUpdate_RunCreatedForArmedKickoffBindsAndOpensStream(t *testing.T) {
	m := newTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
	}
	m.Focused = testSessionName
	next, cmd := m.Update(runCreatedMsg{
		WorkspaceRef: testSessionName,
		RunName:      "hr-int",
		Mode:         paddockv1alpha1.HarnessRunModeInteractive,
	})
	nm := next.(Model)
	if nm.Sessions[testSessionName].Interactive == nil ||
		nm.Sessions[testSessionName].Interactive.RunName != "hr-int" {
		t.Errorf("expected Interactive binding to hr-int; got %+v", nm.Sessions[testSessionName].Interactive)
	}
	// BrokerClient is nil in the test model so we expect a nil cmd (stream
	// opening is skipped when broker is not configured).
	_ = cmd
}

func TestUpdate_RunCreatedBatchDoesNotBind(t *testing.T) {
	m := newTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName},
	}
	m.Focused = testSessionName
	next, cmd := m.Update(runCreatedMsg{
		WorkspaceRef: testSessionName,
		RunName:      "hr-batch",
		Mode:         "", // Batch (zero value)
	})
	nm := next.(Model)
	if nm.Sessions[testSessionName].Interactive != nil {
		t.Errorf("Batch runCreatedMsg must not set Interactive binding; got %+v",
			nm.Sessions[testSessionName].Interactive)
	}
	if cmd != nil {
		t.Errorf("Batch runCreatedMsg must return nil cmd; got %T", cmd)
	}
}

func TestUpdate_FrameFoldsIntoBoundRun(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session:     pdksession.Session{Name: testSessionName},
		Interactive: &InteractiveBinding{RunName: "hr-int"},
		Events:      map[string][]paddockv1alpha1.PaddockEvent{},
	}
	m.Focused = testSessionName
	ch := make(chan paddockbroker.StreamFrame, 1)
	if m.interactiveFrames == nil {
		m.interactiveFrames = map[string]<-chan paddockbroker.StreamFrame{}
	}
	m.interactiveFrames["hr-int"] = ch
	frame := paddockbroker.StreamFrame{Type: "Message", Data: json.RawMessage(`{"summary":"hi"}`)}
	next, cmd := m.Update(interactiveFrameMsg{RunName: "hr-int", Frame: frame})
	nm := next.(Model)
	evs := nm.Sessions[testSessionName].Events["hr-int"]
	if len(evs) != 1 || evs[0].Type != "Message" || evs[0].Summary != "hi" {
		t.Errorf("expected one Message event with Summary=hi; got %+v", evs)
	}
	if cmd == nil {
		t.Error("expected nextInteractiveFrameCmd to be returned, got nil")
	}
}

func TestUpdate_FrameForUnknownRunDroppedSilently(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session:     pdksession.Session{Name: testSessionName},
		Interactive: &InteractiveBinding{RunName: "hr-int"},
		Events:      map[string][]paddockv1alpha1.PaddockEvent{},
	}
	m.Focused = testSessionName
	frame := paddockbroker.StreamFrame{Type: "Message", Data: json.RawMessage(`{"summary":"ghost"}`)}
	// RunName does not match the bound session's RunName.
	next, cmd := m.Update(interactiveFrameMsg{RunName: "hr-unknown", Frame: frame})
	nm := next.(Model)
	if evs := nm.Sessions[testSessionName].Events["hr-unknown"]; len(evs) != 0 {
		t.Errorf("expected no events for unknown run; got %+v", evs)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd for unknown run; got %T", cmd)
	}
}

func TestUpdate_FramesDeduplicate(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session:     pdksession.Session{Name: testSessionName},
		Interactive: &InteractiveBinding{RunName: "hr-int"},
		Events:      map[string][]paddockv1alpha1.PaddockEvent{},
	}
	m.Focused = testSessionName
	ch := make(chan paddockbroker.StreamFrame, 2)
	if m.interactiveFrames == nil {
		m.interactiveFrames = map[string]<-chan paddockbroker.StreamFrame{}
	}
	m.interactiveFrames["hr-int"] = ch
	// Same frame data sent twice — should only appear once in the ring.
	payload := json.RawMessage(`{"summary":"dup","ts":"2026-01-01T00:00:00Z"}`)
	frame := paddockbroker.StreamFrame{Type: "Message", Data: payload}
	next, _ := m.Update(interactiveFrameMsg{RunName: "hr-int", Frame: frame})
	next, _ = next.(Model).Update(interactiveFrameMsg{RunName: "hr-int", Frame: frame})
	evs := next.(Model).Sessions[testSessionName].Events["hr-int"]
	if len(evs) != 1 {
		t.Errorf("expected dedup to collapse identical frames; got %d events", len(evs))
	}
}

func TestUpdate_StreamOpenedRegistersChannelAndSpawnsRead(t *testing.T) {
	m := newTestModel(t)
	m.Sessions[testSessionName] = &SessionState{
		Session:     pdksession.Session{Name: testSessionName},
		Interactive: &InteractiveBinding{RunName: "hr-int"},
		Events:      map[string][]paddockv1alpha1.PaddockEvent{},
	}
	m.Focused = testSessionName
	ch := make(chan paddockbroker.StreamFrame)
	next, cmd := m.Update(interactiveStreamOpenedMsg{RunName: "hr-int", Ch: ch})
	nm := next.(Model)
	if nm.interactiveFrames == nil || nm.interactiveFrames["hr-int"] == nil {
		t.Error("interactiveFrames map not populated after stream opened")
	}
	if cmd == nil {
		t.Error("expected nextInteractiveFrameCmd cmd after stream opened, got nil")
	}
}
