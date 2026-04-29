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

// Package app holds the Bubble Tea Model, Update, View, and message
// types for the paddock-tui interactive UI.
package app

import (
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

// FocusArea is the area of the TUI that currently receives input.
type FocusArea int

const (
	FocusSidebar FocusArea = iota
	FocusPrompt
	FocusMainScroll
)

// ModalKind names which modal (if any) is open.
type ModalKind int

const (
	ModalNone ModalKind = iota
	ModalNew
	ModalEnd
	ModalHelp
	ModalQueue
)

// SessionState bundles the runtime state for one session held in TUI
// memory.
type SessionState struct {
	Session pdksession.Session

	// Runs is the list of HarnessRuns for this session, newest first.
	Runs []RunSummary

	// Events keyed by run name. Only populated for the focused session.
	Events map[string][]paddockv1alpha1.PaddockEvent

	// Queue of prompts pending while a run is in flight.
	Queue Queue
}

// RunSummary is a TUI-shaped projection of a HarnessRun.
type RunSummary struct {
	Name           string
	Phase          paddockv1alpha1.HarnessRunPhase
	Prompt         string
	StartTime      time.Time
	CompletionTime time.Time
	Template       string
}

// IsTerminal reports whether the run has reached a terminal phase.
func (r RunSummary) IsTerminal() bool {
	switch r.Phase {
	case paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed,
		paddockv1alpha1.HarnessRunPhaseCancelled:
		return true
	}
	return false
}
