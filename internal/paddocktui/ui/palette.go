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
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/tjorri/paddock/internal/paddocktui/app"
)

// paletteCommands is the static catalogue rendered into hint rows when
// the palette is open. Order is the order shown to the user; prefix
// match against the current input filters the visible set.
var paletteCommands = []string{
	"cancel",
	"end",
	"interactive",
	"template <name>",
	"reattach",
	"status",
	"edit",
	"help",
}

// PaletteView renders the command palette overlay. Returns the empty
// string when the palette is closed so the caller can layer it
// unconditionally.
func PaletteView(p app.PaletteState, width int) string {
	if !p.Open() {
		return ""
	}
	in := p.Input()
	var hints []string
	for _, c := range paletteCommands {
		if strings.HasPrefix(c, in) {
			hints = append(hints, "  "+c)
		}
	}
	if len(hints) == 0 {
		hints = append(hints, "  (no matching commands)")
	}
	body := strings.Join(append([]string{"› " + in + "_"}, hints...), "\n")
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(width - 4)
	return style.Render(fmt.Sprintf("Command palette (Esc to close)\n%s", body))
}
