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

	"github.com/tjorri/paddock/internal/paddocktui/app"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func TestSidebarView_Basic(t *testing.T) {
	m := app.Model{
		Sessions: map[string]*app.SessionState{
			"alpha":   {Session: pdksession.Session{Name: "alpha", ActiveRunRef: "hr-1"}},
			"bravo":   {Session: pdksession.Session{Name: "bravo"}},
			"charlie": {Session: pdksession.Session{Name: "charlie"}},
		},
		SessionOrder: []string{"alpha", "bravo", "charlie"},
		Focused:      "alpha",
	}
	got := SidebarView(m)
	golden := filepath.Join("testdata", "sidebar_basic.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.WriteFile(golden, []byte(got), 0o600) //nolint:gosec // test helper writes to repo testdata
	}
	want, err := os.ReadFile(golden) //nolint:gosec // test golden file path is always under testdata/
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("sidebar mismatch.\n--- got\n%s\n--- want\n%s", got, want)
	}
}
