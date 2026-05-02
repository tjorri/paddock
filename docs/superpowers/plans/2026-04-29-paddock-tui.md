# paddock-tui Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `paddock-tui`, a self-contained client binary that gives users a Bubble Tea–based interactive multi-session TUI on top of Paddock's existing `Workspace`, `HarnessRun`, and `PaddockEvent` surface.

**Architecture:** New code lives under `cmd/paddock-tui/` and `internal/paddocktui/` with a strict no-imports-from-paddock-internal rule (only `github.com/tjorri/paddock/api/v1alpha1` plus external libs). Sessions are long-lived `Workspaces` labeled `paddock.dev/session=true`; each user prompt becomes one `HarnessRun` against the session's `Workspace`; the TUI watches `Workspace` and `HarnessRun` resources, dedupes `PaddockEvents` from `HarnessRun.status.recentEvents`, and synthesizes per-run boundaries client-side. No CRD, controller, broker, or adapter changes.

**Tech Stack:** Go 1.26, `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles`, `github.com/charmbracelet/lipgloss`, `sigs.k8s.io/controller-runtime/pkg/client`, `k8s.io/cli-runtime/pkg/genericclioptions`, `github.com/tjorri/paddock/api/v1alpha1`. Tests use standard `testing` plus `sigs.k8s.io/controller-runtime/pkg/client/fake`.

**Spec:** `docs/superpowers/specs/2026-04-29-paddock-tui-design.md`.

---

## File structure

Files created by this plan (no existing file is modified except `go.mod`/`go.sum`/`Makefile`):

| Path | Responsibility |
|---|---|
| `cmd/paddock-tui/main.go` | Binary entrypoint; wires the cobra root from `internal/paddocktui/cmd`. |
| `internal/paddocktui/cmd/root.go` | Cobra root command with `--kubeconfig`/`--context`/`--namespace`. Registers all subcommands. Defaults to launching the TUI when invoked with no args. |
| `internal/paddocktui/cmd/version.go` | `paddock-tui version`. |
| `internal/paddocktui/cmd/tui.go` | Launches the Bubble Tea program. |
| `internal/paddocktui/cmd/session.go` | Parent group `paddock-tui session ...`. |
| `internal/paddocktui/cmd/session_list.go` | Non-TUI `session list`. |
| `internal/paddocktui/cmd/session_new.go` | `session new --no-tui` (and the path that opens the TUI focused on a new session). |
| `internal/paddocktui/cmd/session_end.go` | `session end NAME [--yes]`. |
| `internal/paddocktui/session/labels.go` | Label and annotation constants + helpers. |
| `internal/paddocktui/session/types.go` | `Session` value type and `RunSummary`. |
| `internal/paddocktui/session/list.go` | `List(ctx, ns) ([]Session, error)`. |
| `internal/paddocktui/session/create.go` | `Create(ctx, opts) (Session, error)`. |
| `internal/paddocktui/session/end.go` | `End(ctx, name, ns) error`. |
| `internal/paddocktui/session/watch.go` | `Watch(ctx, ns) (<-chan SessionEvent, error)`. |
| `internal/paddocktui/session/templates.go` | `ListTemplates(ctx, ns) ([]TemplateInfo, error)`. |
| `internal/paddocktui/runs/create.go` | `Create(ctx, opts) (string, error)` — creates a `HarnessRun` against a session. |
| `internal/paddocktui/runs/watch.go` | `Watch(ctx, ns, workspaceRef) (<-chan RunEvent, error)`. |
| `internal/paddocktui/runs/cancel.go` | `Cancel(ctx, ns, name) error`. |
| `internal/paddocktui/events/dedupe.go` | `Dedupe.AddIfNew(ev) bool`. |
| `internal/paddocktui/events/tail.go` | `Tail(ctx, ns, runName) (<-chan paddockv1alpha1.PaddockEvent, error)`. |
| `internal/paddocktui/app/types.go` | TUI-side value types: `Session`, `RunSummary`, `FocusArea`, `ModalKind`, `Queue`. |
| `internal/paddocktui/app/messages.go` | Bubble Tea messages: `sessionsLoadedMsg`, `sessionUpdatedMsg`, `runUpdatedMsg`, `eventReceivedMsg`, `runCreatedMsg`, `errMsg`. |
| `internal/paddocktui/app/model.go` | `Model` struct + `Init`. |
| `internal/paddocktui/app/queue.go` | Per-session prompt queue. |
| `internal/paddocktui/app/update.go` | `Update(msg)` reducer. Splits per area: sidebar, prompt, modals, slash. |
| `internal/paddocktui/app/sidebar.go` | Sidebar state transitions (selection, filter, scroll). |
| `internal/paddocktui/app/prompt.go` | Prompt input state and submit/cancel handling. |
| `internal/paddocktui/app/modal_new.go` | New-session modal state. |
| `internal/paddocktui/app/modal_end.go` | End-session modal state. |
| `internal/paddocktui/app/modal_help.go` | Help modal state. |
| `internal/paddocktui/app/slash.go` | Parse `:`-prefixed slash commands. |
| `internal/paddocktui/app/commands.go` | Bubble Tea `tea.Cmd` constructors that wrap the helper packages. |
| `internal/paddocktui/ui/styles.go` | Lip Gloss styles. |
| `internal/paddocktui/ui/sidebar.go` | Sidebar `View`. |
| `internal/paddocktui/ui/mainpane.go` | Main pane `View` (run timeline + prompt input). |
| `internal/paddocktui/ui/modal_new.go` | New-session modal `View`. |
| `internal/paddocktui/ui/modal_end.go` | End-session modal `View`. |
| `internal/paddocktui/ui/modal_help.go` | Help modal `View`. |
| `internal/paddocktui/ui/view.go` | Root `View` — assembles sidebar, main pane, status footer, modal overlay. |
| `Makefile` | Add `paddock-tui` build target. |
| `go.mod` / `go.sum` | Add Bubble Tea / Bubbles / Lip Gloss. |

Test files mirror their non-test counterpart with `_test.go` suffix.

---

## Coding conventions

- **Imports.** `internal/paddocktui/...` may import `github.com/tjorri/paddock/api/v1alpha1` and external libraries only. Forbidden: `internal/cli/`, `internal/broker/`, `internal/controller/`, `internal/auditing/`, `internal/policy/`, `internal/proxy/`, `internal/webhook/`, `internal/brokerclient/`. If a piece of `internal/cli` logic is useful (e.g. event dedupe), copy it — don't import.
- **License header.** Every new `.go` file gets the project's standard 2026 Apache-2.0 header (copy from `cmd/kubectl-paddock/main.go:1-15`).
- **Tests.** Standard `testing.T` table tests. Use `sigs.k8s.io/controller-runtime/pkg/client/fake` for Kubernetes interactions. No Ginkgo in this package — match the `internal/cli/*_test.go` style. Use `t.Helper()` in shared helpers.
- **Commits.** Conventional Commits with `paddock-tui` scope: `feat(paddock-tui): ...`, `test(paddock-tui): ...`, `chore(paddock-tui): ...`. After every successful task, commit before moving on.
- **Pre-commit hook.** `hack/pre-commit.sh` runs `go vet -tags=e2e ./...` and `golangci-lint run`. If you have it installed (`make hooks-install`), commits will fail until both pass. If a hook fails, **fix and create a NEW commit — do not amend.**

---

## Phase A — Skeleton

### Task 1: Add TUI dependencies and `make paddock-tui`

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `Makefile:165-176` (add a target alongside `cli`)

- [ ] **Step 1: Add Bubble Tea / Bubbles / Lip Gloss to go.mod**

```bash
cd /Users/ttj/projects/personal/paddock
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
go mod tidy
```

Expected: `go.mod` gains three direct `require` entries; `go.sum` updated; no compile errors (no consumers yet).

- [ ] **Step 2: Add Makefile build target**

In `Makefile`, after the existing `cli:` target (around line 169-172), add:

```makefile
.PHONY: paddock-tui
paddock-tui: fmt vet ## Build the paddock-tui binary.
	go build -o bin/paddock-tui ./cmd/paddock-tui
	@echo "built bin/paddock-tui — interactive multi-session TUI for Paddock"
```

- [ ] **Step 3: Verify `make paddock-tui` fails with no main package**

Run: `make paddock-tui`
Expected: failure — `cmd/paddock-tui` does not exist yet. This is the failing-test gate before Task 2.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum Makefile
git commit -m "chore(paddock-tui): add bubbletea/bubbles/lipgloss deps and build target"
```

---

### Task 2: Binary skeleton + `version` subcommand

**Files:**
- Create: `cmd/paddock-tui/main.go`
- Create: `internal/paddocktui/cmd/root.go`
- Create: `internal/paddocktui/cmd/version.go`
- Create: `internal/paddocktui/cmd/version_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/paddocktui/cmd/version_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	cmd := newVersionCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version cmd: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "paddock-tui") {
		t.Errorf("version output missing 'paddock-tui': %q", got)
	}
	if !strings.Contains(got, "v0.1.0-dev") {
		t.Errorf("version output missing version: %q", got)
	}
}
```

- [ ] **Step 2: Verify the test fails (no compile)**

Run: `go test ./internal/paddocktui/cmd/...`
Expected: build failure — package `cmd` doesn't exist yet.

- [ ] **Step 3: Create root command**

Create `internal/paddocktui/cmd/root.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package cmd implements the paddock-tui binary's cobra command tree.
//
// The TUI is the primary UX: invoking `paddock-tui` with no subcommand
// launches the Bubble Tea program. The non-TUI subcommands
// (`session list`, `session new`, `session end`, `version`) are kept
// for scripting and one-off operations.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(paddockv1alpha1.AddToScheme(scheme))
}

// NewRootCmd builds the root cobra command. With no subcommand the
// TUI launches; with `version`, `session ...` etc. the corresponding
// non-TUI command runs.
func NewRootCmd() *cobra.Command {
	cfg := genericclioptions.NewConfigFlags(true)

	root := &cobra.Command{
		Use:           "paddock-tui",
		Short:         "Interactive multi-session TUI for Paddock",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Default action: launch the TUI. Wired in Task 27.
			return cmd.Help()
		},
	}
	cfg.AddFlags(root.PersistentFlags())

	root.AddCommand(newVersionCmd())
	// Subsequent tasks register: tui, session list, session new, session end.

	root.SetErr(os.Stderr)
	root.SetOut(os.Stdout)
	return root
}

// newClient builds a controller-runtime client from the kubectl-style
// config flags. Shared by every subcommand. Returns the resolved
// namespace as the second value.
func newClient(cfg *genericclioptions.ConfigFlags) (client.Client, string, error) {
	restConfig, err := cfg.ToRESTConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, "", fmt.Errorf("building Kubernetes client: %w", err)
	}
	ns, _, err := cfg.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("resolving namespace: %w", err)
	}
	return c, ns, nil
}
```

Create `internal/paddocktui/cmd/version.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print binary version info",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "paddock-tui v0.1.0-dev (api=paddock.dev/v1alpha1)")
			return nil
		},
	}
}
```

Create `cmd/paddock-tui/main.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// paddock-tui is the interactive multi-session TUI for Paddock.
// See internal/paddocktui for command and TUI implementations.
package main

import (
	"fmt"
	"os"

	"github.com/tjorri/paddock/internal/paddocktui/cmd"
)

