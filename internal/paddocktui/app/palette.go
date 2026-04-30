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

import "strings"

// PaletteCmd identifies a recognised palette command. PaletteEmpty
// covers the "user opened the palette but hasn't typed anything yet"
// case; PaletteUnknown carries the typed token back to the caller for
// error reporting.
type PaletteCmd int

const (
	PaletteEmpty PaletteCmd = iota
	PaletteCancel
	PaletteEnd
	PaletteInteractive
	PaletteTemplate
	PaletteReattach
	PaletteStatus
	PaletteEdit
	PaletteHelp
	PaletteUnknown
)

// PaletteState tracks the palette overlay's runtime state. Closed
// palettes carry no input; opening starts with an empty buffer.
type PaletteState struct {
	open  bool
	input string
}

func (p PaletteState) Open() bool    { return p.open }
func (p PaletteState) Input() string { return p.input }

// WithOpen toggles the palette open/closed. Closing clears any
// in-progress input so the next open lands on an empty prompt.
func (p PaletteState) WithOpen(open bool) PaletteState {
	if !open {
		p.input = ""
	}
	p.open = open
	return p
}

// WithInput sets the in-progress input string. Caller is responsible
// for keeping the palette open; closed palettes ignore input writes
// (a no-op so the field stays "" on close).
func (p PaletteState) WithInput(s string) PaletteState {
	if !p.open {
		return p
	}
	p.input = s
	return p
}

// ParsePalette classifies a palette command line. The returned arg is
// any whitespace-separated tail (e.g. for `template claude-code`).
// Empty input returns PaletteEmpty so the dispatcher can treat
// Enter-on-empty as a no-op cleanly.
func ParsePalette(input string) (PaletteCmd, string) {
	in := strings.TrimSpace(input)
	if in == "" {
		return PaletteEmpty, ""
	}
	parts := strings.SplitN(in, " ", 2)
	head := parts[0]
	arg := ""
	if len(parts) == 2 {
		arg = strings.TrimSpace(parts[1])
	}
	switch head {
	case "cancel":
		return PaletteCancel, ""
	case "end":
		return PaletteEnd, ""
	case "interactive":
		return PaletteInteractive, ""
	case "template":
		return PaletteTemplate, arg
	case "reattach":
		return PaletteReattach, ""
	case "status":
		return PaletteStatus, ""
	case "edit":
		return PaletteEdit, ""
	case "help":
		return PaletteHelp, ""
	default:
		return PaletteUnknown, head
	}
}
