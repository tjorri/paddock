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
