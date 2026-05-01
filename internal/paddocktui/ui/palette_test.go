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
	"strings"
	"testing"

	"paddock.dev/paddock/internal/paddocktui/app"
)

func TestPaletteView_Closed(t *testing.T) {
	if got := PaletteView(app.PaletteState{}, 80); got != "" {
		t.Errorf("closed palette should render empty; got %q", got)
	}
}

func TestPaletteView_OpenShowsHints(t *testing.T) {
	var p app.PaletteState
	p = p.WithOpen(true)
	p = p.WithInput("can")
	out := PaletteView(p, 80)
	if !strings.Contains(out, "can") {
		t.Errorf("palette overlay should echo current input; got\n%s", out)
	}
	if !strings.Contains(out, "cancel") {
		t.Errorf("palette overlay should hint matching commands; got\n%s", out)
	}
}
