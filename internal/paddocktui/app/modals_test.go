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
)

func TestNewSessionModal_FieldNavigation(t *testing.T) {
	m := Model{Modal: ModalNew, ModalNew: &NewSessionModalState{Field: 0}}
	m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ModalNew.Field != 1 {
		t.Errorf("expected Field=1 after Tab, got %d", m.ModalNew.Field)
	}
}

func TestNewSessionModal_TemplateFieldCyclesPicks(t *testing.T) {
	m := Model{Modal: ModalNew, ModalNew: &NewSessionModalState{
		Field:         1,
		TemplatePicks: []string{"echo", "claude-code", "scout"},
	}}
	m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.ModalNew.TemplateIdx != 1 {
		t.Errorf("expected idx=1 after Down, got %d", m.ModalNew.TemplateIdx)
	}
	m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if m.ModalNew.TemplateIdx != 2 {
		t.Errorf("expected idx=2 after j, got %d", m.ModalNew.TemplateIdx)
	}
	// Bounded — does not overflow.
	m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.ModalNew.TemplateIdx != 2 {
		t.Errorf("expected idx clamped to 2, got %d", m.ModalNew.TemplateIdx)
	}
	m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if m.ModalNew.TemplateIdx != 1 {
		t.Errorf("expected idx=1 after k, got %d", m.ModalNew.TemplateIdx)
	}
}

func TestNewSessionModal_SpaceAppendsToActiveField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field int
	}{
		{"name", 0},
		{"storage", 2},
		{"seed", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{Modal: ModalNew, ModalNew: &NewSessionModalState{Field: tc.field}}
			m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
			m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeySpace})
			m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
			var got string
			switch tc.field {
			case 0:
				got = m.ModalNew.NameInput
			case 2:
				got = m.ModalNew.StorageInput
			case 3:
				got = m.ModalNew.SeedRepoInput
			}
			if got != "a b" {
				t.Errorf("field %s = %q, want %q", tc.name, got, "a b")
			}
		})
	}
}

func TestNewSessionModal_BackspaceTrimsActiveField(t *testing.T) {
	m := Model{Modal: ModalNew, ModalNew: &NewSessionModalState{Field: 0, NameInput: "abc"}}
	m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.ModalNew.NameInput != "ab" {
		t.Errorf("expected name=ab, got %q", m.ModalNew.NameInput)
	}
}

func TestEndSessionModal_RequiresExplicitConfirm(t *testing.T) {
	m := Model{Modal: ModalEnd, ModalEnd: &EndSessionModalState{TargetName: "alpha"}}
	m, confirmed := handleEndSessionModalKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !confirmed {
		t.Errorf("expected confirmation on Enter")
	}
	if m.Modal != ModalNone {
		t.Errorf("expected modal closed after confirm")
	}
}

func TestHelpModal_OpensAndCloses(t *testing.T) {
	m := Model{Modal: ModalNone}
	m = openHelpModal(m)
	if m.Modal != ModalHelp {
		t.Errorf("help did not open")
	}
	m = closeModal(m)
	if m.Modal != ModalNone {
		t.Errorf("modal not closed")
	}
}

func TestOpenNewSessionModal_SetsDefaults(t *testing.T) {
	m := openNewSessionModal(Model{}, []string{"echo", "claude-code"})
	if m.Modal != ModalNew {
		t.Errorf("expected ModalNew, got %v", m.Modal)
	}
	if m.ModalNew.StorageInput != "10Gi" {
		t.Errorf("expected default storage 10Gi, got %q", m.ModalNew.StorageInput)
	}
	if len(m.ModalNew.TemplatePicks) != 2 {
		t.Errorf("expected 2 template picks, got %d", len(m.ModalNew.TemplatePicks))
	}
}

func TestOpenEndSessionModal_SetsTarget(t *testing.T) {
	m := openEndSessionModal(Model{}, "beta")
	if m.Modal != ModalEnd {
		t.Errorf("expected ModalEnd, got %v", m.Modal)
	}
	if m.ModalEnd.TargetName != "beta" {
		t.Errorf("expected target beta, got %q", m.ModalEnd.TargetName)
	}
}
