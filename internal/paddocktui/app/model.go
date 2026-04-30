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
	paddockbroker "paddock.dev/paddock/internal/paddocktui/broker"
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

	// BrokerClient is the authenticated HTTP+WebSocket client for the
	// paddock-broker. Nil until Phase 5 wire-up populates it from the
	// binary entry point. Unit tests that do not exercise the broker
	// call leaf leave it nil.
	BrokerClient *paddockbroker.Client

	// Lifecycle context for long-lived watch goroutines. Cancel is
	// called on tea.Quit/teardown so the polling goroutines spawned by
	// session.Watch / runs.Watch / events.Tail exit cleanly.
	ctx    context.Context
	cancel context.CancelFunc

	// Persistent watch channels. These are opened once per
	// (workspace|run) and read one event at a time by the reducer so
	// re-issuing nextXxxEventCmd does not spawn a fresh goroutine.
	sessionWatchCh    <-chan pdksession.Event
	runWatches        map[string]<-chan pdkruns.Event                // keyed by workspaceRef
	eventTails        map[string]<-chan paddockv1alpha1.PaddockEvent // keyed by runName
	interactiveFrames map[string]<-chan paddockbroker.StreamFrame    // keyed by runName

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

	// PendingPrompt holds a single submitted prompt that's waiting for
	// the broker to stop returning 409 (an in-flight turn on the bound
	// interactive run). Submitting another prompt while non-empty
	// replaces this one. The status footer surfaces a hint.
	PendingPrompt string

	// Palette tracks the command palette overlay's open/closed state and
	// in-progress input. See palette.go.
	Palette PaletteState

	// Modal-specific state, set when Modal != ModalNone.
	ModalNew   *NewSessionModalState
	ModalEnd   *EndSessionModalState
	ModalHelp  bool
	ModalQueue bool

	// availableTemplates is the cached HarnessTemplate list shown by
	// the new-session modal. Populated by loadTemplatesCmd on Init and
	// refreshed each time the user opens the modal.
	availableTemplates []pdksession.TemplateInfo

	// MainScrollFromBottom is the number of lines the main pane has
	// been scrolled UP from the bottom. 0 means stick to bottom (the
	// most recent run is fully visible). PgUp/PgDown adjust this in
	// the reducer; the View slices the rendered content accordingly.
	MainScrollFromBottom int

	// RunCursor indexes into the focused session's Runs slice for
	// keyboard navigation. Only meaningful when FocusArea == FocusMainPane.
	RunCursor int
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
		loadTemplatesCmd(m.Client, m.Namespace),
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
