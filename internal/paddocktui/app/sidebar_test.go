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

func TestSidebar_MoveSelection(t *testing.T) {
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": {}, "bravo": {}, "charlie": {}},
		SessionOrder: []string{"alpha", "bravo", "charlie"},
		FocusArea:    FocusSidebar,
		Focused:      "alpha",
	}
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.Focused != "bravo" {
		t.Errorf("expected bravo focused, got %q", m.Focused)
	}
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyDown})
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyDown}) // bounded — no wrap
	if m.Focused != "charlie" {
		t.Errorf("expected charlie focused, got %q", m.Focused)
	}
}

func TestSidebar_Filter(t *testing.T) {
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": {}, "bravo-2": {}, "bravo-3": {}, "charlie": {}},
		SessionOrder: []string{"alpha", "bravo-2", "bravo-3", "charlie"},
		Filter:       "bravo",
	}
	got := visibleSessions(m)
	if len(got) != 2 || got[0] != "bravo-2" || got[1] != "bravo-3" {
		t.Errorf("filter wrong: %v", got)
	}
}
