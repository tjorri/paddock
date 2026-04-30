package app

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

func TestPromptSubmit_QueuesWhenRunInFlight(t *testing.T) {
	state := &SessionState{
		Session: pdksessionMockedActive(testSessionName, "hr-1"),
		Runs:    []RunSummary{{Name: "hr-1", Phase: paddockv1alpha1.HarnessRunPhaseRunning}},
	}
	m := Model{
		Sessions:     map[string]*SessionState{testSessionName: state},
		SessionOrder: []string{testSessionName},
		Focused:      testSessionName,
		PromptInput:  "second prompt",
	}
	next, cmd := handlePromptSubmit(m)
	if cmd != nil {
		t.Errorf("expected no cmd (queued), got non-nil cmd")
	}
	if state.Queue.Len() != 1 || state.Queue.Peek() != "second prompt" {
		t.Errorf("prompt not queued: %v", state.Queue.Items())
	}
	if next.PromptInput != "" {
		t.Errorf("input not cleared after submit: %q", next.PromptInput)
	}
}

func TestPromptSubmit_FiresWhenIdle(t *testing.T) {
	base := newTestModel(t)
	state := &SessionState{
		Session: pdksessionMockedIdle(testSessionName),
	}
	base.Sessions[testSessionName] = state
	base.SessionOrder = []string{testSessionName}
	base.Focused = testSessionName
	base.PromptInput = "first prompt"
	_, cmd := handlePromptSubmit(base)
	if cmd == nil {
		t.Errorf("expected submitRunCmd, got nil")
	}
}

func TestPromptSubmit_BuffersWhileTurnInFlight(t *testing.T) {
	seq := int32(2)
	state := &SessionState{
		Session: pdksessionMockedIdle(testSessionName),
		Interactive: &InteractiveBinding{
			RunName:        "hr-int",
			CurrentTurnSeq: &seq,
		},
	}
	m := Model{
		Sessions:     map[string]*SessionState{testSessionName: state},
		SessionOrder: []string{testSessionName},
		Focused:      testSessionName,
		PromptInput:  "next idea",
	}
	next, cmd := handlePromptSubmit(m)
	if cmd != nil {
		t.Errorf("expected nil cmd (buffered), got non-nil cmd")
	}
	if next.PendingPrompt != "next idea" {
		t.Errorf("PendingPrompt = %q, want %q", next.PendingPrompt, "next idea")
	}
	if next.PromptInput != "" {
		t.Errorf("PromptInput should clear after buffering; got %q", next.PromptInput)
	}
}

func TestPrompt_ArmedSubmitCreatesInteractiveRun(t *testing.T) {
	m := newTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx
	m.Sessions[testSessionName] = &SessionState{
		Session: pdksession.Session{Name: testSessionName, LastTemplate: "claude-interactive"},
		Armed:   true,
	}
	m.Focused = testSessionName
	m.FocusArea = FocusPrompt
	m.PromptInput = "kick"
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submitRunCmd cmd; got nil")
	}
	nm := next.(Model)
	if nm.Sessions[testSessionName].Armed {
		t.Error("Armed should clear once the kick-off prompt is submitted")
	}
}

func TestPrompt_BoundSubmitCallsBrokerSubmit(t *testing.T) {
	m := newTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx
	m.Sessions[testSessionName] = &SessionState{
		Session:     pdksession.Session{Name: testSessionName},
		Interactive: &InteractiveBinding{RunName: "hr-int"},
	}
	m.Focused = testSessionName
	m.FocusArea = FocusPrompt
	m.PromptInput = "next prompt"
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submitInteractivePromptCmd; got nil")
	}
}

func pdksessionMockedActive(name, runRef string) pdksession.Session {
	return pdksession.Session{Name: name, ActiveRunRef: runRef, Phase: paddockv1alpha1.WorkspacePhaseActive, LastActivity: time.Now()}
}

func pdksessionMockedIdle(name string) pdksession.Session {
	return pdksession.Session{Name: name, Phase: paddockv1alpha1.WorkspacePhaseActive, LastActivity: time.Now()}
}