func main() {
	if err := cmd.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "paddock-tui:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Verify the test passes**

Run: `go test ./internal/paddocktui/cmd/... -run TestVersionCommand -v`
Expected: PASS.

- [ ] **Step 5: Verify the binary builds and `version` works**

Run: `make paddock-tui && ./bin/paddock-tui version`
Expected output:
```
built bin/paddock-tui — interactive multi-session TUI for Paddock
paddock-tui v0.1.0-dev (api=paddock.dev/v1alpha1)
```

- [ ] **Step 6: Commit**

```bash
git add cmd/paddock-tui internal/paddocktui/cmd
git commit -m "feat(paddock-tui): cobra skeleton + version command"
```

---

## Phase B — Helper packages

These are pure-Go packages that wrap Kubernetes interactions for the TUI. Each is unit-tested against `controller-runtime`'s fake client.

### Task 3: Session labels and types

**Files:**
- Create: `internal/paddocktui/session/labels.go`
- Create: `internal/paddocktui/session/types.go`
- Create: `internal/paddocktui/session/labels_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/paddocktui/session/labels_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestIsSession(t *testing.T) {
	tests := []struct {
		name string
		ws   paddockv1alpha1.Workspace
		want bool
	}{
		{
			name: "labeled true",
			ws: paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{SessionLabel: "true"}},
			},
			want: true,
		},
		{
			name: "labeled false",
			ws: paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{SessionLabel: "false"}},
			},
			want: false,
		},
		{
			name: "unlabeled",
			ws:   paddockv1alpha1.Workspace{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSession(&tt.ws); got != tt.want {
				t.Errorf("IsSession() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTemplateAccessors(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				DefaultTemplateAnnotation: "claude-code",
				LastTemplateAnnotation:    "echo",
			},
		},
	}
	if got := DefaultTemplate(ws); got != "claude-code" {
		t.Errorf("DefaultTemplate = %q", got)
	}
	if got := LastTemplate(ws); got != "echo" {
		t.Errorf("LastTemplate = %q", got)
	}
	// Fallback when last is missing.
	delete(ws.Annotations, LastTemplateAnnotation)
	if got := LastTemplate(ws); got != "claude-code" {
		t.Errorf("LastTemplate fallback = %q, want default", got)
	}
}
```

- [ ] **Step 2: Verify the test fails**

Run: `go test ./internal/paddocktui/session/... -v`
Expected: build failure (package doesn't exist).

- [ ] **Step 3: Implement labels and types**

Create `internal/paddocktui/session/labels.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package session contains client-side primitives for treating a
// labeled Workspace as a paddock-tui session: list/create/end/watch
// and template-default annotations.
//
// A "session" is just a Workspace with the SessionLabel set to "true".
// All cluster-side state introduced by paddock-tui lives in three
// keys: one label and two annotations.
package session

import (
	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

const (
	// SessionLabel marks a Workspace as a paddock-tui session.
	SessionLabel = "paddock.dev/session"

	// SessionLabelTrue is the value set on the SessionLabel for sessions.
	SessionLabelTrue = "true"

	// DefaultTemplateAnnotation records the HarnessTemplate the session
	// was created against. Used as a fallback when LastTemplate is unset.
	DefaultTemplateAnnotation = "paddock.dev/session-default-template"

	// LastTemplateAnnotation records the last HarnessTemplate actually
	// used by a HarnessRun in this session. The TUI updates this on
	// every prompt submission and on `:template` slash command. Falls
	// back to DefaultTemplate when missing.
	LastTemplateAnnotation = "paddock.dev/session-last-template"
)

// IsSession reports whether a Workspace carries the session label.
func IsSession(ws *paddockv1alpha1.Workspace) bool {
	if ws == nil {
		return false
	}
	return ws.Labels[SessionLabel] == SessionLabelTrue
}

// DefaultTemplate returns the session's default template annotation
// (empty string when unset).
func DefaultTemplate(ws *paddockv1alpha1.Workspace) string {
	if ws == nil {
		return ""
	}
	return ws.Annotations[DefaultTemplateAnnotation]
}

// LastTemplate returns the session's last-used template annotation,
// falling back to DefaultTemplate when LastTemplate is unset.
func LastTemplate(ws *paddockv1alpha1.Workspace) string {
	if ws == nil {
		return ""
	}
	if v := ws.Annotations[LastTemplateAnnotation]; v != "" {
		return v
	}
	return ws.Annotations[DefaultTemplateAnnotation]
}
```

Create `internal/paddocktui/session/types.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Session is a TUI-shaped projection of a labeled Workspace. It carries
// only what the TUI needs and is safe to copy across goroutines.
type Session struct {
	Name             string
	Namespace        string
	DefaultTemplate  string
	LastTemplate     string
	Phase            paddockv1alpha1.WorkspacePhase
	ActiveRunRef     string
	TotalRuns        int32
	LastActivity     time.Time
	CreationTime     time.Time
	ResourceVersion  string
}

// FromWorkspace converts a Workspace to its Session projection.
func FromWorkspace(ws *paddockv1alpha1.Workspace) Session {
	s := Session{
		Name:            ws.Name,
		Namespace:       ws.Namespace,
		DefaultTemplate: DefaultTemplate(ws),
		LastTemplate:    LastTemplate(ws),
		Phase:           ws.Status.Phase,
		ActiveRunRef:    ws.Status.ActiveRunRef,
		TotalRuns:       ws.Status.TotalRuns,
		ResourceVersion: ws.ResourceVersion,
		CreationTime:    ws.CreationTimestamp.Time,
	}
	if ws.Status.LastActivity != nil {
		s.LastActivity = ws.Status.LastActivity.Time
	}
	return s
}
```

- [ ] **Step 4: Verify the test passes**

Run: `go test ./internal/paddocktui/session/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/session
git commit -m "feat(paddock-tui): session label/annotation constants and Session type"
```

---

### Task 4: Session list

**Files:**
- Create: `internal/paddocktui/session/list.go`
- Create: `internal/paddocktui/session/list_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/paddocktui/session/list_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(paddockv1alpha1.AddToScheme(s))
	return s
}

func TestList_FiltersAndSorts(t *testing.T) {
	older := metav1.NewTime(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))

	mkSession := func(name string, label string, lastAct *metav1.Time) *paddockv1alpha1.Workspace {
		labels := map[string]string{}
		if label != "" {
			labels[SessionLabel] = label
		}
		return &paddockv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels:    labels,
			},
			Status: paddockv1alpha1.WorkspaceStatus{LastActivity: lastAct},
		}
	}

	cli := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(
			mkSession("alpha", SessionLabelTrue, &older),
			mkSession("bravo", SessionLabelTrue, &newer),
			mkSession("not-a-session", "", &newer),
			mkSession("explicit-false", "false", &newer),
		).
		Build()

	got, err := List(context.Background(), cli, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2 (filtered by label); got=%v", len(got), got)
	}
	if got[0].Name != "bravo" || got[1].Name != "alpha" {
		t.Errorf("sort order wrong: %v (want bravo before alpha by lastActivity desc)", []string{got[0].Name, got[1].Name})
	}
}
```

- [ ] **Step 2: Verify the test fails**

Run: `go test ./internal/paddocktui/session/... -run TestList_FiltersAndSorts -v`
Expected: build failure — `List` not defined.

- [ ] **Step 3: Implement List**

Create `internal/paddocktui/session/list.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"fmt"
	"sort"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// List returns sessions in ns, filtered by SessionLabel and sorted by
// LastActivity desc (CreationTime as tiebreaker).
func List(ctx context.Context, c client.Client, ns string) ([]Session, error) {
	var wsList paddockv1alpha1.WorkspaceList
	if err := c.List(ctx, &wsList, client.InNamespace(ns), client.MatchingLabels{SessionLabel: SessionLabelTrue}); err != nil {
		return nil, fmt.Errorf("listing workspaces in %s: %w", ns, err)
	}
	out := make([]Session, 0, len(wsList.Items))
	for i := range wsList.Items {
		out = append(out, FromWorkspace(&wsList.Items[i]))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return activitySortKey(out[i]).After(activitySortKey(out[j]))
	})
	return out, nil
}

func activitySortKey(s Session) time.Time {
	if !s.LastActivity.IsZero() {
		return s.LastActivity
	}
	return s.CreationTime
}
```

- [ ] **Step 4: Verify the test passes**

Run: `go test ./internal/paddocktui/session/... -run TestList_FiltersAndSorts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/session
git commit -m "feat(paddock-tui): session.List filters by label and sorts by lastActivity"
```

---

### Task 5: Session create

**Files:**
- Create: `internal/paddocktui/session/create.go`
- Create: `internal/paddocktui/session/create_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/paddocktui/session/create_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestCreate_StampsLabelAndAnnotations(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

	got, err := Create(context.Background(), cli, CreateOptions{
		Namespace:   "default",
		Name:        "starlight-7",
		Template:    "claude-code",
		StorageSize: resource.MustParse("20Gi"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Name != "starlight-7" || got.DefaultTemplate != "claude-code" {
		t.Errorf("Session projection wrong: %+v", got)
	}

	var ws paddockv1alpha1.Workspace
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "starlight-7"}, &ws); err != nil {
		t.Fatalf("get back: %v", err)
	}
	if ws.Labels[SessionLabel] != SessionLabelTrue {
		t.Errorf("session label not set: %v", ws.Labels)
	}
	if ws.Annotations[DefaultTemplateAnnotation] != "claude-code" {
		t.Errorf("default-template annotation not set: %v", ws.Annotations)
	}
	if ws.Annotations[LastTemplateAnnotation] != "claude-code" {
		t.Errorf("last-template annotation not initialised: %v", ws.Annotations)
	}
	if ws.Spec.Ephemeral {
		t.Errorf("session must not be ephemeral")
	}
	if got, want := ws.Spec.Storage.Size.String(), "20Gi"; got != want {
		t.Errorf("storage size = %s, want %s", got, want)
	}
}

func TestCreate_WithSeedRepo(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	_, err := Create(context.Background(), cli, CreateOptions{
		Namespace:   "default",
		Name:        "with-seed",
		Template:    "claude-code",
		StorageSize: resource.MustParse("10Gi"),
		SeedRepoURL: "https://github.com/example/repo",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var ws paddockv1alpha1.Workspace
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "with-seed"}, &ws); err != nil {
		t.Fatalf("get back: %v", err)
	}
	if ws.Spec.Seed == nil || len(ws.Spec.Seed.Repos) != 1 || ws.Spec.Seed.Repos[0].URL != "https://github.com/example/repo" {
		t.Errorf("seed repo not set correctly: %+v", ws.Spec.Seed)
	}
}

func TestCreate_AlreadyExists(t *testing.T) {
	cli := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(&paddockv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "dup", Namespace: "default"},
		}).
		Build()
	_, err := Create(context.Background(), cli, CreateOptions{
		Namespace:   "default",
		Name:        "dup",
		Template:    "claude-code",
		StorageSize: resource.MustParse("10Gi"),
	})
	if err == nil {
		t.Fatal("expected AlreadyExists, got nil")
	}
}
```

- [ ] **Step 2: Verify the test fails**

Run: `go test ./internal/paddocktui/session/... -run TestCreate -v`
Expected: build failure — `Create` and `CreateOptions` undefined.

- [ ] **Step 3: Implement Create**

Create `internal/paddocktui/session/create.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// CreateOptions parameterises Create. Name is required; Namespace is
// resolved from the kubeconfig at command time and threaded through.
type CreateOptions struct {
	Namespace   string
	Name        string
	Template    string            // HarnessTemplate name; recorded as default + last template.
	StorageSize resource.Quantity // PVC size; required.
	SeedRepoURL string            // optional; if set, becomes spec.seed.repos[0].URL.
	SeedBranch  string            // optional.
}

// Create creates a new session-labeled Workspace and returns its
// Session projection. Does not wait for the Workspace controller to
// finish seeding — callers that need that should watch separately.
func Create(ctx context.Context, c client.Client, opts CreateOptions) (Session, error) {
	if opts.Name == "" {
		return Session{}, fmt.Errorf("session name is required")
	}
	if opts.Template == "" {
		return Session{}, fmt.Errorf("session template is required")
	}
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
			Labels:    map[string]string{SessionLabel: SessionLabelTrue},
			Annotations: map[string]string{
				DefaultTemplateAnnotation: opts.Template,
				LastTemplateAnnotation:    opts.Template,
			},
		},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Storage:   paddockv1alpha1.WorkspaceStorage{Size: opts.StorageSize},
			Ephemeral: false,
		},
	}
	if opts.SeedRepoURL != "" {
		ws.Spec.Seed = &paddockv1alpha1.WorkspaceSeed{
			Repos: []paddockv1alpha1.WorkspaceGitSource{
				{URL: opts.SeedRepoURL, Branch: opts.SeedBranch},
			},
		}
	}
	if err := c.Create(ctx, ws); err != nil {
		return Session{}, fmt.Errorf("creating workspace %s/%s: %w", opts.Namespace, opts.Name, err)
	}
	return FromWorkspace(ws), nil
}
```

- [ ] **Step 4: Verify the test passes**

Run: `go test ./internal/paddocktui/session/... -run TestCreate -v`
Expected: PASS (3 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/session
git commit -m "feat(paddock-tui): session.Create stamps label, annotations, and seed config"
```

---

### Task 6: Session end and watch

**Files:**
- Create: `internal/paddocktui/session/end.go`
- Create: `internal/paddocktui/session/end_test.go`
- Create: `internal/paddocktui/session/watch.go`
- Create: `internal/paddocktui/session/watch_test.go`

- [ ] **Step 1: Write end test**

Create `internal/paddocktui/session/end_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestEnd_DeletesLabeledWorkspace(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "starlight",
			Namespace: "default",
			Labels:    map[string]string{SessionLabel: SessionLabelTrue},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(ws).Build()

	if err := End(context.Background(), cli, "default", "starlight"); err != nil {
		t.Fatalf("End: %v", err)
	}
	var got paddockv1alpha1.Workspace
	err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "starlight"}, &got)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound after End, got err=%v", err)
	}
}

func TestEnd_NotASession(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "non-session", Namespace: "default"},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(ws).Build()
	if err := End(context.Background(), cli, "default", "non-session"); err == nil {
		t.Fatal("expected error for non-session workspace, got nil")
	}
}
```

- [ ] **Step 2: Implement End**

Create `internal/paddocktui/session/end.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// End deletes a session-labeled Workspace. Refuses to delete a
// Workspace that isn't a session — paddock-tui only manages its own
// labeled workspaces.
func End(ctx context.Context, c client.Client, ns, name string) error {
	var ws paddockv1alpha1.Workspace
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &ws); err != nil {
		return fmt.Errorf("fetching workspace %s/%s: %w", ns, name, err)
	}
	if !IsSession(&ws) {
		return fmt.Errorf("workspace %s/%s is not a paddock-tui session (missing %q label)", ns, name, SessionLabel)
	}
	if err := c.Delete(ctx, &ws); err != nil {
		return fmt.Errorf("deleting workspace %s/%s: %w", ns, name, err)
	}
	return nil
}
```

- [ ] **Step 3: Verify End tests pass**

Run: `go test ./internal/paddocktui/session/... -run TestEnd -v`
Expected: PASS (2 subtests).

- [ ] **Step 4: Write watch test**

Create `internal/paddocktui/session/watch_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestWatch_EmitsAddOnInitialList(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha",
			Namespace: "default",
			Labels:    map[string]string{SessionLabel: SessionLabelTrue},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(ws).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := Watch(ctx, cli, "default", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.Type != EventAdd || ev.Session.Name != "alpha" {
			t.Errorf("unexpected event %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for initial Add event")
	}
}
```

- [ ] **Step 5: Implement Watch**

Create `internal/paddocktui/session/watch.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EventType labels a watch event.
type EventType string

const (
	EventAdd    EventType = "Add"
	EventUpdate EventType = "Update"
	EventDelete EventType = "Delete"
)

// Event is a single session-watch update.
type Event struct {
	Type    EventType
	Session Session
}

// Watch polls List(ns) at the given interval and emits Add/Update/
// Delete events on the returned channel. The channel closes when ctx
// is done. We poll rather than use a controller-runtime informer so
// the client side stays small and dependency-light. interval=0 falls
// back to one second.
func Watch(ctx context.Context, c client.Client, ns string, interval time.Duration) (<-chan Event, error) {
	if interval <= 0 {
		interval = time.Second
	}
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		known := map[string]Session{}
		emit := func(t EventType, s Session) {
			select {
			case out <- Event{Type: t, Session: s}:
			case <-ctx.Done():
			}
		}
		tick := time.NewTimer(0)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			snap, err := List(ctx, c, ns)
			if err != nil {
				tick.Reset(interval)
				continue
			}
			seen := map[string]struct{}{}
			for _, s := range snap {
				seen[s.Name] = struct{}{}
				prev, had := known[s.Name]
				switch {
				case !had:
					emit(EventAdd, s)
				case prev.ResourceVersion != s.ResourceVersion:
					emit(EventUpdate, s)
				}
				known[s.Name] = s
			}
			for name, s := range known {
				if _, ok := seen[name]; !ok {
					emit(EventDelete, s)
					delete(known, name)
				}
			}
			tick.Reset(interval)
		}
	}()
	return out, nil
}
```

- [ ] **Step 6: Verify watch test passes**

Run: `go test ./internal/paddocktui/session/... -run TestWatch -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/paddocktui/session
git commit -m "feat(paddock-tui): session.End deletes labeled workspace; session.Watch polls and emits Add/Update/Delete"
```

---

### Task 7: Template listing (helper)

**Files:**
- Create: `internal/paddocktui/session/templates.go`
- Create: `internal/paddocktui/session/templates_test.go`

The new-session modal needs to enumerate available `HarnessTemplates` (and optionally `ClusterHarnessTemplates`) so the user can pick one. Read the description from `metadata.annotations[paddock.dev/description]`; if absent, fall back to the template's image.

- [ ] **Step 1: Test**

Create `internal/paddocktui/session/templates_test.go`:

```go
/*
Copyright 2026.
[std header omitted in plan; copy from earlier files]
*/

