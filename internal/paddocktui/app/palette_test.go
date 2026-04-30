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

import "testing"

func TestParsePalette_KnownCommands(t *testing.T) {
	cases := []struct {
		in   string
		want PaletteCmd
		arg  string
	}{
		{"", PaletteEmpty, ""},
		{"cancel", PaletteCancel, ""},
		{"end", PaletteEnd, ""},
		{"interactive", PaletteInteractive, ""},
		{"template claude-code", PaletteTemplate, "claude-code"},
		{"reattach", PaletteReattach, ""},
		{"status", PaletteStatus, ""},
		{"edit", PaletteEdit, ""},
		{"help", PaletteHelp, ""},
		{"bogus", PaletteUnknown, "bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, arg := ParsePalette(tc.in)
			if got != tc.want || arg != tc.arg {
				t.Errorf("ParsePalette(%q) = %v %q, want %v %q",
					tc.in, got, arg, tc.want, tc.arg)
			}
		})
	}
}

func TestPaletteState_Open_Close(t *testing.T) {
	var s PaletteState
	if s.Open() {
		t.Fatal("expected closed by default")
	}
	s = s.WithOpen(true)
	if !s.Open() {
		t.Fatal("expected open after WithOpen(true)")
	}
	s = s.WithInput("can")
	if s.Input() != "can" {
		t.Errorf("Input = %q, want %q", s.Input(), "can")
	}
	s = s.WithOpen(false)
	if s.Input() != "" {
		t.Errorf("closed palette should clear input; got %q", s.Input())
	}
}
