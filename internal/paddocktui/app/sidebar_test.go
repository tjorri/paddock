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
	if m.Focused != "charlie" {
		t.Errorf("expected charlie focused, got %q", m.Focused)
	}
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.Focused != NewSessionSentinel {
		t.Errorf("expected sentinel focused after stepping past last session, got %q", m.Focused)
	}
	// One more Down clamps to the sentinel — the sentinel is the last
	// row by design.
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.Focused != NewSessionSentinel {
		t.Errorf("expected sentinel to be the bounded last row, got %q", m.Focused)
	}
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.Focused != "charlie" {
		t.Errorf("expected to step back from sentinel to charlie, got %q", m.Focused)
	}
}

func TestSidebar_Filter(t *testing.T) {
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": {}, "bravo-2": {}, "bravo-3": {}, "charlie": {}},
		SessionOrder: []string{"alpha", "bravo-2", "bravo-3", "charlie"},
		Filter:       "bravo",
	}
	got := visibleSessions(m)
	// Sentinel is always appended to the end of visible regardless of filter.
	if len(got) != 3 || got[0] != "bravo-2" || got[1] != "bravo-3" || got[2] != NewSessionSentinel {
		t.Errorf("filter wrong: %v", got)
	}
}

func TestSidebar_SentinelAlwaysPresent(t *testing.T) {
	// Even with no real sessions, the sentinel anchors the visible
	// list so the user can arrow down to "[+ new session]" and press
	// Enter to create their first session.
	m := Model{}
	got := visibleSessions(m)
	if len(got) != 1 || got[0] != NewSessionSentinel {
		t.Errorf("expected just the sentinel on empty list, got %v", got)
	}
}