package session

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestListTemplates(t *testing.T) {
	withDesc := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "claude-code", Namespace: "default",
			Annotations: map[string]string{"paddock.dev/description": "Anthropic Claude Code"},
		},
		Spec: paddockv1alpha1.HarnessTemplateSpec{Image: "paddock-claude-code:dev"},
	}
	noDesc := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: "default"},
		Spec:       paddockv1alpha1.HarnessTemplateSpec{Image: "paddock-echo:dev"},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(withDesc, noDesc).Build()

	got, err := ListTemplates(context.Background(), cli, "default")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d; got=%v", len(got), got)
	}
	byName := map[string]TemplateInfo{}
	for _, ti := range got {
		byName[ti.Name] = ti
	}
	if byName["claude-code"].Description != "Anthropic Claude Code" {
		t.Errorf("description annotation not used: %+v", byName["claude-code"])
	}
	if byName["echo"].Description != "paddock-echo:dev" {
		t.Errorf("image fallback not applied: %+v", byName["echo"])
	}
}
```

- [ ] **Step 2: Implement**

Create `internal/paddocktui/session/templates.go`:

```go
/*
Copyright 2026.
[std header]
*/

package session

import (
	"context"
	"fmt"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// TemplateInfo is a flattened view of a HarnessTemplate suited for the
// new-session modal.
type TemplateInfo struct {
	Name        string
	Kind        string // "HarnessTemplate" or "ClusterHarnessTemplate"
	Description string
}

// DescriptionAnnotation lets template authors surface a one-line blurb
// in paddock-tui's picker. Optional; image is the fallback.
const DescriptionAnnotation = "paddock.dev/description"

// ListTemplates returns namespaced HarnessTemplates plus all
// ClusterHarnessTemplates. Sorted by Name. ClusterHarnessTemplate
// errors are tolerated (RBAC may forbid list at cluster scope).
func ListTemplates(ctx context.Context, c client.Client, ns string) ([]TemplateInfo, error) {
	out := []TemplateInfo{}

	var nsList paddockv1alpha1.HarnessTemplateList
	if err := c.List(ctx, &nsList, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("listing HarnessTemplates in %s: %w", ns, err)
	}
	for _, t := range nsList.Items {
		out = append(out, TemplateInfo{
			Name: t.Name, Kind: "HarnessTemplate",
			Description: descOrImage(t.Annotations, t.Spec.Image),
		})
	}

	var clusterList paddockv1alpha1.ClusterHarnessTemplateList
	if err := c.List(ctx, &clusterList); err == nil {
		for _, t := range clusterList.Items {
			out = append(out, TemplateInfo{
				Name: t.Name, Kind: "ClusterHarnessTemplate",
				Description: descOrImage(t.Annotations, t.Spec.Image),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func descOrImage(ann map[string]string, image string) string {
	if v := ann[DescriptionAnnotation]; v != "" {
		return v
	}
	return image
}
```

> Note for the implementer: if `paddockv1alpha1.HarnessTemplateSpec` doesn't expose `Image` directly (e.g. it's nested under a container spec), inspect `api/v1alpha1/harnesstemplate_types.go` and adapt the field path. The fallback string just needs to be a stable, useful identifier.

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/paddocktui/session/... -run TestListTemplates -v
git add internal/paddocktui/session
git commit -m "feat(paddock-tui): list HarnessTemplates with description fallback to image"
```

---

### Task 8: Runs — Create

**Files:**
- Create: `internal/paddocktui/runs/create.go`
- Create: `internal/paddocktui/runs/create_test.go`

- [ ] **Step 1: Test**

Create `internal/paddocktui/runs/create_test.go`:

```go
/*
Copyright 2026.
[std header]
*/

package runs

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(paddockv1alpha1.AddToScheme(s))
	return s
}

func TestCreate_PopulatesSpecAndPrefix(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	name, err := Create(context.Background(), cli, CreateOptions{
		Namespace:    "default",
		WorkspaceRef: "starlight-7",
		Template:     "claude-code",
		Prompt:       "summarize CHANGELOG",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(name, "starlight-7-") {
		t.Errorf("expected name to be prefixed with workspaceRef, got %q", name)
	}
	var hr paddockv1alpha1.HarnessRun
	if err := cli.Get(context.Background(), runtimeclientKey("default", name), &hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if hr.Spec.WorkspaceRef != "starlight-7" || hr.Spec.TemplateRef.Name != "claude-code" || hr.Spec.Prompt != "summarize CHANGELOG" {
		t.Errorf("spec wrong: %+v", hr.Spec)
	}
	if hr.Labels["paddock.dev/session"] != "" {
		// Labels on the run aren't part of MVP; only flagging if someone added one accidentally.
		t.Logf("note: HarnessRun was labeled with %s=%s", "paddock.dev/session", hr.Labels["paddock.dev/session"])
	}
	_ = metav1.Now() // imports keep
}

// runtimeclientKey is a tiny helper to keep tests readable.
func runtimeclientKey(ns, name string) interface{ String() string } {
	return struct{ Namespace, Name string }{ns, name}
}
```

> Note: `c.Get` wants `types.NamespacedName{Namespace: ns, Name: name}`. Replace the `runtimeclientKey` shim in the test with the real type before running:

```go
import "k8s.io/apimachinery/pkg/types"
// ...
if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &hr); err != nil {
```

- [ ] **Step 2: Implement**

Create `internal/paddocktui/runs/create.go`:

```go
/*
Copyright 2026.
[std header]
*/

// Package runs wraps HarnessRun create/watch/cancel operations from the
// paddock-tui's perspective. It is independent of internal/cli — the
// no-internal-import rule keeps paddock-tui easy to lift out.
package runs

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// CreateOptions parameterises Create.
type CreateOptions struct {
	Namespace    string
	WorkspaceRef string
	Template     string
	Prompt       string
}

// Create creates a HarnessRun against an existing Workspace. The
// HarnessRun's metadata.generateName uses the workspace name as a
// prefix so user-visible run names are easy to associate with the
// session (e.g. starlight-7-abcde).
//
// Returns the generated name.
func Create(ctx context.Context, c client.Client, opts CreateOptions) (string, error) {
	if opts.WorkspaceRef == "" || opts.Template == "" || opts.Prompt == "" {
		return "", fmt.Errorf("workspace, template, and prompt are all required")
	}
	hr := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    opts.Namespace,
			GenerateName: opts.WorkspaceRef + "-",
		},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef:  paddockv1alpha1.TemplateRef{Name: opts.Template},
			WorkspaceRef: opts.WorkspaceRef,
			Prompt:       opts.Prompt,
		},
	}
	if err := c.Create(ctx, hr); err != nil {
		return "", fmt.Errorf("creating HarnessRun: %w", err)
	}
	return hr.Name, nil
}
```

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/paddocktui/runs/... -run TestCreate -v
git add internal/paddocktui/runs
git commit -m "feat(paddock-tui): runs.Create submits HarnessRun against a session workspace"
```

---

### Task 9: Runs — Cancel and Watch

**Files:**
- Create: `internal/paddocktui/runs/cancel.go`
- Create: `internal/paddocktui/runs/cancel_test.go`
- Create: `internal/paddocktui/runs/watch.go`
- Create: `internal/paddocktui/runs/watch_test.go`

For `Cancel`, mirror what `kubectl paddock cancel` does — read `internal/cli/cancel.go` and **copy** the same patch/delete logic into our package. (Same pre-1.0 mechanism; we don't import internal/cli per the import rule.)

- [ ] **Step 1: Read `internal/cli/cancel.go`** to learn the cancellation mechanism.

```bash
sed -n '1,80p' internal/cli/cancel.go
```

Note in your head: does it patch a label/annotation, delete the run, or set `spec.cancelled`? Replicate the same pattern below.

- [ ] **Step 2: Cancel test**

Create `internal/paddocktui/runs/cancel_test.go`:

```go
/*
Copyright 2026.
[std header]
*/

package runs

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestCancel(t *testing.T) {
	hr := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "default"},
		Status:     paddockv1alpha1.HarnessRunStatus{Phase: paddockv1alpha1.HarnessRunPhaseRunning},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(hr).Build()
	if err := Cancel(context.Background(), cli, "default", "hr-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// Adapt these assertions to the kubectl-paddock convention you found in Step 1.
	// If cancellation is a label patch:
	var got paddockv1alpha1.HarnessRun
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "hr-1"}, &got); err != nil {
		// If cancellation deletes, assert NotFound here.
		t.Fatalf("get: %v", err)
	}
	// e.g. if got.Labels["paddock.dev/cancel"] != "true" { t.Error(...) }
}
```

- [ ] **Step 3: Implement Cancel**

Create `internal/paddocktui/runs/cancel.go` mirroring the kubectl-paddock pattern:

```go
/*
Copyright 2026.
[std header]
*/

package runs

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Cancel terminates an in-flight HarnessRun. Mirrors the cancel
// semantics already implemented in `kubectl paddock cancel` —
// duplicate that code here rather than importing internal/cli.
func Cancel(ctx context.Context, c client.Client, ns, name string) error {
	var hr paddockv1alpha1.HarnessRun
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &hr); err != nil {
		return fmt.Errorf("fetching run %s/%s: %w", ns, name, err)
	}
	// Apply the same mutation kubectl-paddock applies (label, annotation,
	// or delete). Then patch:
	//
	//   if err := c.Patch(ctx, &hr, client.MergeFrom(&original)); err != nil { ... }
	//
	// Adjust signature if the existing convention is to delete instead.
	return fmt.Errorf("paddocktui/runs.Cancel: implement using the convention from internal/cli/cancel.go")
}
```

> The implementer **must** open `internal/cli/cancel.go`, copy the actual mechanism, and replace the stub. The test in Step 2 will guide them — if cancellation is delete, the test should assert `NotFound`; if it's a label patch, assert the label.

- [ ] **Step 4: Watch test**

Create `internal/paddocktui/runs/watch_test.go`:

```go
/*
Copyright 2026.
[std header]
*/

package runs

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestWatch_FiltersByWorkspaceRef(t *testing.T) {
	mine := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "default"},
		Spec:       paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "starlight-7"},
	}
	other := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"},
		Spec:       paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "moonbeam-3"},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(mine, other).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := Watch(ctx, cli, "default", "starlight-7", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	got := map[string]bool{}
	for ev := range ch {
		got[ev.Run.Name] = true
		if len(got) >= 1 {
			cancel()
		}
	}
	if !got["mine"] {
		t.Errorf("expected to see 'mine'; got=%v", got)
	}
	if got["other"] {
		t.Errorf("did not expect to see 'other'; got=%v", got)
	}
}
```

- [ ] **Step 5: Implement Watch**

Create `internal/paddocktui/runs/watch.go`:

```go
/*
Copyright 2026.
[std header]
*/

package runs

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Event is a HarnessRun watch update.
type Event struct {
	Type string // "Add", "Update", "Delete"
	Run  paddockv1alpha1.HarnessRun
}

// Watch polls HarnessRuns in ns, emits Add/Update/Delete events for
// runs whose Spec.WorkspaceRef matches workspaceRef.
func Watch(ctx context.Context, c client.Client, ns, workspaceRef string, interval time.Duration) (<-chan Event, error) {
	if interval <= 0 {
		interval = time.Second
	}
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		known := map[string]string{} // name -> resourceVersion
		emit := func(t string, hr paddockv1alpha1.HarnessRun) {
			select {
			case out <- Event{Type: t, Run: hr}:
			case <-ctx.Done():
			}
		}
		tick := time.NewTimer(0)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			var list paddockv1alpha1.HarnessRunList
			if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
				tick.Reset(interval)
				continue
			}
			seen := map[string]struct{}{}
			for i := range list.Items {
				hr := list.Items[i]
				if hr.Spec.WorkspaceRef != workspaceRef {
					continue
				}
				seen[hr.Name] = struct{}{}
				prev, had := known[hr.Name]
				switch {
				case !had:
					emit("Add", hr)
				case prev != hr.ResourceVersion:
					emit("Update", hr)
				}
				known[hr.Name] = hr.ResourceVersion
			}
			for name := range known {
				if _, ok := seen[name]; !ok {
					emit("Delete", paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})
					delete(known, name)
				}
			}
			tick.Reset(interval)
		}
	}()
	return out, nil
}
```

> The Delete branch builds a stub `HarnessRun` with just name+namespace; that's enough for the TUI to remove the row. Add the missing `metav1` import.

- [ ] **Step 6: Verify and commit**

```bash
go test ./internal/paddocktui/runs/... -v
git add internal/paddocktui/runs
git commit -m "feat(paddock-tui): runs.Cancel + runs.Watch (workspace-filtered polling)"
```

---

### Task 10: Events — dedupe and tail

**Files:**
- Create: `internal/paddocktui/events/dedupe.go`
- Create: `internal/paddocktui/events/dedupe_test.go`
- Create: `internal/paddocktui/events/tail.go`
- Create: `internal/paddocktui/events/tail_test.go`

The dedup logic mirrors `internal/cli/events.go`. Don't import — duplicate. The tail polls `HarnessRun.status.recentEvents` and emits new events; ends when the run reaches a terminal phase.

- [ ] **Step 1: Dedupe test + impl + commit**

`dedupe_test.go`:

```go
package events

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestDedupe(t *testing.T) {
	d := NewDedupe()
	ev := paddockv1alpha1.PaddockEvent{
		SchemaVersion: "1",
		Timestamp:     metav1.NewTime(time.Now()),
		Type:          "Message",
		Summary:       "hello",
	}
	if !d.AddIfNew(ev) {
		t.Fatal("first AddIfNew should return true")
	}
	if d.AddIfNew(ev) {
		t.Fatal("second AddIfNew of the same event should return false")
	}
}
```

`dedupe.go`:

```go
package events

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Dedupe tracks seen events by content hash so the tail loop doesn't
// re-emit already-printed events when the recentEvents ring rotates
// or the HarnessRun is re-fetched.
type Dedupe struct {
	seen map[string]struct{}
}

