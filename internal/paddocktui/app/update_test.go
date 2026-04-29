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
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

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
