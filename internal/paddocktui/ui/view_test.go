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

package ui

import (
	"os"
	"path/filepath"
	"testing"

	"paddock.dev/paddock/internal/paddocktui/app"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

func TestView_EmptyState(t *testing.T) {
	m := app.Model{Sessions: map[string]*app.SessionState{}, FocusArea: app.FocusSidebar, Namespace: "default"}
	got := View(m, 80, 24)
	checkGolden(t, got, "view_empty.golden")
}

func TestView_OneSessionFocused(t *testing.T) {
	m := app.Model{
		Sessions:     map[string]*app.SessionState{"alpha": {Session: pdksession.Session{Name: "alpha"}}},
		SessionOrder: []string{"alpha"},
		Focused:      "alpha",
		FocusArea:    app.FocusPrompt,
		Namespace:    "default",
	}
	got := View(m, 80, 24)
	checkGolden(t, got, "view_one_session.golden")
}

func TestView_HelpModalOpen(t *testing.T) {
	m := app.Model{Sessions: map[string]*app.SessionState{}, FocusArea: app.FocusSidebar, Modal: app.ModalHelp, Namespace: "default"}
	got := View(m, 80, 24)
	checkGolden(t, got, "view_help_modal.golden")
}

func checkGolden(t *testing.T, got, name string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.WriteFile(path, []byte(got), 0o600) //nolint:gosec // test helper writes golden files
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // path is constructed from a fixed testdata dir + test-controlled name
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch.\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}