func NewDedupe() *Dedupe { return &Dedupe{seen: map[string]struct{}{}} }

func (d *Dedupe) AddIfNew(ev paddockv1alpha1.PaddockEvent) bool {
	k := keyOf(ev)
	if _, ok := d.seen[k]; ok {
		return false
	}
	d.seen[k] = struct{}{}
	return true
}

func keyOf(ev paddockv1alpha1.PaddockEvent) string {
	h := sha256.New()
	_, _ = h.Write([]byte(ev.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z")))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(ev.Type))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(ev.Summary))
	keys := make([]string, 0, len(ev.Fields))
	for k := range ev.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = h.Write([]byte("|"))
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte("="))
		_, _ = h.Write([]byte(ev.Fields[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

- [ ] **Step 2: Tail test**

`tail_test.go`:

```go
package events

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestTail_EmitsAndTerminates(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(paddockv1alpha1.AddToScheme(scheme))
	hr := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "default"},
		Status: paddockv1alpha1.HarnessRunStatus{
			Phase: paddockv1alpha1.HarnessRunPhaseSucceeded,
			RecentEvents: []paddockv1alpha1.PaddockEvent{
				{SchemaVersion: "1", Timestamp: metav1.NewTime(time.Now()), Type: "Message", Summary: "first"},
				{SchemaVersion: "1", Timestamp: metav1.NewTime(time.Now()), Type: "Message", Summary: "second"},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hr).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := Tail(ctx, cli, "default", "hr-1", 25*time.Millisecond)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	got := []string{}
	for ev := range ch {
		got = append(got, ev.Summary)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("expected [first, second], got %v", got)
	}
}
```

- [ ] **Step 3: Tail impl**

`tail.go`:

```go
package events

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Tail polls HarnessRun.status.recentEvents, dedupes, and emits new
// events until ctx is cancelled or the run reaches a terminal phase.
func Tail(ctx context.Context, c client.Client, ns, runName string, interval time.Duration) (<-chan paddockv1alpha1.PaddockEvent, error) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	out := make(chan paddockv1alpha1.PaddockEvent, 64)
	go func() {
		defer close(out)
		dedupe := NewDedupe()
		tick := time.NewTimer(0)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			var hr paddockv1alpha1.HarnessRun
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: runName}, &hr); err != nil {
				// Surface the error to the caller via a synthetic event so the
				// TUI can show it; or just retry until ctx cancels. Retry is
				// the simpler choice — the TUI already handles disconnects.
				_ = fmt.Errorf("polling: %w", err)
				tick.Reset(interval)
				continue
			}
			for _, ev := range hr.Status.RecentEvents {
				if dedupe.AddIfNew(ev) {
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
			if isTerminal(hr.Status.Phase) {
				return
			}
			tick.Reset(interval)
		}
	}()
	return out, nil
}

func isTerminal(p paddockv1alpha1.HarnessRunPhase) bool {
	switch p {
	case paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed,
		paddockv1alpha1.HarnessRunPhaseCancelled:
		return true
	}
	return false
}
```

- [ ] **Step 4: Verify and commit**

```bash
go test ./internal/paddocktui/events/... -v
git add internal/paddocktui/events
git commit -m "feat(paddock-tui): events.Tail polls recentEvents with dedupe; terminates on terminal phase"
```

---

## Phase C — Non-TUI cobra subcommands

These are the scriptable entry points that work without launching the TUI. Useful for one-off operations and provide a smoke test that the helper packages compose correctly.

### Task 11: `paddock-tui session list`

**Files:**
- Create: `internal/paddocktui/cmd/session.go`
- Create: `internal/paddocktui/cmd/session_list.go`
- Create: `internal/paddocktui/cmd/session_list_test.go`
- Modify: `internal/paddocktui/cmd/root.go` (register the `session` group)

- [ ] **Step 1: Test**

Create `internal/paddocktui/cmd/session_list_test.go`:

```go
/*
Copyright 2026.
[std header]
*/

package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func TestSessionList_PrintsTable(t *testing.T) {
	now := metav1.NewTime(time.Now())
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha", Namespace: "default",
			Labels: map[string]string{pdksession.SessionLabel: pdksession.SessionLabelTrue},
			Annotations: map[string]string{
				pdksession.DefaultTemplateAnnotation: "claude-code",
			},
		},
		Status: paddockv1alpha1.WorkspaceStatus{LastActivity: &now, TotalRuns: 3},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).Build()
	var buf bytes.Buffer
	if err := runSessionList(context.Background(), cli, "default", &buf); err != nil {
		t.Fatalf("runSessionList: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"NAME", "alpha", "claude-code", "3"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: Implement session group + list**

Create `internal/paddocktui/cmd/session.go`:

```go
/*
Copyright 2026.
[std header]
*/

package cmd

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func newSessionCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	c := &cobra.Command{
		Use:   "session",
		Short: "Manage paddock-tui sessions (labeled Workspaces)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	c.AddCommand(newSessionListCmd(cfg))
	c.AddCommand(newSessionNewCmd(cfg))
	c.AddCommand(newSessionEndCmd(cfg))
	return c
}
```

Create `internal/paddocktui/cmd/session_list.go`:

```go
/*
Copyright 2026.
[std header]
*/

package cmd

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func newSessionListCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List paddock-tui sessions in the current namespace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			return runSessionList(cmd.Context(), c, ns, cmd.OutOrStdout())
		},
	}
}

func runSessionList(ctx context.Context, c client.Client, ns string, out io.Writer) error {
	sessions, err := pdksession.List(ctx, c, ns)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTEMPLATE\tACTIVE-RUN\tRUNS\tLAST-ACTIVITY\tAGE")
	for _, s := range sessions {
		last := "-"
		if !s.LastActivity.IsZero() {
			last = humanDuration(time.Since(s.LastActivity)) + " ago"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			s.Name, s.LastTemplate, dashIfEmpty(s.ActiveRunRef), s.TotalRuns, last,
			humanDuration(time.Since(s.CreationTime)),
		)
	}
	return tw.Flush()
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// humanDuration is a pared-down version of duration.HumanDuration:
// "5m", "2h13m", "3d", "47s". Avoids importing k8s.io/apimachinery's
// duration helper to keep the import surface minimal.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}
```

- [ ] **Step 3: Register on root**

Modify `internal/paddocktui/cmd/root.go` — add to `NewRootCmd` after `newVersionCmd`:

```go
	root.AddCommand(newSessionCmd(cfg))
```

- [ ] **Step 4: Verify and commit**

```bash
go test ./internal/paddocktui/cmd/... -run TestSessionList -v
make paddock-tui
./bin/paddock-tui session list   # against an empty cluster: prints just header row
git add internal/paddocktui/cmd
git commit -m "feat(paddock-tui): non-TUI 'session list' subcommand"
```

---

### Task 12: `paddock-tui session new --no-tui`

**Files:**
- Create: `internal/paddocktui/cmd/session_new.go`
- Create: `internal/paddocktui/cmd/session_new_test.go`

- [ ] **Step 1: Test**

```go
/*
Copyright 2026.
[std header]
*/

package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSessionNew_NoTUI(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	var buf bytes.Buffer
	err := runSessionNew(context.Background(), cli, sessionNewOpts{
		Namespace:   "default",
		Name:        "starlight-7",
		Template:    "claude-code",
		StorageSize: resource.MustParse("10Gi"),
		NoTUI:       true,
	}, &buf)
	if err != nil {
		t.Fatalf("runSessionNew: %v", err)
	}
	if !strings.Contains(buf.String(), "starlight-7") {
		t.Errorf("output missing session name:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Implement**

```go
/*
Copyright 2026.
[std header]
*/

package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

type sessionNewOpts struct {
	Namespace   string
	Name        string
	Template    string
	StorageSize resource.Quantity
	SeedRepo    string
	NoTUI       bool
}

func newSessionNewCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := sessionNewOpts{StorageSize: resource.MustParse("10Gi")}
	c := &cobra.Command{
		Use:   "new",
		Short: "Create a new paddock-tui session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			opts.Namespace = ns
			return runSessionNew(cmd.Context(), c, opts, cmd.OutOrStdout())
		},
	}
	c.Flags().StringVar(&opts.Name, "name", "", "Session name (required)")
	c.Flags().StringVar(&opts.Template, "template", "", "HarnessTemplate name (required)")
	c.Flags().StringVar(&opts.SeedRepo, "seed-repo", "", "Optional seed git repo URL")
	c.Flags().Var(&opts.StorageSize, "storage", "PVC size (default 10Gi)")
	c.Flags().BoolVar(&opts.NoTUI, "no-tui", false, "Don't launch the TUI after creation")
	_ = c.MarkFlagRequired("name")
	_ = c.MarkFlagRequired("template")
	return c
}

func runSessionNew(ctx context.Context, c client.Client, opts sessionNewOpts, out io.Writer) error {
	s, err := pdksession.Create(ctx, c, pdksession.CreateOptions{
		Namespace:   opts.Namespace,
		Name:        opts.Name,
		Template:    opts.Template,
		StorageSize: opts.StorageSize,
		SeedRepoURL: opts.SeedRepo,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "session %q created in %s (template=%s)\n", s.Name, s.Namespace, s.DefaultTemplate)
	if !opts.NoTUI {
		// Wired in Task 27. Until then, remind the user.
		fmt.Fprintln(out, "(launching TUI not yet wired; pass --no-tui to suppress this message)")
	}
	return nil
}
```

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/paddocktui/cmd/... -run TestSessionNew -v
make paddock-tui
./bin/paddock-tui session new --name test-1 --template claude-code --no-tui   # against a real cluster
git add internal/paddocktui/cmd
git commit -m "feat(paddock-tui): non-TUI 'session new' creates a labeled workspace"
```

---

### Task 13: `paddock-tui session end`

**Files:**
- Create: `internal/paddocktui/cmd/session_end.go`
- Create: `internal/paddocktui/cmd/session_end_test.go`

- [ ] **Step 1: Test (covers both --yes path and the not-a-session error)**

```go
package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func TestSessionEnd_DeletesWithYes(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "starlight-7", Namespace: "default",
			Labels: map[string]string{pdksession.SessionLabel: pdksession.SessionLabelTrue},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).Build()
	var buf bytes.Buffer
	if err := runSessionEnd(context.Background(), cli, "default", "starlight-7", true, &buf); err != nil {
		t.Fatalf("runSessionEnd: %v", err)
	}
	var got paddockv1alpha1.Workspace
	err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "starlight-7"}, &got)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound after end, got %v", err)
	}
	if !strings.Contains(buf.String(), "ended") {
		t.Errorf("expected confirmation message:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Implement**

```go
package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func newSessionEndCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "end NAME",
		Short: "Delete a paddock-tui session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			if !yes {
				if !confirm(os.Stdin, cmd.OutOrStdout(), fmt.Sprintf("End session %q in namespace %q? (y/N) ", args[0], ns)) {
					fmt.Fprintln(cmd.OutOrStdout(), "cancelled")
					return nil
				}
			}
			return runSessionEnd(cmd.Context(), c, ns, args[0], true, cmd.OutOrStdout())
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	return c
}

func runSessionEnd(ctx context.Context, c client.Client, ns, name string, _ bool, out io.Writer) error {
	if err := pdksession.End(ctx, c, ns, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "session %q in %s ended\n", name, ns)
	return nil
}

func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprint(out, prompt)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false
	}
	resp := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return resp == "y" || resp == "yes"
}
```

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/paddocktui/cmd/... -run TestSessionEnd -v
git add internal/paddocktui/cmd
git commit -m "feat(paddock-tui): non-TUI 'session end' with --yes confirmation skip"
```

---

## Phase D — TUI: state, messages, queue

The TUI follows the Elm-style Bubble Tea pattern: `Model` holds all state; `Init() tea.Cmd` kicks off async work; `Update(msg) (Model, tea.Cmd)` is the pure reducer; `View() string` renders the current state. The TUI tasks below build outward from a minimal compiling skeleton, layering features behind tests at each step.

### Task 14: TUI scaffolding (types, messages, model, queue)

**Files:**
- Create: `internal/paddocktui/app/types.go`
- Create: `internal/paddocktui/app/messages.go`
- Create: `internal/paddocktui/app/queue.go`
- Create: `internal/paddocktui/app/queue_test.go`
- Create: `internal/paddocktui/app/model.go`
- Create: `internal/paddocktui/app/model_test.go`

- [ ] **Step 1: Queue test**

`queue_test.go`:

```go
package app

import "testing"

func TestQueue(t *testing.T) {
	q := Queue{}
	q.Push("first")
	q.Push("second")
	if got := q.Peek(); got != "first" {
		t.Errorf("Peek=%q, want first", got)
	}
	if got, ok := q.Pop(); !ok || got != "first" {
		t.Errorf("Pop=%q,%v", got, ok)
	}
	if q.Len() != 1 {
		t.Errorf("Len after Pop=%d, want 1", q.Len())
	}
	q.RemoveAt(0)
	if q.Len() != 0 {
		t.Errorf("Len after RemoveAt=%d, want 0", q.Len())
	}
}
```

- [ ] **Step 2: Queue impl**

`queue.go`:

```go
/*
Copyright 2026.
[std header]
*/

package app

// Queue is a tiny FIFO of pending prompts for one session. Lives in
// TUI memory only — quitting the TUI loses queued prompts by design.
type Queue struct {
	items []string
}

func (q *Queue) Push(s string)       { q.items = append(q.items, s) }
func (q *Queue) Len() int            { return len(q.items) }
func (q *Queue) Peek() string        { if len(q.items) == 0 { return "" }; return q.items[0] }
func (q *Queue) Items() []string     { out := make([]string, len(q.items)); copy(out, q.items); return out }
func (q *Queue) RemoveAt(i int) {
	if i < 0 || i >= len(q.items) {
		return
	}
	q.items = append(q.items[:i], q.items[i+1:]...)
}
func (q *Queue) Pop() (string, bool) {
	if len(q.items) == 0 {
		return "", false
	}
	v := q.items[0]
	q.items = q.items[1:]
	return v, true
}
```

- [ ] **Step 3: Types**

`types.go`:

```go
/*
Copyright 2026.
[std header]
*/

// Package app holds the Bubble Tea Model, Update, View, and message
// types for the paddock-tui interactive UI.
package app

import (
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
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
```

- [ ] **Step 4: Messages**

`messages.go`:

```go
/*
Copyright 2026.
[std header]
*/

package app

import (
	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

// Bubble Tea messages produced by the async commands in commands.go.
// Each is a plain value type; Update branches on the concrete type.

type sessionsLoadedMsg struct{ Sessions []pdksession.Session }
type sessionAddedMsg struct{ Session pdksession.Session }
type sessionUpdatedMsg struct{ Session pdksession.Session }
type sessionDeletedMsg struct{ Name string }

type runUpdatedMsg struct {
	WorkspaceRef string
	Run          paddockv1alpha1.HarnessRun
}
type runDeletedMsg struct {
	WorkspaceRef string
	Name         string
}

type eventReceivedMsg struct {
	RunName string
	Event   paddockv1alpha1.PaddockEvent
}

type runCreatedMsg struct {
	WorkspaceRef string
	RunName      string
}

type runCancelledMsg struct{ Name string }

type errMsg struct{ Err error }

func (e errMsg) Error() string { return e.Err.Error() }
```

- [ ] **Step 5: Model**

`model.go`:

```go
/*
Copyright 2026.
[std header]
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
	ModalNew  *NewSessionModalState
	ModalEnd  *EndSessionModalState
	ModalHelp bool
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
// Wired in Task 18 (commands.go).
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadSessionsCmd(m.Client, m.Namespace), watchSessionsCmd(m.Client, m.Namespace))
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
```

- [ ] **Step 6: Model test (smoke)**

`model_test.go`:

```go
package app

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(paddockv1alpha1.AddToScheme(s))
	return s
}

func TestNewModel(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	m := NewModel(cli, "default")
	if m.Namespace != "default" || m.Sessions == nil {
		t.Errorf("model not initialised: %+v", m)
	}
}
```

- [ ] **Step 7: Verify and commit**

```bash
go test ./internal/paddocktui/app/... -v
git add internal/paddocktui/app
git commit -m "feat(paddock-tui): app scaffolding (types, messages, queue, model)"
```

> Note: `model.go` references `loadSessionsCmd` / `watchSessionsCmd` from commands.go (Task 18). Until that task lands, comment out the body of `Init()` (return `nil`) and uncomment in Task 18.

---

### Task 15: Sidebar state + filter

**Files:**
- Create: `internal/paddocktui/app/sidebar.go`
- Create: `internal/paddocktui/app/sidebar_test.go`

The sidebar logic is pure: take a Model + a key event, return a new Model. No async work. Tests are easy.

- [ ] **Step 1: Test**

```go
package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSidebar_MoveSelection(t *testing.T) {
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": {}, "bravo": {}, "charlie": {}},
		SessionOrder: []string{"alpha", "bravo", "charlie"},
		FocusArea:    FocusSidebar,
		Focused:      "alpha",
	}
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.Focused != "bravo" {
		t.Errorf("expected bravo focused, got %q", m.Focused)
	}
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyDown})
	m = handleSidebarKey(m, tea.KeyMsg{Type: tea.KeyDown}) // bounded — no wrap
	if m.Focused != "charlie" {
		t.Errorf("expected charlie focused, got %q", m.Focused)
	}
}

func TestSidebar_Filter(t *testing.T) {
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": {}, "bravo-2": {}, "bravo-3": {}, "charlie": {}},
		SessionOrder: []string{"alpha", "bravo-2", "bravo-3", "charlie"},
		Filter:       "bravo",
	}
	got := visibleSessions(m)
	if len(got) != 2 || got[0] != "bravo-2" || got[1] != "bravo-3" {
		t.Errorf("filter wrong: %v", got)
	}
}
```

- [ ] **Step 2: Implement**

```go
/*
Copyright 2026.
[std header]
*/

package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleSidebarKey processes a KeyMsg when FocusArea==FocusSidebar.
// Pure: returns the next Model without side effects.
func handleSidebarKey(m Model, key tea.KeyMsg) Model {
	switch key.Type {
	case tea.KeyUp, tea.KeyRunes:
		if key.Type == tea.KeyRunes && string(key.Runes) != "k" {
			break
		}
		m = moveSelection(m, -1)
	case tea.KeyDown:
		m = moveSelection(m, +1)
	}
	if key.Type == tea.KeyRunes && string(key.Runes) == "j" {
		m = moveSelection(m, +1)
	}
	return m
}

func moveSelection(m Model, delta int) Model {
	visible := visibleSessions(m)
	if len(visible) == 0 {
		return m
	}
	idx := 0
	for i, n := range visible {
		if n == m.Focused {
			idx = i
		}
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(visible) {
		idx = len(visible) - 1
	}
	m.Focused = visible[idx]
	return m
}

// visibleSessions returns the SessionOrder names filtered by m.Filter
// (substring match, case-insensitive).
func visibleSessions(m Model) []string {
	if m.Filter == "" {
		out := make([]string, len(m.SessionOrder))
		copy(out, m.SessionOrder)
		return out
	}
	needle := strings.ToLower(m.Filter)
	out := []string{}
	for _, n := range m.SessionOrder {
		if strings.Contains(strings.ToLower(n), needle) {
			out = append(out, n)
		}
	}
	return out
}
```

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/paddocktui/app/... -run TestSidebar -v
git add internal/paddocktui/app
git commit -m "feat(paddock-tui): sidebar selection and filter logic"
```

---

### Task 16: Slash command parser + prompt logic

**Files:**
- Create: `internal/paddocktui/app/slash.go`
- Create: `internal/paddocktui/app/slash_test.go`
- Create: `internal/paddocktui/app/prompt.go`
- Create: `internal/paddocktui/app/prompt_test.go`

- [ ] **Step 1: Slash test**

```go
package app

import "testing"

func TestParseSlash(t *testing.T) {
	cases := []struct {
		in       string
		wantCmd  SlashCmd
		wantArg  string
		wantOK   bool
	}{
		{":cancel", SlashCancel, "", true},
		{":queue", SlashQueue, "", true},
		{":template echo", SlashTemplate, "echo", true},
		{":interactive", SlashInteractive, "", true},
		{":help", SlashHelp, "", true},
		{":edit", SlashEdit, "", true},
		{":status", SlashStatus, "", true},
		{":bogus", SlashUnknown, "bogus", true},
		{"plain prompt", SlashNone, "plain prompt", false},
		{":", SlashNone, ":", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			cmd, arg, ok := ParseSlash(tc.in)
			if cmd != tc.wantCmd || arg != tc.wantArg || ok != tc.wantOK {
				t.Errorf("ParseSlash(%q) = (%v,%q,%v); want (%v,%q,%v)", tc.in, cmd, arg, ok, tc.wantCmd, tc.wantArg, tc.wantOK)
			}
		})
	}
}
```

- [ ] **Step 2: Slash impl**

```go
/*
Copyright 2026.
[std header]
*/

package app

import "strings"

// SlashCmd is a recognised slash command. SlashNone means the input
// was an ordinary prompt; SlashUnknown means the input was a `:`-
// prefixed token we don't recognise.
type SlashCmd int

const (
	SlashNone SlashCmd = iota
	SlashCancel
	SlashQueue
	SlashEdit
	SlashStatus
	SlashTemplate
	SlashInteractive
	SlashHelp
	SlashUnknown
)

// ParseSlash classifies an input line. Returns (cmd, arg, isSlash).
// When isSlash is false, the input is a regular prompt and the
// caller should treat arg as the prompt body.
func ParseSlash(input string) (SlashCmd, string, bool) {
	in := strings.TrimSpace(input)
	if !strings.HasPrefix(in, ":") || len(in) <= 1 {
		return SlashNone, input, false
	}
	rest := strings.TrimSpace(in[1:])
	parts := strings.SplitN(rest, " ", 2)
	head := parts[0]
	arg := ""
	if len(parts) == 2 {
		arg = strings.TrimSpace(parts[1])
	}
	switch head {
	case "cancel":
		return SlashCancel, "", true
	case "queue":
		return SlashQueue, "", true
	case "edit":
		return SlashEdit, "", true
	case "status":
		return SlashStatus, "", true
	case "template":
		return SlashTemplate, arg, true
	case "interactive":
		return SlashInteractive, "", true
	case "help":
		return SlashHelp, "", true
	default:
		return SlashUnknown, head, true
	}
}
```

- [ ] **Step 3: Prompt test**

```go
package app

import (
	"testing"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestPromptSubmit_QueuesWhenRunInFlight(t *testing.T) {
	state := &SessionState{
		Session: pdksessionMockedActive("alpha", "hr-1"),
		Runs:    []RunSummary{{Name: "hr-1", Phase: paddockv1alpha1.HarnessRunPhaseRunning}},
	}
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": state},
		SessionOrder: []string{"alpha"},
		Focused:      "alpha",
		PromptInput:  "second prompt",
	}
	m, submit := handlePromptSubmit(m)
	if submit != "" {
		t.Errorf("expected no immediate submit, got %q", submit)
	}
	if state.Queue.Len() != 1 || state.Queue.Peek() != "second prompt" {
		t.Errorf("prompt not queued: %v", state.Queue.Items())
	}
	if m.PromptInput != "" {
		t.Errorf("input not cleared after submit: %q", m.PromptInput)
	}
}

func TestPromptSubmit_FiresWhenIdle(t *testing.T) {
	state := &SessionState{
		Session: pdksessionMockedIdle("alpha"),
	}
	m := Model{
		Sessions:     map[string]*SessionState{"alpha": state},
		SessionOrder: []string{"alpha"},
		Focused:      "alpha",
		PromptInput:  "first prompt",
	}
	_, submit := handlePromptSubmit(m)
	if submit != "first prompt" {
		t.Errorf("expected immediate submit, got %q", submit)
	}
}

// pdksessionMockedActive / Idle helpers — define inline in the test file.
import_test_helpers_here_or_inline()
```

> Replace the placeholder import block with:

```go
import (
	"time"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func pdksessionMockedActive(name, runRef string) pdksession.Session {
	return pdksession.Session{Name: name, ActiveRunRef: runRef, Phase: paddockv1alpha1.WorkspacePhaseActive, LastActivity: time.Now()}
}
func pdksessionMockedIdle(name string) pdksession.Session {
	return pdksession.Session{Name: name, Phase: paddockv1alpha1.WorkspacePhaseActive, LastActivity: time.Now()}
}
```

- [ ] **Step 4: Prompt impl**

```go
/*
Copyright 2026.
[std header]
*/

package app

// handlePromptSubmit advances Model on Enter in the prompt input.
//
// Returns:
//   - the next Model with PromptInput cleared
//   - the prompt to submit IMMEDIATELY (empty string when queued)
//
// Slash commands are dispatched separately by handlePromptKey before
// reaching here.
func handlePromptSubmit(m Model) (Model, string) {
	if m.Focused == "" {
		return m, ""
	}
	state := m.Sessions[m.Focused]
	if state == nil {
		return m, ""
	}
	prompt := m.PromptInput
	m.PromptInput = ""
	if prompt == "" {
		return m, ""
	}
	if state.Session.ActiveRunRef != "" {
		state.Queue.Push(prompt)
		return m, ""
	}
	return m, prompt
}
```

- [ ] **Step 5: Verify and commit**

```bash
go test ./internal/paddocktui/app/... -run "TestParseSlash|TestPromptSubmit" -v
git add internal/paddocktui/app
git commit -m "feat(paddock-tui): slash command parser and prompt submit/queue logic"
```

---

### Task 17: Modals (new / end / help / queue)

**Files:**
- Create: `internal/paddocktui/app/modal_new.go`
- Create: `internal/paddocktui/app/modal_end.go`
- Create: `internal/paddocktui/app/modal_help.go`
- Create: `internal/paddocktui/app/modals_test.go`

The modals are state-only here; their View renders are in Phase E.

- [ ] **Step 1: Test (one for each modal)**

```go
package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewSessionModal_FieldNavigation(t *testing.T) {
	m := Model{Modal: ModalNew, ModalNew: &NewSessionModalState{Field: 0}}
	m = handleNewSessionModalKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ModalNew.Field != 1 {
		t.Errorf("expected Field=1 after Tab, got %d", m.ModalNew.Field)
	}
}

func TestEndSessionModal_RequiresExplicitConfirm(t *testing.T) {
	m := Model{Modal: ModalEnd, ModalEnd: &EndSessionModalState{TargetName: "alpha"}}
	m, confirmed := handleEndSessionModalKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !confirmed {
		t.Errorf("expected confirmation on Enter")
	}
	if m.Modal != ModalNone {
		t.Errorf("expected modal closed after confirm")
	}
}

func TestHelpModal_OpensAndCloses(t *testing.T) {
	m := Model{Modal: ModalNone}
	m = openHelpModal(m)
	if m.Modal != ModalHelp {
		t.Errorf("help did not open")
	}
	m = closeModal(m)
	if m.Modal != ModalNone {
		t.Errorf("modal not closed")
	}
}
```

- [ ] **Step 2: Implementations**

`modal_new.go`:

```go
/*
Copyright 2026.
[std header]
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
```

`modal_end.go`:

```go
/*
Copyright 2026.
[std header]
*/

package app

import tea "github.com/charmbracelet/bubbletea"

// handleEndSessionModalKey returns the next Model and whether the user
// confirmed the deletion (Enter).
func handleEndSessionModalKey(m Model, key tea.KeyMsg) (Model, bool) {
	if m.ModalEnd == nil {
		return m, false
	}
	switch key.Type {
	case tea.KeyEnter:
		m.ModalEnd.Confirmed = true
		next := closeModal(m)
		return next, true
	case tea.KeyEsc:
		return closeModal(m), false
	}
	return m, false
}

func openEndSessionModal(m Model, target string) Model {
	m.Modal = ModalEnd
	m.ModalEnd = &EndSessionModalState{TargetName: target}
	return m
}
```

`modal_help.go`:

```go
/*
Copyright 2026.
[std header]
*/

package app

func openHelpModal(m Model) Model {
	m.Modal = ModalHelp
	return m
}

func closeModal(m Model) Model {
	m.Modal = ModalNone
	m.ModalNew = nil
	m.ModalEnd = nil
	return m
}
```

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/paddocktui/app/... -run "TestNewSessionModal|TestEndSessionModal|TestHelpModal" -v
git add internal/paddocktui/app
git commit -m "feat(paddock-tui): modal state for new-session, end-session, help"
```

---

### Task 18: Bubble Tea command constructors

**Files:**
- Create: `internal/paddocktui/app/commands.go`

These wrap the helper packages (session, runs, events) into `tea.Cmd` values that produce messages on completion. Each command is small; tests live in Tasks 14-17 (which cover the Update reactions to these messages) and through the e2e build smoke-test.

- [ ] **Step 1: Implementation**

```go
/*
Copyright 2026.
[std header]
*/

package app

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pdkevents "github.com/tjorri/paddock/internal/paddocktui/events"
	pdkruns "github.com/tjorri/paddock/internal/paddocktui/runs"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

// loadSessionsCmd performs an initial List for the sidebar.
func loadSessionsCmd(c client.Client, ns string) tea.Cmd {
	return func() tea.Msg {
		ss, err := pdksession.List(context.Background(), c, ns)
		if err != nil {
			return errMsg{Err: err}
		}
		return sessionsLoadedMsg{Sessions: ss}
	}
}

// watchSessionsCmd polls List on a goroutine and returns Bubble Tea
// messages on each change. The cmd never returns nil — it always
// produces one message (the next event) so Update can re-issue it.
func watchSessionsCmd(c client.Client, ns string) tea.Cmd {
	// Bubble Tea pattern: wrap a long-running channel as a series of
	// tea.Cmd by returning a fresh Cmd from each message-handler.
	// Here we kick off the goroutine via session.Watch and bridge.
	ctx := context.Background()
	ch, err := pdksession.Watch(ctx, c, ns, 0)
	if err != nil {
		return func() tea.Msg { return errMsg{Err: err} }
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		switch ev.Type {
		case pdksession.EventAdd:
			return sessionAddedMsg{Session: ev.Session}
		case pdksession.EventUpdate:
			return sessionUpdatedMsg{Session: ev.Session}
		case pdksession.EventDelete:
			return sessionDeletedMsg{Name: ev.Session.Name}
		}
		return nil
	}
}

// watchRunsCmd watches HarnessRuns for one workspace.
func watchRunsCmd(c client.Client, ns, workspaceRef string) tea.Cmd {
	ctx := context.Background()
	ch, err := pdkruns.Watch(ctx, c, ns, workspaceRef, 0)
	if err != nil {
		return func() tea.Msg { return errMsg{Err: err} }
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		switch ev.Type {
		case "Add", "Update":
			return runUpdatedMsg{WorkspaceRef: workspaceRef, Run: ev.Run}
		case "Delete":
			return runDeletedMsg{WorkspaceRef: workspaceRef, Name: ev.Run.Name}
		}
		return nil
	}
}

// tailEventsCmd polls a HarnessRun's recentEvents.
func tailEventsCmd(c client.Client, ns, runName string) tea.Cmd {
	ctx := context.Background()
	ch, err := pdkevents.Tail(ctx, c, ns, runName, 0)
	if err != nil {
		return func() tea.Msg { return errMsg{Err: err} }
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventReceivedMsg{RunName: runName, Event: ev}
	}
}

// submitRunCmd creates a HarnessRun.
func submitRunCmd(c client.Client, ns, workspaceRef, template, prompt string) tea.Cmd {
	return func() tea.Msg {
		name, err := pdkruns.Create(context.Background(), c, pdkruns.CreateOptions{
			Namespace:    ns,
			WorkspaceRef: workspaceRef,
			Template:     template,
			Prompt:       prompt,
		})
		if err != nil {
			return errMsg{Err: err}
		}
		return runCreatedMsg{WorkspaceRef: workspaceRef, RunName: name}
	}
}

// cancelRunCmd cancels a HarnessRun.
func cancelRunCmd(c client.Client, ns, name string) tea.Cmd {
	return func() tea.Msg {
		if err := pdkruns.Cancel(context.Background(), c, ns, name); err != nil {
			return errMsg{Err: err}
		}
		return runCancelledMsg{Name: name}
	}
}

// createSessionCmd wraps session.Create for the new-session modal.
func createSessionCmd(c client.Client, ns, name, template string, storage resource.Quantity, seedRepo string) tea.Cmd {
	return func() tea.Msg {
		s, err := pdksession.Create(context.Background(), c, pdksession.CreateOptions{
			Namespace: ns, Name: name, Template: template, StorageSize: storage, SeedRepoURL: seedRepo,
		})
		if err != nil {
			return errMsg{Err: err}
		}
		return sessionAddedMsg{Session: s}
	}
}

// endSessionCmd wraps session.End for the end-session modal.
func endSessionCmd(c client.Client, ns, name string) tea.Cmd {
	return func() tea.Msg {
		if err := pdksession.End(context.Background(), c, ns, name); err != nil {
			return errMsg{Err: err}
		}
		return sessionDeletedMsg{Name: name}
	}
}

// patchLastTemplateCmd updates the LastTemplateAnnotation on a session
// Workspace. Used by the :template slash command so the override
// persists across reattach.
func patchLastTemplateCmd(c client.Client, ns, name, template string) tea.Cmd {
	return func() tea.Msg {
		var ws paddockv1alpha1.Workspace
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &ws); err != nil {
			return errMsg{Err: err}
		}
		original := ws.DeepCopy()
		if ws.Annotations == nil {
			ws.Annotations = map[string]string{}
		}
		ws.Annotations[pdksession.LastTemplateAnnotation] = template
		if err := c.Patch(context.Background(), &ws, client.MergeFrom(original)); err != nil {
			return errMsg{Err: err}
		}
		return nil
	}
}
```

> Add the `paddockv1alpha1` and `k8s.io/apimachinery/pkg/types` imports to commands.go for the patch helper.

> **Important pattern**: a `watchSessionsCmd` / `watchRunsCmd` / `tailEventsCmd` returns ONE message and stops. The Update handler must re-issue the same `Cmd` to keep the watch going. We'll wire that in Task 19.

- [ ] **Step 2: Verify build**

```bash
go build ./internal/paddocktui/...
```

Expected: clean compile.

- [ ] **Step 3: Commit**

```bash
git add internal/paddocktui/app/commands.go
git commit -m "feat(paddock-tui): tea.Cmd constructors wrapping session/runs/events helpers"
```

---

### Task 19: Update reducer (wire it all)

**Files:**
- Create: `internal/paddocktui/app/update.go`
- Create: `internal/paddocktui/app/update_test.go`
- Modify: `internal/paddocktui/app/model.go` (uncomment `Init` body)

The reducer is the biggest single file in the app package. It dispatches on message type, delegates to per-area handlers, and re-issues watch commands so the streams keep flowing.

- [ ] **Step 1: Test the message-routing skeleton**

```go
package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func TestUpdate_AddSession(t *testing.T) {
	m := newTestModel(t)
	next, _ := m.Update(sessionAddedMsg{Session: pdksession.Session{Name: "alpha"}})
	nm := next.(Model)
	if _, ok := nm.Sessions["alpha"]; !ok {
		t.Fatalf("session not added: %v", nm.Sessions)
	}
	if len(nm.SessionOrder) != 1 || nm.SessionOrder[0] != "alpha" {
		t.Errorf("session order wrong: %v", nm.SessionOrder)
	}
}

func TestUpdate_DeleteSession(t *testing.T) {
	m := newTestModel(t)
	m.Sessions["alpha"] = &SessionState{Session: pdksession.Session{Name: "alpha"}}
	m.SessionOrder = []string{"alpha"}
	m.Focused = "alpha"
	next, _ := m.Update(sessionDeletedMsg{Name: "alpha"})
	nm := next.(Model)
	if _, ok := nm.Sessions["alpha"]; ok {
		t.Errorf("session not removed")
	}
	if nm.Focused != "" {
		t.Errorf("focus should clear when focused session deleted")
	}
}

func TestUpdate_QuitOnQ(t *testing.T) {
	m := newTestModel(t)
	m.FocusArea = FocusSidebar
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd, got nil")
	}
	// We can't compare cmd to tea.Quit directly (it's a function); calling
	// the cmd should produce a tea.QuitMsg.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", msg)
	}
}

// newTestModel builds a Model wired to a fake client; reuses the
// helper from model_test.go.
func newTestModel(t *testing.T) Model { /* defined in model_test.go */ return Model{Sessions: map[string]*SessionState{}} }
```

> Where the test file references `newTestModel`, define one in `model_test.go` that returns a Model with a fake client. Or duplicate the body inline if `model_test.go` is in a different file in the same package — Go allows shared test helpers across files in the same package.

- [ ] **Step 2: Update implementation**

```go
/*
Copyright 2026.
[std header]
*/

package app

import (
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

// Update dispatches messages to per-area handlers and returns the next
// Model + a tea.Cmd. Watch commands re-issue themselves on every
// message they produce so streams stay alive.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case sessionsLoadedMsg:
		for _, s := range msg.Sessions {
			m = upsertSession(m, s)
		}
		// After initial load, focus the first session if any.
		if m.Focused == "" && len(m.SessionOrder) > 0 {
			m.Focused = m.SessionOrder[0]
		}
		return m, nil

	case sessionAddedMsg, sessionUpdatedMsg:
		var s pdksession.Session
		if a, ok := msg.(sessionAddedMsg); ok {
			s = a.Session
		} else {
			s = msg.(sessionUpdatedMsg).Session
		}
		m = upsertSession(m, s)
		// Re-issue the watch so we keep getting events.
		return m, watchSessionsCmd(m.Client, m.Namespace)

	case sessionDeletedMsg:
		delete(m.Sessions, msg.Name)
		m.SessionOrder = removeFromOrder(m.SessionOrder, msg.Name)
		if m.Focused == msg.Name {
			m.Focused = ""
		}
		return m, watchSessionsCmd(m.Client, m.Namespace)

	case runUpdatedMsg:
		m = upsertRun(m, msg)
		return m, watchRunsCmd(m.Client, m.Namespace, msg.WorkspaceRef)

	case runDeletedMsg:
		m = removeRun(m, msg)
		return m, watchRunsCmd(m.Client, m.Namespace, msg.WorkspaceRef)

	case eventReceivedMsg:
		m = appendEvent(m, msg)
		return m, tailEventsCmd(m.Client, m.Namespace, msg.RunName)

	case runCreatedMsg:
		// Start tailing events for this new run; the watch on workspace
		// runs will pick up the run object itself.
		return m, tailEventsCmd(m.Client, m.Namespace, msg.RunName)

	case runCancelledMsg, errMsg:
		if e, ok := msg.(errMsg); ok {
			m.ErrBanner = e.Err.Error()
		}
		return m, nil

	case tea.KeyMsg:
		return handleKeyMsg(m, msg)
	}
	return m, nil
}

func upsertSession(m Model, s pdksession.Session) Model {
	if _, exists := m.Sessions[s.Name]; !exists {
		m.SessionOrder = append(m.SessionOrder, s.Name)
		sort.Strings(m.SessionOrder) // simple lexical fallback; refined later by activity sort if desired
		m.Sessions[s.Name] = &SessionState{Session: s, Events: map[string][]paddockv1alpha1PaddockEvent{}}
	} else {
		m.Sessions[s.Name].Session = s
	}
	return m
}

// upsertRun, removeRun, appendEvent — straightforward map mutations.
// Implementer fills in.
```

> The `upsertSession` snippet contains a typo placeholder `paddockv1alpha1PaddockEvent` — replace with the real type alias once imports are added (`paddockv1alpha1.PaddockEvent`). Also implement `upsertRun`, `removeRun`, `appendEvent`, `removeFromOrder` as small map/slice helpers — each is 5-10 lines.

- [ ] **Step 3: handleKeyMsg dispatch**

In `update.go`, add:

```go
func handleKeyMsg(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Modal takes priority.
	if m.Modal != ModalNone {
		return handleModalKey(m, key)
	}
	switch m.FocusArea {
	case FocusSidebar:
		return handleSidebarFocusKey(m, key)
	case FocusPrompt:
		return handlePromptFocusKey(m, key)
	}
	return m, nil
}

func handleSidebarFocusKey(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Type == tea.KeyRunes && string(key.Runes) == "q":
		return m, tea.Quit
	case key.Type == tea.KeyRunes && string(key.Runes) == "n":
		// Open new-session modal; load templates inline (small list).
		return openNewSessionModal(m, []string{}), nil // template list populated by ModalOpen-side cmd
	case key.Type == tea.KeyRunes && string(key.Runes) == "e":
		return openEndSessionModal(m, m.Focused), nil
	case key.Type == tea.KeyTab:
		m.FocusArea = FocusPrompt
		return m, nil
	case key.Type == tea.KeyRunes && string(key.Runes) == "?":
		return openHelpModal(m), nil
	}
	return handleSidebarKey(m, key), nil
}

func handlePromptFocusKey(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.FocusArea = FocusSidebar
		return m, nil
	case tea.KeyTab:
		m.FocusArea = FocusSidebar
		return m, nil
	case tea.KeyEnter:
		// Slash command? Dispatch.
		cmd, arg, ok := ParseSlash(m.PromptInput)
		if ok {
			next, ext := dispatchSlash(m, cmd, arg)
			next.PromptInput = ""
			return next, ext
		}
		next, prompt := handlePromptSubmit(m)
		if prompt == "" {
			return next, nil
		}
		focused := m.Sessions[m.Focused]
		template := focused.Session.LastTemplate
		return next, submitRunCmd(m.Client, m.Namespace, m.Focused, template, prompt)
	case tea.KeyRunes:
		m.PromptInput += string(key.Runes)
		return m, nil
	case tea.KeyBackspace:
		if len(m.PromptInput) > 0 {
			m.PromptInput = m.PromptInput[:len(m.PromptInput)-1]
		}
		return m, nil
	}
	return m, nil
}

func handleModalKey(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Modal {
	case ModalNew:
		m = handleNewSessionModalKey(m, key)
		// On Enter on the last field, submit.
		if key.Type == tea.KeyEnter && m.ModalNew != nil && m.ModalNew.Field == 3 {
			storage, _ := parseQuantity(m.ModalNew.StorageInput) // helper, q.v.
			cmd := createSessionCmd(m.Client, m.Namespace,
				m.ModalNew.NameInput, m.ModalNew.TemplatePicks[m.ModalNew.TemplateIdx],
				storage, m.ModalNew.SeedRepoInput)
			m = closeModal(m)
			return m, cmd
		}
		return m, nil
	case ModalEnd:
		next, confirmed := handleEndSessionModalKey(m, key)
		if confirmed {
			cmd := endSessionCmd(m.Client, m.Namespace, m.ModalEnd.TargetName)
			return next, cmd
		}
		return next, nil
	case ModalHelp:
		if key.Type == tea.KeyEsc || (key.Type == tea.KeyRunes && string(key.Runes) == "?") {
			return closeModal(m), nil
		}
		return m, nil
	}
	return m, nil
}

func dispatchSlash(m Model, cmd SlashCmd, arg string) (Model, tea.Cmd) {
	switch cmd {
	case SlashCancel:
		focused := m.Sessions[m.Focused]
		if focused != nil && focused.Session.ActiveRunRef != "" {
			return m, cancelRunCmd(m.Client, m.Namespace, focused.Session.ActiveRunRef)
		}
	case SlashHelp:
		return openHelpModal(m), nil
	case SlashTemplate:
		if arg == "" {
			m.ErrBanner = ":template requires a template name"
			return m, nil
		}
		if focused := m.Sessions[m.Focused]; focused != nil {
			focused.Session.LastTemplate = arg
			// Persist via annotation patch so reattach restores the override.
			return m, patchLastTemplateCmd(m.Client, m.Namespace, m.Focused, arg)
		}
	case SlashInteractive:
		m.ErrBanner = "interactive mode is not yet implemented"
	}
	return m, nil
}

// parseQuantity, removeFromOrder, upsertRun, removeRun, appendEvent —
// small helpers, implementer fills in (each 5-15 lines).
```

- [ ] **Step 4: Re-enable Init**

In `model.go`, update `Init()`:

```go
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadSessionsCmd(m.Client, m.Namespace), watchSessionsCmd(m.Client, m.Namespace))
}
```

- [ ] **Step 5: Verify and commit**

```bash
go test ./internal/paddocktui/app/... -v
go build ./internal/paddocktui/...
git add internal/paddocktui/app
git commit -m "feat(paddock-tui): Update reducer wires session/run/event watches and key handlers"
```

---

## Phase E — TUI: rendering

The View functions are pure: `Model -> string`. Snapshot tests catch rendering regressions by comparing the View output to a golden file. Lipgloss handles styling.

### Task 20: Lipgloss styles + sidebar view

**Files:**
- Create: `internal/paddocktui/ui/styles.go`
- Create: `internal/paddocktui/ui/sidebar.go`
- Create: `internal/paddocktui/ui/sidebar_test.go`
- Create: `internal/paddocktui/ui/testdata/sidebar_basic.golden`

- [ ] **Step 1: Styles**

```go
/*
Copyright 2026.
[std header]
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

	StyleHeader     = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	StyleStatusBar  = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	StyleErrBanner  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true).Padding(0, 1)
	StyleRunHeader  = lipgloss.NewStyle().Bold(true)
	StyleRunFooter  = lipgloss.NewStyle().Faint(true)
	StylePromptArea = lipgloss.NewStyle().Padding(0, 1).Width(60)
	StyleModalFrame = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
)
```

- [ ] **Step 2: Sidebar view**

```go
/*
Copyright 2026.
[std header]
*/

