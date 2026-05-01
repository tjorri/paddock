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

// Package ui contains View functions and Lipgloss styles for the TUI.
// Pure: Model -> string. Bubble Tea's runtime calls these on every
// frame; keep them allocation-light.
package ui

import "github.com/charmbracelet/lipgloss"

var (
	StyleSidebarFrame = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, true, false, false).
				Padding(0, 1).
				Width(28)

	StyleSidebarRowFocused = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	StyleSidebarRowNormal  = lipgloss.NewStyle()
	StyleSidebarRowFailed  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	StyleSidebarRowRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	StyleHeader    = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	StyleStatusBar = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	StyleErrBanner = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true).Padding(0, 1)
	StyleRunHeader = lipgloss.NewStyle().Bold(true)
	StyleRunFooter = lipgloss.NewStyle().Faint(true)
	// StyleRunCursor is applied to the header of the run currently
	// selected by RunCursor when FocusArea == FocusMainPane.
	StyleRunCursor  = lipgloss.NewStyle().Bold(true).Reverse(true)
	StylePromptArea = lipgloss.NewStyle().Padding(0, 1).Width(60)
	StyleModalFrame = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
)
