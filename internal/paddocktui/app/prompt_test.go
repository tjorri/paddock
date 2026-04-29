package app

import (
	"testing"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

func TestPromptSubmit_QueuesWhenRunInFlight(t *testing.T) {
	state := &SessionState{
		Session: pdksessionMockedActive("alpha", "hr-1"),
		Runs:    []RunSummary{{Name: "hr-1", Phase: paddockv1alpha1.HarnessRunPhaseRunning}},
	}
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": state},
		SessionOrder: []string{"alpha"},
		Focused:      "alpha",
		PromptInput:  "second prompt",
	}
	m, submit := handlePromptSubmit(m)
	if submit != "" {
		t.Errorf("expected no immediate submit, got %q", submit)
	}
	if state.Queue.Len() != 1 || state.Queue.Peek() != "second prompt" {
		t.Errorf("prompt not queued: %v", state.Queue.Items())
	}
	if m.PromptInput != "" {
		t.Errorf("input not cleared after submit: %q", m.PromptInput)
	}
}

func TestPromptSubmit_FiresWhenIdle(t *testing.T) {
	state := &SessionState{
		Session: pdksessionMockedIdle("alpha"),
	}
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": state},
		SessionOrder: []string{"alpha"},
		Focused:      "alpha",
		PromptInput:  "first prompt",
	}
	_, submit := handlePromptSubmit(m)
	if submit != "first prompt" {
		t.Errorf("expected immediate submit, got %q", submit)
	}
}

func pdksessionMockedActive(name, runRef string) pdksession.Session {
	return pdksession.Session{Name: name, ActiveRunRef: runRef, Phase: paddockv1alpha1.WorkspacePhaseActive, LastActivity: time.Now()}
}

func pdksessionMockedIdle(name string) pdksession.Session {
	return pdksession.Session{Name: name, Phase: paddockv1alpha1.WorkspacePhaseActive, LastActivity: time.Now()}
}