package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/paddocktui/app"
)

// SidebarView renders the sidebar from the given model. Returns the
// styled string suitable for placement at the left of a JoinHorizontal.
func SidebarView(m app.Model) string {
	var rows []string
	rows = append(rows, StyleHeader.Render(fmt.Sprintf("Sessions (%d)", len(m.SessionOrder))))
	for _, name := range visibleNames(m) {
		s := m.Sessions[name]
		row := renderSidebarRow(name, s, name == m.Focused)
		rows = append(rows, row)
	}
	rows = append(rows, "  [+ new session]")
	rows = append(rows, StyleStatusBar.Render(fmt.Sprintf("Active: %d", countActive(m))))
	return StyleSidebarFrame.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

func renderSidebarRow(name string, s *app.SessionState, focused bool) string {
	glyph := " · "
	style := StyleSidebarRowNormal
	if s != nil && s.Session.ActiveRunRef != "" {
		glyph, style = " ▸ ", StyleSidebarRowRunning
	}
	if s != nil && lastRunFailed(s) {
		glyph, style = " ! ", StyleSidebarRowFailed
	}
	if focused {
		style = StyleSidebarRowFocused
	}
	return style.Render(fmt.Sprintf("%s%s", glyph, name))
}

func lastRunFailed(s *app.SessionState) bool {
	if len(s.Runs) == 0 {
		return false
	}
	return s.Runs[0].Phase == paddockv1alpha1.HarnessRunPhaseFailed
}

func countActive(m app.Model) int {
	n := 0
	for _, s := range m.Sessions {
		if s.Session.ActiveRunRef != "" {
			n++
		}
	}
	return n
}

func visibleNames(m app.Model) []string {
	if m.Filter == "" {
		out := make([]string, len(m.SessionOrder))
		copy(out, m.SessionOrder)
		return out
	}
	needle := strings.ToLower(m.Filter)
	out := []string{}
	for _, n := range m.SessionOrder {
		if strings.Contains(strings.ToLower(n), needle) {
			out = append(out, n)
		}
	}
	return out
}
```

- [ ] **Step 3: Snapshot test + golden**

```go
package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tjorri/paddock/internal/paddocktui/app"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func TestSidebarView_Basic(t *testing.T) {
	m := app.Model{
		Sessions: map[string]*app.SessionState{
			"alpha":   {Session: pdksession.Session{Name: "alpha", ActiveRunRef: "hr-1"}},
			"bravo":   {Session: pdksession.Session{Name: "bravo"}},
			"charlie": {Session: pdksession.Session{Name: "charlie"}},
		},
		SessionOrder: []string{"alpha", "bravo", "charlie"},
		Focused:      "alpha",
	}
	got := SidebarView(m)
	golden := filepath.Join("testdata", "sidebar_basic.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.WriteFile(golden, []byte(got), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("sidebar mismatch.\n--- got\n%s\n--- want\n%s", got, want)
	}
}
```

Then create the golden file by running:

```bash
mkdir -p internal/paddocktui/ui/testdata
UPDATE_GOLDEN=1 go test ./internal/paddocktui/ui/... -run TestSidebarView_Basic
```

Inspect `internal/paddocktui/ui/testdata/sidebar_basic.golden` to confirm it looks right (it should show all three sessions with appropriate glyphs and `alpha` highlighted), then re-run the test without `UPDATE_GOLDEN` and verify it passes.

- [ ] **Step 4: Commit**

```bash
git add internal/paddocktui/ui
git commit -m "feat(paddock-tui): lipgloss styles and sidebar view with snapshot test"
```

---

### Task 21: Main pane view (run timeline + prompt input)

**Files:**
- Create: `internal/paddocktui/ui/mainpane.go`
- Create: `internal/paddocktui/ui/mainpane_test.go`
- Create: `internal/paddocktui/ui/testdata/mainpane_*.golden`

- [ ] **Step 1: Implementation**

```go
/*
Copyright 2026.
[std header]
*/

