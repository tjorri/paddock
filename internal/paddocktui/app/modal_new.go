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
	case tea.KeyRunes:
		// Append rune to the active field's input.
		switch m.ModalNew.Field {
		case 0:
			m.ModalNew.NameInput += string(key.Runes)
		case 1:
			// Template picker: left/right or first-letter match.
			// Implement as a dropdown if list is small.
		case 2:
			m.ModalNew.StorageInput += string(key.Runes)
		case 3:
			m.ModalNew.SeedRepoInput += string(key.Runes)
		}
	}
	return m
}

func openNewSessionModal(m Model, templates []string) Model {
	m.Modal = ModalNew
	m.ModalNew = &NewSessionModalState{
		StorageInput:  "10Gi",
		TemplatePicks: templates,
	}
	return m
}
