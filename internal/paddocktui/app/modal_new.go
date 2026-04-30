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

import tea "github.com/charmbracelet/bubbletea"

// handleNewSessionModalKey progresses through Name → Template → Storage → Seed
// fields with Tab/Shift-Tab; Enter on the last field signals submit
// (returned via second value).
//
// On the template field (1), Up/Down or k/j cycle through the
// available picks. Other fields treat KeyRunes as appended input.
func handleNewSessionModalKey(m Model, key tea.KeyMsg) Model {
	if m.ModalNew == nil {
		return m
	}
	switch key.Type {
	case tea.KeyTab:
		m.ModalNew.Field = (m.ModalNew.Field + 1) % 4
	case tea.KeyShiftTab:
		m.ModalNew.Field = (m.ModalNew.Field + 3) % 4
	case tea.KeyEsc:
		m = closeModal(m)
	case tea.KeyUp:
		if m.ModalNew.Field == 1 {
			m.ModalNew.TemplateIdx = clampTemplateIdx(m.ModalNew, m.ModalNew.TemplateIdx-1)
		}
	case tea.KeyDown:
		if m.ModalNew.Field == 1 {
			m.ModalNew.TemplateIdx = clampTemplateIdx(m.ModalNew, m.ModalNew.TemplateIdx+1)
		}
	case tea.KeyBackspace:
		switch m.ModalNew.Field {
		case 0:
			m.ModalNew.NameInput = trimLastRune(m.ModalNew.NameInput)
		case 2:
			m.ModalNew.StorageInput = trimLastRune(m.ModalNew.StorageInput)
		case 3:
			m.ModalNew.SeedRepoInput = trimLastRune(m.ModalNew.SeedRepoInput)
		}
	case tea.KeyRunes:
		// Append rune to the active field's input.
		switch m.ModalNew.Field {
		case 0:
			m.ModalNew.NameInput += string(key.Runes)
		case 1:
			// Template picker. j/k cycle the list; other runes are a
			// no-op so users can't corrupt the selection by typing.
			switch string(key.Runes) {
			case "k":
				m.ModalNew.TemplateIdx = clampTemplateIdx(m.ModalNew, m.ModalNew.TemplateIdx-1)
			case "j":
				m.ModalNew.TemplateIdx = clampTemplateIdx(m.ModalNew, m.ModalNew.TemplateIdx+1)
			}
		case 2:
			m.ModalNew.StorageInput += string(key.Runes)
		case 3:
			m.ModalNew.SeedRepoInput += string(key.Runes)
		}
	case tea.KeySpace:
		// Bubble Tea fires KeySpace separately from KeyRunes; without this
		// case the user can't type spaces in the text fields.
		switch m.ModalNew.Field {
		case 0:
			m.ModalNew.NameInput += " "
		case 2:
			m.ModalNew.StorageInput += " "
		case 3:
			m.ModalNew.SeedRepoInput += " "
		}
	}
	return m
}

// clampTemplateIdx bounds idx to [0, len(TemplatePicks)-1]. An empty
// picks list collapses to 0, which the modal-submit path treats as
// "no template selected".
func clampTemplateIdx(s *NewSessionModalState, idx int) int {
	n := len(s.TemplatePicks)
	if n == 0 {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

// trimLastRune drops the final rune from s; safe on empty input. Used
// by Backspace handling so multi-byte runes aren't sliced mid-codepoint.
func trimLastRune(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return string(r[:len(r)-1])
}

func openNewSessionModal(m Model, templates []string) Model {
	m.Modal = ModalNew
	m.ModalNew = &NewSessionModalState{
		StorageInput:  "10Gi",
		TemplatePicks: templates,
	}
	return m
}