package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/paddocktui/app"
)

// MainPaneView renders the focused session's run timeline plus the
// prompt input. When no session is focused, returns a placeholder.
func MainPaneView(m app.Model, width int) string {
	if m.Focused == "" {
		return StyleHeader.Render("(no session selected — pick one in the sidebar or press n to create)")
	}
	s := m.Sessions[m.Focused]
	if s == nil {
		return StyleHeader.Render("(session not loaded)")
	}
	var sections []string
	sections = append(sections, StyleHeader.Render(fmt.Sprintf("%s · %s", s.Session.Name, s.Session.LastTemplate)))
	for i := len(s.Runs) - 1; i >= 0; i-- {
		sections = append(sections, renderRun(s.Runs[i], s.Events[s.Runs[i].Name]))
	}
	prompt := renderPromptArea(m)
	sections = append(sections, prompt)
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func renderRun(r app.RunSummary, events []paddockv1alpha1.PaddockEvent) string {
	header := StyleRunHeader.Render(fmt.Sprintf("╭─ %s · %s ─%s", r.Name, r.StartTime.Format("15:04:05"), strings.Repeat("─", 8)))
	body := []string{}
	for _, ev := range events {
		body = append(body, "│ "+renderEvent(ev))
	}
	footer := StyleRunFooter.Render(fmt.Sprintf("╰─ %s · %s ", phaseLabel(r), durationLabel(r)))
	out := []string{header}
	if r.Prompt != "" {
		out = append(out, "│ > "+r.Prompt)
	}
	out = append(out, body...)
	out = append(out, footer)
	return strings.Join(out, "\n")
}

func renderEvent(ev paddockv1alpha1.PaddockEvent) string {
	switch ev.Type {
	case "ToolUse":
		return "• " + ev.Summary
	case "Message":
		return ev.Summary
	case "Error":
		return StyleSidebarRowFailed.Render("⚠ " + ev.Summary)
	default:
		return "  " + ev.Summary
	}
}

func renderPromptArea(m app.Model) string {
	cursor := ""
	if m.FocusArea == app.FocusPrompt {
		cursor = "_"
	}
	return StylePromptArea.Render(fmt.Sprintf("> %s%s", m.PromptInput, cursor))
}

func phaseLabel(r app.RunSummary) string { return string(r.Phase) }
func durationLabel(r app.RunSummary) string {
	if r.CompletionTime.IsZero() || r.StartTime.IsZero() {
		return "..."
	}
	return r.CompletionTime.Sub(r.StartTime).Truncate(1e9).String()
}
```

- [ ] **Step 2: Snapshot test**

```go
package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/paddocktui/app"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func TestMainPaneView_RunSucceeded(t *testing.T) {
	startTs := time.Date(2026, 4, 29, 14, 22, 11, 0, time.UTC)
	endTs := startTs.Add(47 * time.Second)
	m := app.Model{
		Sessions: map[string]*app.SessionState{
			"starlight-7": {
				Session: pdksession.Session{Name: "starlight-7", LastTemplate: "claude-code"},
				Runs: []app.RunSummary{{
					Name:           "hr-starlight-7-001",
					Phase:          paddockv1alpha1.HarnessRunPhaseSucceeded,
					Prompt:         "summarize CHANGELOG",
					StartTime:      startTs,
					CompletionTime: endTs,
				}},
				Events: map[string][]paddockv1alpha1.PaddockEvent{
					"hr-starlight-7-001": {
						{SchemaVersion: "1", Timestamp: metav1.NewTime(startTs.Add(time.Second)), Type: "ToolUse", Summary: "read CHANGELOG.md"},
						{SchemaVersion: "1", Timestamp: metav1.NewTime(startTs.Add(2 * time.Second)), Type: "Message", Summary: "Read 142 lines."},
					},
				},
			},
		},
		SessionOrder: []string{"starlight-7"},
		Focused:      "starlight-7",
		FocusArea:    app.FocusPrompt,
	}
	got := MainPaneView(m, 80)
	golden := filepath.Join("testdata", "mainpane_run_succeeded.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.WriteFile(golden, []byte(got), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("mainpane mismatch.\n--- got\n%s\n--- want\n%s", got, want)
	}
}
```

- [ ] **Step 3: Generate golden, verify, commit**

```bash
UPDATE_GOLDEN=1 go test ./internal/paddocktui/ui/... -run TestMainPaneView_RunSucceeded
go test ./internal/paddocktui/ui/... -run TestMainPaneView_RunSucceeded
git add internal/paddocktui/ui
git commit -m "feat(paddock-tui): main pane view (run timeline + prompt input) with snapshot"
```

---

### Task 22: Modal views and root view

**Files:**
- Create: `internal/paddocktui/ui/modal_new.go`
- Create: `internal/paddocktui/ui/modal_end.go`
- Create: `internal/paddocktui/ui/modal_help.go`
- Create: `internal/paddocktui/ui/view.go`
- Create: `internal/paddocktui/ui/view_test.go`
- Create: `internal/paddocktui/ui/testdata/view_*.golden`

- [ ] **Step 1: Modal views**

`modal_new.go`:

```go
package ui

