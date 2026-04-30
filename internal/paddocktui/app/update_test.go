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
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
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
