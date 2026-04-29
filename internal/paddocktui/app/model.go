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
	tea "github.com/charmbracelet/bubbletea"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Model is the Bubble Tea model for paddock-tui. Everything that
// renders or affects rendering lives here. Async work is driven by
// tea.Cmd values returned from Update — see commands.go.
type Model struct {
	// Cluster wiring.
	Client    client.Client
	Namespace string

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
	return Model{
		Client:    c,
		Namespace: ns,
		Sessions:  map[string]*SessionState{},
	}
}

// Init kicks off the initial session-list load and the watch loop.
// Wired in Task 18 (commands.go) — body is commented out until then.
func (m Model) Init() tea.Cmd {
	// return tea.Batch(loadSessionsCmd(m.Client, m.Namespace), watchSessionsCmd(m.Client, m.Namespace))
	return nil
}

// Update is implemented in update.go (Task 19).
// View is implemented in ui/view.go (Task 22).

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