import (
	"fmt"
	"strings"

	"github.com/tjorri/paddock/internal/paddocktui/app"
)

func NewSessionModalView(s *app.NewSessionModalState) string {
	if s == nil {
		return ""
	}
	field := func(label, value string, active bool) string {
		marker := "  "
		if active {
			marker = "▸ "
		}
		return fmt.Sprintf("%s%s: %s", marker, label, value)
	}
	tmpl := ""
	if len(s.TemplatePicks) > 0 {
		tmpl = s.TemplatePicks[s.TemplateIdx]
	}
	body := strings.Join([]string{
		field("name", s.NameInput, s.Field == 0),
		field("template", tmpl, s.Field == 1),
		field("storage", s.StorageInput, s.Field == 2),
		field("seed-repo", s.SeedRepoInput, s.Field == 3),
		"",
		"Tab/Shift-Tab: switch field · Enter on last field: submit · Esc: cancel",
	}, "\n")
	return StyleModalFrame.Render(body)
}
```

`modal_end.go`:

```go
package ui

import (
	"fmt"

	"github.com/tjorri/paddock/internal/paddocktui/app"
)

func EndSessionModalView(s *app.EndSessionModalState) string {
	if s == nil {
		return ""
	}
	return StyleModalFrame.Render(fmt.Sprintf("End session %q?\n\nEnter: confirm · Esc: cancel", s.TargetName))
}
```

`modal_help.go`:

```go
package ui

