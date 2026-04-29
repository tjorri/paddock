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

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	pdkruns "paddock.dev/paddock/internal/paddocktui/runs"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

// Model is the Bubble Tea model for paddock-tui. Everything that
// renders or affects rendering lives here. Async work is driven by
// tea.Cmd values returned from Update — see commands.go.
type Model struct {
	// Cluster wiring.
	Client    client.Client
	Namespace string

	// Lifecycle context for long-lived watch goroutines. Cancel is
	// called on tea.Quit/teardown so the polling goroutines spawned by
	// session.Watch / runs.Watch / events.Tail exit cleanly.
	ctx    context.Context
	cancel context.CancelFunc

	// Persistent watch channels. These are opened once per
	// (workspace|run) and read one event at a time by the reducer so
	// re-issuing nextXxxEventCmd does not spawn a fresh goroutine.
	sessionWatchCh <-chan pdksession.Event
	runWatches     map[string]<-chan pdkruns.Event                // keyed by workspaceRef
	eventTails     map[string]<-chan paddockv1alpha1.PaddockEvent // keyed by runName

	// Session list, keyed by Name. SessionOrder gives display order.
	Sessions     map[string]*SessionState
	SessionOrder []string
	Focused      string // session name; "" when no session selected.

	// UI state.
	FocusArea   FocusArea
	Modal       ModalKind
	PromptInput string
	Filter      string
	ErrBanner   string

	// Modal-specific state, set when Modal != ModalNone.
	ModalNew   *NewSessionModalState
	ModalEnd   *EndSessionModalState
	ModalHelp  bool
	ModalQueue bool
}

// NewModel constructs a Model with the supplied cluster wiring.
func NewModel(c client.Client, ns string) Model {
	ctx, cancel := context.WithCancel(context.Background())
	return Model{
		Client:     c,
		Namespace:  ns,
		ctx:        ctx,
		cancel:     cancel,
		Sessions:   map[string]*SessionState{},
		runWatches: map[string]<-chan pdkruns.Event{},
		eventTails: map[string]<-chan paddockv1alpha1.PaddockEvent{},
	}
}

// Init kicks off the initial session-list load and opens the session
// watch. The session watch is opened once for the lifetime of the
// Model — Update re-issues nextSessionEventCmd to read further events
// off the same channel without spawning new goroutines.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		loadSessionsCmd(m.Client, m.Namespace),
		openSessionsWatchCmd(m.ctx, m.Client, m.Namespace),
	)
}

// Update is implemented in update.go (Task 19).
//
// View is a placeholder so Model satisfies tea.Model — the real
// rendering lives in package ui (Task 22) and is wired in by the
// cmd-package teaModel adapter that embeds Model.
func (m Model) View() string { return "" }

// Modal-state placeholders — implementations land in Task 17.
type NewSessionModalState struct {
	NameInput     string
	TemplatePicks []string // populated from session.ListTemplates
	TemplateIdx   int
	StorageInput  string
	SeedRepoInput string
	Field         int // 0=name, 1=template, 2=storage, 3=seed
}

type EndSessionModalState struct {
	TargetName string
	Confirmed  bool
}