import "strings"

func HelpModalView() string {
	body := strings.Join([]string{
		"paddock-tui keybindings",
		"",
		"sidebar:",
		"  ↑↓ / jk    move selection",
		"  Enter      focus session",
		"  n          new session",
		"  e          end session",
		"  /          filter",
		"  q          quit",
		"",
		"prompt:",
		"  Enter      submit prompt (queues if a run is in flight)",
		"  Ctrl-J     newline (multi-line prompt)",
		"  Ctrl-E     open $EDITOR",
		"  Ctrl-X     cancel in-flight run",
		"  Ctrl-R     toggle raw events",
		"",
		"slash commands (in prompt):",
		"  :cancel     cancel in-flight run",
		"  :queue      show queued prompts",
		"  :template T set last-template",
		"  :status     compact session summary",
		"  :interactive (reserved — not yet implemented)",
		"  :help       this screen",
		"",
		"Esc / ?: close this help",
	}, "\n")
	return StyleModalFrame.Render(body)
}
```

- [ ] **Step 2: Root view**

`view.go`:

```go
/*
Copyright 2026.
[std header]
*/

package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/tjorri/paddock/internal/paddocktui/app"
)

// View renders the full TUI: sidebar | main pane, with footer status
// bar and an optional modal overlay.
func View(m app.Model, width, height int) string {
	sidebar := SidebarView(m)
	main := MainPaneView(m, width-30)
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
	footer := StyleStatusBar.Render(footerHints(m))
	if m.ErrBanner != "" {
		footer = StyleErrBanner.Render(m.ErrBanner) + "\n" + footer
	}
	composed := lipgloss.JoinVertical(lipgloss.Left, body, footer)
	switch m.Modal {
	case app.ModalNew:
		return overlay(composed, NewSessionModalView(m.ModalNew))
	case app.ModalEnd:
		return overlay(composed, EndSessionModalView(m.ModalEnd))
	case app.ModalHelp:
		return overlay(composed, HelpModalView())
	}
	return composed
}

func footerHints(m app.Model) string {
	switch m.FocusArea {
	case app.FocusSidebar:
		return "↑↓ select · Enter focus · n new · e end · / search · q quit · ? help"
	case app.FocusPrompt:
		return "Enter submit · Esc unfocus · :help · Ctrl-X cancel run"
	}
	return ""
}

// overlay places the modal in the centre of the composed view. Naive
// implementation: append after the body. A fancier overlay using
// lipgloss.Place can replace this later without changing callers.
func overlay(body, modal string) string {
	return body + "\n\n" + modal
}
```

- [ ] **Step 3: Root view snapshot test**

```go
package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tjorri/paddock/internal/paddocktui/app"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func TestView_EmptyState(t *testing.T) {
	m := app.Model{Sessions: map[string]*app.SessionState{}, FocusArea: app.FocusSidebar}
	got := View(m, 80, 24)
	checkGolden(t, got, "view_empty.golden")
}

func TestView_OneSessionFocused(t *testing.T) {
	m := app.Model{
		Sessions:     map[string]*app.SessionState{"alpha": {Session: pdksession.Session{Name: "alpha"}}},
		SessionOrder: []string{"alpha"},
		Focused:      "alpha",
		FocusArea:    app.FocusPrompt,
	}
	got := View(m, 80, 24)
	checkGolden(t, got, "view_one_session.golden")
}

func TestView_HelpModalOpen(t *testing.T) {
	m := app.Model{Sessions: map[string]*app.SessionState{}, FocusArea: app.FocusSidebar, Modal: app.ModalHelp}
	got := View(m, 80, 24)
	checkGolden(t, got, "view_help_modal.golden")
}

func checkGolden(t *testing.T, got, name string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.WriteFile(path, []byte(got), 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch.\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}
```

- [ ] **Step 4: Generate goldens, verify, commit**

```bash
UPDATE_GOLDEN=1 go test ./internal/paddocktui/ui/... -v
# inspect testdata/*.golden — make sure they look reasonable
go test ./internal/paddocktui/ui/... -v
git add internal/paddocktui/ui
git commit -m "feat(paddock-tui): modal views and root View with snapshot tests"
```

---

## Phase F — Wire-up and polish

### Task 23: TUI launcher and root command default

**Files:**
- Create: `internal/paddocktui/cmd/tui.go`
- Modify: `internal/paddocktui/cmd/root.go` (default RunE → launch TUI)

- [ ] **Step 1: Implementation**

Create `internal/paddocktui/cmd/tui.go`:

```go
/*
Copyright 2026.
[std header]
*/

package cmd

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	pdkapp "github.com/tjorri/paddock/internal/paddocktui/app"
	pdkui "github.com/tjorri/paddock/internal/paddocktui/ui"
)

// teaModel adapts pdkapp.Model to Bubble Tea's tea.Model by wiring the
// View method to ui.View. We do this here (in the cmd package, which
// imports both app and ui) to keep the strict separation: app/ doesn't
// know about ui/, ui/ doesn't know about Bubble Tea's tea.Model
// interface.
type teaModel struct {
	pdkapp.Model
	width, height int
}

func (t teaModel) Init() tea.Cmd { return t.Model.Init() }

func (t teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		t.width = ws.Width
		t.height = ws.Height
		return t, nil
	}
	next, cmd := t.Model.Update(msg)
	t.Model = next.(pdkapp.Model)
	return t, cmd
}

func (t teaModel) View() string {
	return pdkui.View(t.Model, t.width, t.height)
}

func newTUICmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:    "tui",
		Short:  "Launch the interactive TUI (default action when no subcommand)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cfg)
		},
	}
}

func runTUI(cfg *genericclioptions.ConfigFlags) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}
	tm := teaModel{Model: pdkapp.NewModel(c, ns)}
	prog := tea.NewProgram(tm, tea.WithAltScreen())
	final, err := prog.Run()
	if err != nil {
		return err
	}
	// Per spec §9: warn the user about queued prompts that were dropped
	// on quit. Bubble Tea exits alt-screen before returning, so stderr
	// writes here land in the regular terminal scrollback.
	if fm, ok := final.(teaModel); ok {
		dropped := []string{}
		for _, name := range fm.Model.SessionOrder {
			s := fm.Model.Sessions[name]
			if s == nil {
				continue
			}
			for _, p := range s.Queue.Items() {
				dropped = append(dropped, fmt.Sprintf("%s: %s", name, truncate(p, 60)))
			}
		}
		if len(dropped) > 0 {
			fmt.Fprintf(os.Stderr, "paddock-tui: %d queued prompt(s) dropped on quit:\n", len(dropped))
			for _, d := range dropped {
				fmt.Fprintf(os.Stderr, "  - %s\n", d)
			}
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

> Add the `fmt` and `os` imports.

- [ ] **Step 2: Make TUI the default action of root**

In `internal/paddocktui/cmd/root.go`, change the root `RunE` from `cmd.Help()` to `runTUI(cfg)`:

```go
RunE: func(cmd *cobra.Command, _ []string) error {
    return runTUI(cfg)
},
```

Add `root.AddCommand(newTUICmd(cfg))` near the version registration so `paddock-tui tui` works as an explicit alias.

- [ ] **Step 3: Verify the build**

```bash
make paddock-tui
./bin/paddock-tui --help    # shows session, version, tui
./bin/paddock-tui version
```

Expected: clean build, help shows the subcommand list, `version` still works.

- [ ] **Step 4: Manual smoke test**

Against a running cluster (kind dev or test cluster), run:

```bash
./bin/paddock-tui session new --name smoke-1 --template echo --no-tui
./bin/paddock-tui session list
./bin/paddock-tui          # launches the TUI; ↑↓ to move, Tab to focus prompt, type, Enter, watch events stream
./bin/paddock-tui session end smoke-1 --yes
```

Verify each command works against your cluster. The TUI should show smoke-1 in the sidebar, accept a prompt, create a HarnessRun, and stream events.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/cmd
git commit -m "feat(paddock-tui): wire Bubble Tea program; root cmd defaults to TUI"
```

---

### Task 24: Lint pass and spec/coding-rule compliance

**Files:**
- (no new files; this task is the discipline check)

- [ ] **Step 1: Verify the no-internal-imports rule**

```bash
go list -deps ./internal/paddocktui/... | grep github.com/tjorri/paddock/internal/ | grep -v paddocktui
```

Expected: empty output (or only the `api/v1alpha1` import reflected via paddocktui's transitive use). If anything from `internal/cli`, `internal/broker`, etc. shows up, fix the offending file by copying logic instead of importing.

- [ ] **Step 2: Run vet and golangci-lint**

```bash
go vet -tags=e2e ./internal/paddocktui/... ./cmd/paddock-tui/...
make lint   # runs golangci-lint
```

Expected: no warnings. Fix any that appear.

- [ ] **Step 3: Run all paddock-tui tests**

```bash
go test ./internal/paddocktui/... ./cmd/paddock-tui/... -v
```

Expected: all pass.

- [ ] **Step 4: Run the full test suite**

```bash
make test
```

Expected: all existing tests still pass, plus the new ones.

- [ ] **Step 5: Commit any fixes**

```bash
git add -u
git commit -m "chore(paddock-tui): lint pass — silence vet/golangci-lint findings"
```

(Skip this step if there were no fixes to make.)

---

### Task 25: Final smoke and polish

**Files:**
- Modify: `README.md` (optional — add a one-line mention of the new binary)

- [ ] **Step 1: Update README (optional)**

If `README.md` lists the project's binaries near the top, add `paddock-tui` to the list. Otherwise skip — user docs come in a follow-up.

- [ ] **Step 2: Final manual smoke**

Run the full happy-path flow against a real cluster:

1. `make paddock-tui`
2. `./bin/paddock-tui` — TUI launches in alt-screen mode.
3. Press `n` — new-session modal opens; tab through fields; submit.
4. Wait for the new session to appear in the sidebar; press `Enter` to focus it.
5. Press `Tab` to focus the prompt; type a prompt; press `Enter`.
6. Watch events stream into the main pane as the run progresses.
7. Press `e` on the sidebar to end the session.
8. Press `q` to quit.

If anything misbehaves, file an issue (or if it's a regression in this plan, fix in place and commit).

- [ ] **Step 3: Final commit**

```bash
# only if README.md was modified
git add README.md
git commit -m "docs(paddock-tui): mention paddock-tui in top-level README"
```

- [ ] **Step 4: Push the branch**

```bash
git push -u origin feat/paddock-tui
```

(Or follow your team's PR-creation workflow — `gh pr create` etc.)

---

## Self-review checklist (run AFTER all tasks complete)

Before opening the PR, verify the implementation matches the spec:

- [ ] Sessions are exclusively labeled Workspaces (`paddock.dev/session=true`); the binary creates no other CRD resources.
- [ ] `paddock.dev/session-default-template` and `paddock.dev/session-last-template` annotations are written by `session.Create` and the slash `:template` command.
- [ ] No file under `internal/paddocktui/` imports from `internal/cli/`, `internal/broker/`, `internal/controller/`, `internal/auditing/`, `internal/policy/`, `internal/proxy/`, `internal/webhook/`, or `internal/brokerclient/`.
- [ ] The TUI's prompt queue is in-process only; quitting the TUI prints a one-line warning naming any dropped prompts.
- [ ] `:interactive` is a stub that prints a "not yet implemented" notice and reserves the keybinding.
- [ ] No CRD, controller, broker, proxy, adapter, webhook, or chart change.
- [ ] Snapshot tests exist for at least: empty sidebar, sidebar with multiple sessions, main pane with a succeeded run, help modal.
- [ ] `make test` passes.
- [ ] `make lint` passes.
- [ ] Manual smoke test passed end-to-end.
- [ ] Each task's commits follow Conventional Commits with `paddock-tui` scope.

---

## Out of scope for this plan (deferred per spec §11–13)

These are explicitly **not** built by this plan and should remain unimplemented until a future spec/plan addresses them:

- e2e Ginkgo specs against a real Kind cluster.
- Bundling `paddock-tui` into the Helm chart.
- `--keep-pvc` flag on session end.
- Idle GC of stale sessions.
- `--all-namespaces` mode in the sidebar.
- Persistent `HarnessRun` mode (the `:interactive` slash command stays a stub).
- Conversation transcript continuity across runs.
- Per-session credential overrides at session-creation time.
- Releasing the binary on GitHub Releases (the build target is in place; the release pipeline change is a follow-up).
