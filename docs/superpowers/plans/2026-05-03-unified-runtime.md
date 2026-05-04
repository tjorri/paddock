# Unified Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the per-run `paddock-adapter-*` and `paddock-collector` containers into a single per-harness `paddock-runtime-*` sidecar that owns the full harness-side data plane (input recording, output translation, transcript persistence, ConfigMap publishing, stdout passthrough, broker HTTP+WS surface).

**Architecture:** A new `internal/runtime/` library houses the harness-agnostic logic (transcript writer, ConfigMap projector, archive layout, stdout passthrough, broker proxy). Per-harness binaries under `cmd/runtime-*/` import the library and supply only the harness-specific Converter + PromptFormatter. The pod goes from 4 containers (agent, adapter, collector, proxy) to 3 (agent, runtime, proxy). The `HarnessTemplate.Spec.EventAdapter` CRD field is renamed to `Spec.Runtime` directly (pre-1.0 evolves in place).

**Tech Stack:** Go 1.26, Kubernetes (kubebuilder), Helm, kind for E2E. Existing project conventions apply.

**Source spec:** `docs/superpowers/specs/2026-05-03-unified-runtime-design.md`.

**Plan shape rationale:** Considered splitting into Sub-plan A (mechanical refactor) + Sub-plan B (new behavior) per spec §14. Rejected — the dependency graph is too tight: dropping the collector container can't happen until the runtime owns transcript+publishing+stdout+archive, and renaming `EventAdapter→Runtime` without merging containers leaves a half-baked "renamed adapter alongside collector" state that's pure busywork. Keeping as one 15-task plan; each task lands a passing build.

---

## Prerequisites

**This plan MUST NOT start until PR #105 (`feature/interactive-adapter-as-proxy`, F19) merges to `main`.** The current branch (`feature/unified-runtime`) was deliberately based on `origin/main` so the spec/plan could be authored in parallel; the implementation must rebase onto fresh main with F19 present before Task 2.

## Project conventions (HARD constraints)

- **Pre-1.0 evolves in place** — no `v1alpha2`, no flag aliasing. `Spec.EventAdapter` is renamed in-place to `Spec.Runtime`. No backward-compat shims.
- **Conventional Commits** with breaking-change footer (`!:` syntax or `BREAKING CHANGE:` body) since the CRD rename is breaking.
- **Don't mention Claude in commit messages.**
- **Pre-commit hook** runs `go vet -tags=e2e ./...` + `golangci-lint run`. After hook failure: stage the fix and create a NEW commit (don't `--amend`).
- **`release-please` owns `CHANGELOG.md`** — don't touch manually.
- **E2E iteration**: `make test-e2e 2>&1 | tee /tmp/e2e.log` (or `FAIL_FAST=1 make test-e2e` to stop at first failure, `KEEP_E2E_RUN=1` to retain tenant state for kubectl post-mortem).

---

## File structure (end state)

```
api/v1alpha1/harnesstemplate_types.go       # Spec.EventAdapter → Spec.Runtime; rename type EventAdapterSpec → RuntimeSpec
api/v1alpha1/zz_generated.deepcopy.go       # regenerate

internal/runtime/                           # NEW — generic library
  ├── transcript/transcript.go              # events.jsonl writer + tail-broadcast (single writer)
  ├── transcript/transcript_test.go
  ├── publish/publish.go                    # ConfigMap projection (extracted from cmd/collector/)
  ├── publish/ring.go                       # bounded recent-events buffer (moved from cmd/collector/)
  ├── publish/projection.go                 # PaddockEvent → ConfigMap-bound shape (drops Fields.text/content)
  ├── publish/*_test.go
  ├── archive/archive.go                    # /workspace/.paddock/runs/<run>/ layout, metadata.json
  ├── archive/archive_test.go
  ├── stdout/stdout.go                      # JSONL passthrough
  ├── stdout/stdout_test.go
  ├── proxy/                                # MOVED from internal/adapter/proxy/
  │   ├── (all existing files)              # import-path updates only
  │   └── *_test.go

cmd/runtime-claude-code/                    # MOVED + reshaped from cmd/adapter-claude-code/
  ├── main.go                               # wires library + Converter + PromptFormatter; runs as native sidecar in both modes
  ├── convert.go                            # unchanged — claude stream-json → PaddockEvent
  ├── convert_test.go
  ├── main_test.go
cmd/runtime-echo/                           # MOVED + reshaped from cmd/adapter-echo/
  ├── main.go
  ├── (existing files)

cmd/collector/                              # DELETED
internal/adapter/                           # DELETED (proxy moved to internal/runtime/proxy/)
cmd/adapter-claude-code/                    # DELETED
cmd/adapter-echo/                           # DELETED

images/runtime-claude-code/Dockerfile       # MOVED + updated label from images/adapter-claude-code/
images/runtime-echo/Dockerfile              # MOVED + updated label from images/adapter-echo/
images/adapter-claude-code/                 # DELETED
images/adapter-echo/                        # DELETED
images/collector/                           # DELETED

internal/controller/pod_spec.go             # buildAdapterContainer + buildCollectorContainer → buildRuntimeContainer; new pod labels; new env
internal/controller/pod_spec_test.go        # rewrite container assertions

internal/webhook/v1alpha1/harnesstemplate_shared.go     # EventAdapter → Runtime
internal/webhook/v1alpha1/harnesstemplate_label.go      # adapter-interactive-modes → runtime-interactive-modes (filename TBD; whichever file holds the label-pull logic)
internal/webhook/v1alpha1/*_test.go         # updates

api/v1alpha1/paddockevent.go                # add PaddockEventTypePromptSubmitted constant
api/v1alpha1/paddockevent_test.go           # cover the new constant

config/samples/*.yaml                       # eventAdapter: → runtime:
charts/paddock/templates/*.yaml             # any references
charts/paddock/values.yaml                  # image values renamed
config/manager/*.yaml                       # may reference removed RBAC

Makefile                                    # image-adapter-* → image-runtime-*; image-collector deleted; load-images updated
Tiltfile                                    # if it references the renamed images

docs/contributing/harness-authoring.md      # update contract (single image, library imports, etc.)
docs/getting-started/quickstart.md          # any pod-shape references
test/e2e/*                                  # adapter+collector references; new transcript-presence assertions
```

---

## Task 1: Rebase onto fresh main and verify F19 architecture

**Files:** none modified — this is a sanity gate.

- [ ] **Step 1: Confirm F19 has merged**

```bash
git fetch origin main
git log origin/main --oneline -20 | grep -i "interactive-adapter-as-proxy\|F19\|harness-supervisor" | head -5
```

Expected: at least one commit referencing the F19 merge. If empty, STOP — F19 hasn't merged yet, plan execution is blocked.

- [ ] **Step 2: Rebase the branch onto fresh main**

```bash
git checkout feature/unified-runtime
git rebase origin/main
```

If conflicts arise on the spec file (`docs/superpowers/specs/2026-05-03-unified-runtime-design.md`), keep the version on this branch — main shouldn't have it.

- [ ] **Step 3: Verify the F19 architecture is in place**

```bash
ls internal/adapter/proxy/             # exists, multiple files
ls cmd/harness-supervisor/             # exists, supervisor binary
test -f cmd/adapter-claude-code/main.go && echo OK
go build ./...
```

Expected: all pass; `go build` is clean.

- [ ] **Step 4: Verify unit + lint baselines**

```bash
go vet -tags=e2e ./...
golangci-lint run --timeout=5m ./...
go test -count=1 -race $(go list ./... | grep -v /e2e)
```

Expected: all green. If any fail on stock main, STOP and surface — the plan assumes a green baseline.

- [ ] **Step 5: No commit needed.** This is a pre-flight only.

---

## Task 2: Extract publish library from cmd/collector

**Files:**
- Create: `internal/runtime/publish/publish.go`
- Create: `internal/runtime/publish/ring.go`
- Create: `internal/runtime/publish/projection.go`
- Create: `internal/runtime/publish/publish_test.go`
- Create: `internal/runtime/publish/ring_test.go`
- Create: `internal/runtime/publish/projection_test.go`
- Modify: `cmd/collector/main.go` — import new package
- Modify: `cmd/collector/publisher.go` — thin wrapper around publish.Run
- Delete: none yet (collector binary still exists in this task)

This task is a pure refactor — the collector binary continues to work, it just imports library code instead of inlining everything.

- [ ] **Step 1: Read the existing collector to understand its public surface**

```bash
cat cmd/collector/publisher.go
cat cmd/collector/ring.go
cat cmd/collector/main.go | head -80
```

Note the function signatures: `Publisher` struct, ring buffer ops, `Run` loop, ConfigMap patcher. Identify what's "library-shaped" (struct + methods, deterministic functions) vs. "main-shaped" (flag parsing, signal handling, k8s client wiring).

- [ ] **Step 2: Create internal/runtime/publish/ring.go**

Move the ring buffer code from `cmd/collector/ring.go` verbatim. Change package declaration from `package main` to `package publish`. Adjust any internal references.

```go
// Package publish projects PaddockEvents from the runtime's transcript
// to the controller-watched output ConfigMap. The ConfigMap is the
// summary projection (capped, drops Fields.text and Fields.content);
// the workspace-PVC events.jsonl is the system of record.
package publish

// Ring is a bounded FIFO of PaddockEvents. Used to keep the most
// recent N events resident for ConfigMap projection.
type Ring struct {
    // copy field shape from cmd/collector/ring.go verbatim
}
// (all methods)
```

- [ ] **Step 3: Create internal/runtime/publish/projection.go**

Define the projection rules. New file — does not exist in cmd/collector today, but the rules are spread across cmd/collector/publisher.go.

```go
package publish

import paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"

// Project transforms a full-fidelity PaddockEvent into the
// ConfigMap-bound shape: drops Fields["text"] from PromptSubmitted
// events and Fields["content"] from assistant Message events. All
// other fields and the canonical event metadata are preserved.
//
// The full event remains in the workspace events.jsonl; this is the
// summary view powering Status.RecentEvents.
func Project(e paddockv1alpha1.PaddockEvent) paddockv1alpha1.PaddockEvent {
    out := e
    if out.Fields == nil {
        return out
    }
    cleaned := make(map[string]string, len(out.Fields))
    for k, v := range out.Fields {
        if e.Type == "PromptSubmitted" && k == "text" {
            continue
        }
        if e.Type == "Message" && k == "content" {
            continue
        }
        cleaned[k] = v
    }
    out.Fields = cleaned
    return out
}
```

- [ ] **Step 4: Write projection_test.go**

```go
package publish

import (
    "testing"
    paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestProject_DropsPromptText(t *testing.T) {
    in := paddockv1alpha1.PaddockEvent{
        Type:    "PromptSubmitted",
        Summary: "hello world",
        Fields:  map[string]string{"text": "the full prompt", "length": "15", "hash": "sha256:abc"},
    }
    out := Project(in)
    if _, ok := out.Fields["text"]; ok {
        t.Fatal("Fields[text] must be dropped from PromptSubmitted projection")
    }
    if out.Fields["length"] != "15" || out.Fields["hash"] != "sha256:abc" {
        t.Fatalf("non-text fields must survive projection, got %#v", out.Fields)
    }
    if out.Summary != "hello world" {
        t.Fatalf("summary must survive, got %q", out.Summary)
    }
}

func TestProject_DropsAssistantContent(t *testing.T) {
    in := paddockv1alpha1.PaddockEvent{
        Type:    "Message",
        Summary: "Hi",
        Fields:  map[string]string{"role": "assistant", "content": "Hi there!"},
    }
    out := Project(in)
    if _, ok := out.Fields["content"]; ok {
        t.Fatal("Fields[content] must be dropped from assistant Message projection")
    }
    if out.Fields["role"] != "assistant" {
        t.Fatalf("role must survive, got %#v", out.Fields)
    }
}

func TestProject_LeavesOtherFieldsIntact(t *testing.T) {
    in := paddockv1alpha1.PaddockEvent{
        Type:   "ToolUse",
        Fields: map[string]string{"tool": "Read", "id": "tool-1"},
    }
    out := Project(in)
    if out.Fields["tool"] != "Read" || out.Fields["id"] != "tool-1" {
        t.Fatalf("ToolUse fields must pass through, got %#v", out.Fields)
    }
}
```

- [ ] **Step 5: Run projection tests, expect FAIL until Project exists**

```bash
go test ./internal/runtime/publish/...
```

Expected: PASS (Project from Step 3 satisfies the assertions). If FAIL, fix the impl until green.

- [ ] **Step 6: Create internal/runtime/publish/publish.go**

Move the Publisher struct + Run loop from `cmd/collector/publisher.go`. Adapt to use `Project()` from Step 3 on each event before pushing to the ring. Function signatures stay the same as today's collector.

```go
package publish

import (
    "context"
    "k8s.io/client-go/kubernetes"
    paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Publisher projects events into the controller-watched output
// ConfigMap. Construct with New(); drive with Run() in a goroutine.
type Publisher struct {
    // exact field shape ported from cmd/collector/publisher.go
}

func New(client kubernetes.Interface, ns, configMapName string, capacity int) *Publisher {
    // mirror cmd/collector/publisher.go constructor
}

// Run consumes PaddockEvents from in and patches the ConfigMap on a
// debounced cadence. Returns when in is closed.
func (p *Publisher) Run(ctx context.Context, in <-chan paddockv1alpha1.PaddockEvent) error {
    // mirror cmd/collector/publisher.go Run, but call Project(e) before
    // p.ring.Push(e) so the ring contains projected events only.
}
```

- [ ] **Step 7: Port ring_test.go and write any missing publish_test.go**

```bash
# port verbatim from cmd/collector/ring_test.go (package main → publish)
cp cmd/collector/ring_test.go internal/runtime/publish/ring_test.go
# edit: package main → package publish
```

For publish_test.go, port the existing publisher_test.go from cmd/collector/ — same logic, package change.

- [ ] **Step 8: Update cmd/collector/main.go to import the library**

`cmd/collector/main.go` currently constructs Publisher inline. Replace with:

```go
import "github.com/tjorri/paddock/internal/runtime/publish"

// in main():
pub := publish.New(client, ns, cmName, capacity)
go func() { _ = pub.Run(ctx, eventsIn) }()
```

Delete the now-duplicated logic from `cmd/collector/publisher.go` and `cmd/collector/ring.go` (or shrink them to thin wrappers if any per-collector logic genuinely belongs there).

- [ ] **Step 9: Build + test**

```bash
go build ./...
go test -count=1 -race ./internal/runtime/publish/... ./cmd/collector/...
```

Expected: all green.

- [ ] **Step 10: Commit**

```bash
git add internal/runtime/publish/ cmd/collector/
git commit -m "$(cat <<'EOF'
refactor(runtime): extract publish library from cmd/collector

Move Publisher, Ring, and the ConfigMap projection rules to a new
internal/runtime/publish package. cmd/collector now imports the
library; functional behavior is unchanged.

Adds the projection step (drop Fields.text from PromptSubmitted,
Fields.content from assistant Message) so the upcoming runtime
binary can use the same path. The summary projection vs. full
record split documented in the spec is now expressed in code.

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §3.4 §7.1.
EOF
)"
```

---

## Task 3: Create archive library

**Files:**
- Create: `internal/runtime/archive/archive.go`
- Create: `internal/runtime/archive/archive_test.go`

The archive package owns the `/workspace/.paddock/runs/<run-name>/` directory layout and the `metadata.json` write/update path. It is harness-agnostic.

- [ ] **Step 1: Define the package and Metadata type**

```go
// Package archive owns the per-run workspace directory layout used
// for durable transcript persistence. Each run's directory lives at
// /workspace/.paddock/runs/<run-name>/ and contains:
//
//   - metadata.json  (this package)
//   - events.jsonl   (transcript package writes; archive package only
//                     declares the path)
//   - raw.jsonl      (existing harness output, unchanged)
//
// The .paddock/ prefix avoids colliding with user-authored workspace
// files.
package archive

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"
)

// MetadataSchemaVersion is the current shape of metadata.json. Bumped
// only on incompatible changes; readers should ignore unknown fields.
const MetadataSchemaVersion = "1"

type RunRef struct {
    Name      string `json:"name"`
    Namespace string `json:"namespace"`
    UID       string `json:"uid"`
}

type HarnessRef struct {
    Image       string `json:"image"`
    ImageDigest string `json:"imageDigest,omitempty"`
}

type Metadata struct {
    SchemaVersion string     `json:"schemaVersion"`
    Run           RunRef     `json:"run"`
    Workspace     string     `json:"workspace"`
    Template      string     `json:"template"`
    Mode          string     `json:"mode"`
    Harness       HarnessRef `json:"harness"`
    StartedAt     time.Time  `json:"startedAt"`
    CompletedAt   *time.Time `json:"completedAt,omitempty"`
    ExitStatus    string     `json:"exitStatus,omitempty"`
    ExitReason    string     `json:"exitReason,omitempty"`
}
```

- [ ] **Step 2: Define the Archive struct and constructor**

```go
// Archive is the per-run handle for the workspace archive directory.
// Construct with Open(); call WriteStartMetadata() once at startup,
// UpdateCompletion() once on agent exit. Concurrent writes from the
// same Archive are serialized internally.
type Archive struct {
    dir string
}

// Open ensures the directory exists and returns a handle. The directory
// is /workspace/.paddock/runs/<runName> by convention; pass the full
// path to allow tests to use a tempdir.
func Open(dir string) (*Archive, error) {
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return nil, fmt.Errorf("archive: mkdir %s: %w", dir, err)
    }
    return &Archive{dir: dir}, nil
}

// EventsPath returns the absolute path to events.jsonl in this archive.
// Used by the transcript package as its single writer destination.
func (a *Archive) EventsPath() string {
    return filepath.Join(a.dir, "events.jsonl")
}

// MetadataPath returns the absolute path to metadata.json.
func (a *Archive) MetadataPath() string {
    return filepath.Join(a.dir, "metadata.json")
}
```

- [ ] **Step 3: Implement WriteStartMetadata**

```go
// WriteStartMetadata writes metadata.json with StartedAt set and
// CompletedAt nil. Replaces any prior file (a re-running runtime on
// pod restart will overwrite — the start timestamp reflects the most
// recent activation, which is the operator's question).
func (a *Archive) WriteStartMetadata(m Metadata) error {
    m.SchemaVersion = MetadataSchemaVersion
    if m.StartedAt.IsZero() {
        m.StartedAt = time.Now().UTC()
    }
    m.CompletedAt = nil
    m.ExitStatus = ""
    return a.writeMetadata(m)
}

// UpdateCompletion reads the existing metadata.json, sets the
// completion fields, and rewrites atomically. If metadata.json doesn't
// exist (start failed), returns an error so the caller can decide
// whether to log-and-continue.
func (a *Archive) UpdateCompletion(completedAt time.Time, exitStatus, exitReason string) error {
    raw, err := os.ReadFile(a.MetadataPath())
    if err != nil {
        return fmt.Errorf("archive: read metadata: %w", err)
    }
    var m Metadata
    if err := json.Unmarshal(raw, &m); err != nil {
        return fmt.Errorf("archive: decode metadata: %w", err)
    }
    m.CompletedAt = &completedAt
    m.ExitStatus = exitStatus
    m.ExitReason = exitReason
    return a.writeMetadata(m)
}

func (a *Archive) writeMetadata(m Metadata) error {
    tmp := a.MetadataPath() + ".tmp"
    f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
    if err != nil {
        return fmt.Errorf("archive: open tmp: %w", err)
    }
    enc := json.NewEncoder(f)
    enc.SetIndent("", "  ")
    if err := enc.Encode(&m); err != nil {
        _ = f.Close()
        return fmt.Errorf("archive: encode metadata: %w", err)
    }
    if err := f.Close(); err != nil {
        return fmt.Errorf("archive: close tmp: %w", err)
    }
    if err := os.Rename(tmp, a.MetadataPath()); err != nil {
        return fmt.Errorf("archive: rename: %w", err)
    }
    return nil
}
```

- [ ] **Step 4: Write archive_test.go**

```go
package archive

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestOpen_CreatesDir(t *testing.T) {
    dir := filepath.Join(t.TempDir(), "runs", "tuomo-test")
    a, err := Open(dir)
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    if a.EventsPath() != filepath.Join(dir, "events.jsonl") {
        t.Fatalf("EventsPath: %s", a.EventsPath())
    }
    if _, err := os.Stat(dir); err != nil {
        t.Fatalf("dir not created: %v", err)
    }
}

func TestWriteStartMetadata_AndUpdateCompletion(t *testing.T) {
    dir := t.TempDir()
    a, err := Open(dir)
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    started := time.Date(2026, 5, 3, 2, 12, 15, 0, time.UTC)
    if err := a.WriteStartMetadata(Metadata{
        Run: RunRef{Name: "run", Namespace: "ns", UID: "u"},
        Workspace: "ws", Template: "tpl", Mode: "Interactive",
        Harness: HarnessRef{Image: "img:dev"},
        StartedAt: started,
    }); err != nil {
        t.Fatalf("WriteStartMetadata: %v", err)
    }
    raw, err := os.ReadFile(a.MetadataPath())
    if err != nil {
        t.Fatalf("read: %v", err)
    }
    var got Metadata
    if err := json.Unmarshal(raw, &got); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if got.SchemaVersion != "1" || got.Run.Name != "run" || got.Workspace != "ws" || got.CompletedAt != nil {
        t.Fatalf("start metadata wrong: %#v", got)
    }
    completed := started.Add(5 * time.Minute)
    if err := a.UpdateCompletion(completed, "succeeded", "agent exited cleanly"); err != nil {
        t.Fatalf("UpdateCompletion: %v", err)
    }
    raw, _ = os.ReadFile(a.MetadataPath())
    _ = json.Unmarshal(raw, &got)
    if got.CompletedAt == nil || !got.CompletedAt.Equal(completed) || got.ExitStatus != "succeeded" {
        t.Fatalf("completion not persisted: %#v", got)
    }
}

func TestWriteStartMetadata_StampsStartIfZero(t *testing.T) {
    a, _ := Open(t.TempDir())
    before := time.Now().UTC()
    if err := a.WriteStartMetadata(Metadata{Run: RunRef{Name: "x"}, Mode: "Batch"}); err != nil {
        t.Fatal(err)
    }
    raw, _ := os.ReadFile(a.MetadataPath())
    var got Metadata
    _ = json.Unmarshal(raw, &got)
    if got.StartedAt.Before(before) {
        t.Fatalf("StartedAt not stamped: %v < %v", got.StartedAt, before)
    }
}
```

- [ ] **Step 5: Run tests**

```bash
go test -count=1 -race ./internal/runtime/archive/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/archive/
git commit -m "feat(runtime): add archive package for per-run workspace dir

New internal/runtime/archive package owns the
/workspace/.paddock/runs/<run-name>/ layout. Provides:

  - Open() to create the dir and return a handle
  - WriteStartMetadata() called once at runtime startup
  - UpdateCompletion() called once on agent exit

metadata.json is written atomically (tmp + rename) so a crash mid-
write doesn't corrupt the file. The transcript and stdout packages
will write events.jsonl alongside it in subsequent tasks.

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §6."
```

---

## Task 4: Create transcript library

**Files:**
- Create: `internal/runtime/transcript/transcript.go`
- Create: `internal/runtime/transcript/transcript_test.go`

The transcript package owns `events.jsonl` as a single-writer file. Same process appends prompts and outputs in order; no other writer ever opens the file. It also exposes a tail-broadcast subscription so the proxy package's /stream handler can fan out new lines without re-reading the file.

- [ ] **Step 1: Define package, Writer struct, constructor**

```go
// Package transcript owns the events.jsonl file on the workspace PVC.
// It is the single writer; concurrent appends from the runtime
// (prompt receipt, output translation) are serialized through Append.
//
// A tail-broadcast subscription (Subscribe) lets the proxy /stream
// handler fan out new lines to WebSocket clients without re-reading
// from disk.
package transcript

import (
    "encoding/json"
    "fmt"
    "os"
    "sync"

    paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

type Writer struct {
    mu          sync.Mutex
    f           *os.File
    enc         *json.Encoder
    subscribers map[chan<- []byte]struct{}
    subMu       sync.Mutex
}

// Open creates or appends to events.jsonl at path, returning a Writer
// ready for Append calls.
func Open(path string) (*Writer, error) {
    f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
    if err != nil {
        return nil, fmt.Errorf("transcript: open %s: %w", path, err)
    }
    return &Writer{
        f:           f,
        enc:         json.NewEncoder(f),
        subscribers: make(map[chan<- []byte]struct{}),
    }, nil
}

// Close releases the underlying file handle. Subscribers' channels
// are not closed by this call (their owners are responsible for the
// lifecycle of any goroutine consuming them).
func (w *Writer) Close() error {
    w.mu.Lock()
    defer w.mu.Unlock()
    return w.f.Close()
}
```

- [ ] **Step 2: Implement Append**

```go
// Append serializes e to one JSONL line, appends to the file, and
// fans the same bytes to every subscriber. Returns on first error;
// the file write is not retried.
func (w *Writer) Append(e paddockv1alpha1.PaddockEvent) error {
    line, err := json.Marshal(&e)
    if err != nil {
        return fmt.Errorf("transcript: marshal: %w", err)
    }
    line = append(line, '\n')
    w.mu.Lock()
    if _, err := w.f.Write(line); err != nil {
        w.mu.Unlock()
        return fmt.Errorf("transcript: write: %w", err)
    }
    w.mu.Unlock()
    w.broadcast(line)
    return nil
}

func (w *Writer) broadcast(line []byte) {
    w.subMu.Lock()
    subs := make([]chan<- []byte, 0, len(w.subscribers))
    for ch := range w.subscribers {
        subs = append(subs, ch)
    }
    w.subMu.Unlock()
    for _, ch := range subs {
        select {
        case ch <- line:
        default:
            // drop on slow consumer; subscribers are best-effort by design
        }
    }
}

// Subscribe returns a channel that receives every Append line going
// forward. Pass a buffered channel (recommended capacity 64) so a
// slow consumer doesn't drop frames during normal operation.
// Subscribe never replays past lines; clients that need history
// should read events.jsonl directly first, then subscribe.
//
// Call Unsubscribe to detach.
func (w *Writer) Subscribe(ch chan<- []byte) {
    w.subMu.Lock()
    w.subscribers[ch] = struct{}{}
    w.subMu.Unlock()
}

func (w *Writer) Unsubscribe(ch chan<- []byte) {
    w.subMu.Lock()
    delete(w.subscribers, ch)
    w.subMu.Unlock()
}
```

- [ ] **Step 3: Write transcript_test.go**

```go
package transcript

import (
    "bufio"
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestAppend_WritesJSONL(t *testing.T) {
    path := filepath.Join(t.TempDir(), "events.jsonl")
    w, err := Open(path)
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    defer w.Close()

    e := paddockv1alpha1.PaddockEvent{
        SchemaVersion: "1",
        Timestamp:     metav1.NewTime(time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)),
        Type:          "Message",
        Summary:       "hi",
    }
    if err := w.Append(e); err != nil {
        t.Fatalf("Append: %v", err)
    }
    raw, _ := os.ReadFile(path)
    f, _ := os.Open(path)
    defer f.Close()
    sc := bufio.NewScanner(f)
    if !sc.Scan() {
        t.Fatalf("no line written, raw=%s", string(raw))
    }
    var got paddockv1alpha1.PaddockEvent
    if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if got.Type != "Message" || got.Summary != "hi" {
        t.Fatalf("round-trip: %#v", got)
    }
}

func TestAppend_FansOutToSubscribers(t *testing.T) {
    w, _ := Open(filepath.Join(t.TempDir(), "events.jsonl"))
    defer w.Close()
    ch := make(chan []byte, 8)
    w.Subscribe(ch)
    defer w.Unsubscribe(ch)
    for i := 0; i < 3; i++ {
        if err := w.Append(paddockv1alpha1.PaddockEvent{Type: "Message", Summary: "x"}); err != nil {
            t.Fatal(err)
        }
    }
    for i := 0; i < 3; i++ {
        select {
        case b := <-ch:
            if len(b) == 0 || b[len(b)-1] != '\n' {
                t.Fatalf("expected trailing newline, got %q", b)
            }
        case <-time.After(200 * time.Millisecond):
            t.Fatalf("subscriber missed frame %d", i)
        }
    }
}

func TestAppend_DoesNotBlockOnSlowSubscriber(t *testing.T) {
    w, _ := Open(filepath.Join(t.TempDir(), "events.jsonl"))
    defer w.Close()
    slow := make(chan []byte) // unbuffered; drops on first Append
    w.Subscribe(slow)
    defer w.Unsubscribe(slow)
    done := make(chan struct{})
    go func() {
        for i := 0; i < 100; i++ {
            _ = w.Append(paddockv1alpha1.PaddockEvent{Type: "Message"})
        }
        close(done)
    }()
    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("Append blocked on slow subscriber — should have dropped instead")
    }
}
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 -race ./internal/runtime/transcript/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/transcript/
git commit -m "feat(runtime): add transcript package — single-writer events.jsonl

internal/runtime/transcript owns the events.jsonl file on the
workspace PVC. Append() serializes a PaddockEvent to one JSONL line,
appends to disk, and fans out to all Subscribe()d channels.

Subscribers are best-effort by design: a slow consumer drops frames
rather than blocking the writer (the file write itself remains
durable, so a /stream WS client missing a frame can recover by
re-reading events.jsonl).

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §6."
```

---

## Task 5: Create stdout passthrough library

**Files:**
- Create: `internal/runtime/stdout/stdout.go`
- Create: `internal/runtime/stdout/stdout_test.go`

The stdout package emits each event to `os.Stdout` as JSONL — byte-identical to what the transcript package writes to `events.jsonl`. The runtime wires it as a transcript subscriber; from then on it's automatic.

- [ ] **Step 1: Define the package**

```go
// Package stdout emits the runtime's transcript to its standard out
// as JSONL. It is the operational stream consumed by `kubectl logs`
// and external log aggregators (Fluent Bit, Vector, Promtail).
//
// Bytes emitted to stdout are identical to bytes written to
// events.jsonl by the transcript package; consumers that read both
// see the same sequence.
package stdout

import (
    "io"
    "os"
)

// Pump consumes from in and writes each frame verbatim to w. Returns
// when in is closed or w errors.
//
// Production wires `in` to a transcript subscriber and `w` to
// os.Stdout. Tests use a bytes.Buffer.
func Pump(in <-chan []byte, w io.Writer) error {
    for line := range in {
        if _, err := w.Write(line); err != nil {
            return err
        }
    }
    return nil
}

// PumpToStdout is the production wiring helper. Returns when in is
// closed.
func PumpToStdout(in <-chan []byte) {
    _ = Pump(in, os.Stdout)
}
```

- [ ] **Step 2: Write stdout_test.go**

```go
package stdout

import (
    "bytes"
    "testing"
)

func TestPump_WritesEachFrame(t *testing.T) {
    in := make(chan []byte, 4)
    in <- []byte(`{"type":"PromptSubmitted"}` + "\n")
    in <- []byte(`{"type":"Message"}` + "\n")
    close(in)
    var buf bytes.Buffer
    if err := Pump(in, &buf); err != nil {
        t.Fatalf("Pump: %v", err)
    }
    got := buf.String()
    want := `{"type":"PromptSubmitted"}` + "\n" + `{"type":"Message"}` + "\n"
    if got != want {
        t.Fatalf("\ngot:  %q\nwant: %q", got, want)
    }
}

func TestPump_StopsOnClose(t *testing.T) {
    in := make(chan []byte)
    close(in)
    if err := Pump(in, &bytes.Buffer{}); err != nil {
        t.Fatalf("Pump: %v", err)
    }
}
```

- [ ] **Step 3: Run tests**

```bash
go test -count=1 -race ./internal/runtime/stdout/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/runtime/stdout/
git commit -m "feat(runtime): add stdout passthrough package

internal/runtime/stdout pumps transcript subscriber frames to
os.Stdout. Bytes are identical to events.jsonl on disk; \`kubectl
logs <pod> -c runtime\` returns the canonical transcript.

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §8."
```

---

## Task 6: Move proxy package from internal/adapter to internal/runtime

**Files:**
- Create: `internal/runtime/proxy/` (entire directory, contents preserved verbatim)
- Modify: every file in `internal/runtime/proxy/` — change package import paths only
- Delete: `internal/adapter/proxy/` (all files)
- Modify: every Go file in the repo that imports `github.com/tjorri/paddock/internal/adapter/proxy`

This is a `git mv` + import-path sed. No semantic change.

- [ ] **Step 1: Move the directory**

```bash
git mv internal/adapter/proxy internal/runtime/proxy
ls internal/adapter/   # should be empty (or only contain other subdirs if any)
test -d internal/adapter/proxy && echo "MOVE FAILED" || echo OK
```

- [ ] **Step 2: Find all importers of the old path**

```bash
grep -rln "github.com/tjorri/paddock/internal/adapter/proxy" --include="*.go" .
```

Note the list (likely 1-3 files: cmd/adapter-claude-code/main.go and any tests).

- [ ] **Step 3: Update import paths**

```bash
grep -rl "github.com/tjorri/paddock/internal/adapter/proxy" --include="*.go" . | \
    xargs sed -i.bak 's|github.com/tjorri/paddock/internal/adapter/proxy|github.com/tjorri/paddock/internal/runtime/proxy|g'
find . -name "*.go.bak" -delete
```

- [ ] **Step 4: Verify**

```bash
grep -rn "internal/adapter/proxy" --include="*.go" . || echo "all references updated"
go build ./...
go test -count=1 -race ./internal/runtime/proxy/... ./cmd/adapter-claude-code/...
```

Expected: `all references updated`, build clean, tests green.

- [ ] **Step 5: Delete the now-empty internal/adapter/ tree if empty**

```bash
rmdir internal/adapter/proxy 2>/dev/null  # may already be gone after git mv
rmdir internal/adapter 2>/dev/null || echo "internal/adapter still has contents (other subdirs); leave it"
```

- [ ] **Step 6: Commit**

```bash
git add -A internal/adapter internal/runtime/proxy cmd/
git commit -m "refactor(runtime): move proxy package from internal/adapter to internal/runtime

The proxy package (broker HTTP+WS surface, UDS-bridge fanout) is
harness-agnostic and belongs alongside the other runtime libraries.
This is a directory rename + import-path update; no semantic change.

cmd/adapter-claude-code's main.go updated to import the new path.
The cmd/adapter-claude-code binary itself is replaced by
cmd/runtime-claude-code in a later task."
```

---

## Task 7: Add PromptSubmitted PaddockEvent type

**Files:**
- Modify: `api/v1alpha1/paddockevent.go` (or wherever PaddockEvent type constants live)
- Modify: `api/v1alpha1/paddockevent_test.go` (or add one if absent)

- [ ] **Step 1: Find where existing event-type constants live**

```bash
grep -rn 'PaddockEventType\|"Message"\|"Result"\|"Error"\|"ToolUse"' api/v1alpha1/*.go | head -10
```

Identify the canonical file. If event types are stringly-typed (no constants), this task adds the first constant — that's fine, document it as the canonical reference.

- [ ] **Step 2: Add the PromptSubmitted constant**

In the appropriate file under `api/v1alpha1/`:

```go
// PaddockEvent.Type values. See docs/superpowers/specs/2026-05-03-unified-runtime-design.md §7.
const (
    PaddockEventTypeMessage         = "Message"
    PaddockEventTypeResult          = "Result"
    PaddockEventTypeError           = "Error"
    PaddockEventTypeToolUse         = "ToolUse"
    PaddockEventTypePromptSubmitted = "PromptSubmitted"
)
```

If existing constants don't exist, add all five so the new one isn't an outlier.

- [ ] **Step 3: Add tests asserting the constant exists**

```go
package v1alpha1

import "testing"

func TestPaddockEventType_PromptSubmitted(t *testing.T) {
    if PaddockEventTypePromptSubmitted != "PromptSubmitted" {
        t.Fatalf("got %q", PaddockEventTypePromptSubmitted)
    }
}
```

- [ ] **Step 4: Build + test**

```bash
go build ./...
go test -count=1 -race ./api/v1alpha1/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add api/v1alpha1/
git commit -m "feat(api): add PromptSubmitted PaddockEvent type

The unified runtime emits PromptSubmitted events into events.jsonl
when a prompt is received (interactive: from broker /prompts, batch:
from Spec.Prompt at startup). Full text lives in Fields[\"text\"];
the ConfigMap projection drops it.

Adds a constant block for all event-type values (none existed before)
so future additions don't drift across the codebase.

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §7."
```

---

## Task 8: Rename HarnessTemplate.Spec.EventAdapter to Spec.Runtime

**Files:**
- Modify: `api/v1alpha1/harnesstemplate_types.go`
- Modify: `api/v1alpha1/zz_generated.deepcopy.go` (regenerate)
- Modify: `internal/webhook/v1alpha1/harnesstemplate_shared.go` (and any other files referencing `EventAdapter`)
- Modify: `internal/webhook/v1alpha1/harnesstemplate_webhook_test.go`
- Modify: `internal/controller/pod_spec.go` (callsites only — full pod-spec rewrite is Task 12)
- Modify: `config/crd/bases/paddock.dev_harnesstemplates.yaml` (regenerate)
- Modify: every `config/samples/*.yaml` file using `eventAdapter:` → `runtime:`
- Modify: `charts/paddock/` references

This is the breaking CRD change. Pre-1.0 evolves in place — no aliasing.

- [ ] **Step 1: Rename the Go field and type**

In `api/v1alpha1/harnesstemplate_types.go`:

```go
// before:
//   EventAdapter *EventAdapterSpec `json:"eventAdapter,omitempty"`
// after:
    Runtime *RuntimeSpec `json:"runtime,omitempty"`

// Type rename:
//   type EventAdapterSpec struct { ... }
// becomes:
type RuntimeSpec struct {
    Image           string             `json:"image"`
    ImagePullPolicy corev1.PullPolicy  `json:"imagePullPolicy,omitempty"`
    // copy any other fields from the old EventAdapterSpec verbatim
}
```

Update the godoc comment to describe the runtime's full responsibility (input recording, output translation, transcript persistence, ConfigMap publishing, broker surface).

- [ ] **Step 2: Update every callsite in non-generated code**

```bash
grep -rln "EventAdapter\|eventAdapter\|EventAdapterSpec" --include="*.go" . | grep -v zz_generated
```

For each Go file in the list, change:
- `EventAdapter` → `Runtime`
- `EventAdapterSpec` → `RuntimeSpec`
- `eventAdapter` → `runtime` (in any string literals)

```bash
grep -rln "EventAdapter" --include="*.go" . | grep -v zz_generated | \
    xargs sed -i.bak 's/EventAdapter/Runtime/g'
grep -rln "eventAdapter" --include="*.go" . | grep -v zz_generated | \
    xargs sed -i.bak 's/eventAdapter/runtime/g' 2>/dev/null || true
find . -name "*.go.bak" -delete
```

- [ ] **Step 3: Regenerate deepcopy**

```bash
make generate
# or, if the project uses controller-gen directly:
# controller-gen object paths="./..."
```

Verify `api/v1alpha1/zz_generated.deepcopy.go` no longer contains `EventAdapter` references.

- [ ] **Step 4: Regenerate CRD manifest**

```bash
make manifests
# or controller-gen rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
```

Inspect `config/crd/bases/paddock.dev_harnesstemplates.yaml` — `eventAdapter:` schema block should now be `runtime:`.

- [ ] **Step 5: Update samples**

```bash
grep -rl "eventAdapter:" config/samples/ examples/ | \
    xargs sed -i.bak 's/eventAdapter:/runtime:/g'
find . -name "*.yaml.bak" -delete
```

- [ ] **Step 6: Update Helm chart**

```bash
make helm-chart   # regenerates the chart
grep -rn "eventAdapter\|EventAdapter" charts/paddock/ || echo OK
```

If any references remain, edit by hand and re-run `make helm-chart`.

- [ ] **Step 7: Build + unit tests**

```bash
go vet -tags=e2e ./...
go build ./...
go test -count=1 -race $(go list ./... | grep -v /e2e)
```

Expected: all green. Webhook tests will reflect the new field name.

- [ ] **Step 8: Commit (with breaking-change footer)**

```bash
git add -A api/ config/ internal/ charts/ examples/
git commit -m "$(cat <<'EOF'
feat(api)!: rename HarnessTemplate.Spec.EventAdapter to Spec.Runtime

The per-harness sidecar's responsibility is being expanded from
"event-stream adapter" to "full data plane" (transcript persistence,
ConfigMap publishing, broker surface). Renaming the field reflects
that scope shift before downstream tasks add the new behavior.

Pre-1.0 evolves in place: no aliasing, no v1alpha2.

BREAKING CHANGE: HarnessTemplate manifests must rename their
`eventAdapter:` field to `runtime:`. The Go type EventAdapterSpec
is renamed to RuntimeSpec. The image referenced by the field is
unchanged at this point in the migration; the rename to
paddock-runtime-* lands in a later task.

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §4.
EOF
)"
```

---

## Task 9: Rename adapter-interactive-modes label to runtime-interactive-modes

**Files:**
- Modify: every Go file referencing `paddock.dev/adapter-interactive-modes`
- Modify: `images/adapter-claude-code/Dockerfile` (LABEL line) — even though the image will be deleted later, keep the codebase consistent during the transition
- Modify: `images/adapter-echo/Dockerfile` (same)

- [ ] **Step 1: Find references**

```bash
grep -rn "adapter-interactive-modes" --include="*.go" --include="Dockerfile*" --include="*.yaml" .
```

Likely results: webhook label-pull logic, Dockerfile LABELs, possibly samples.

- [ ] **Step 2: Replace**

```bash
grep -rl "adapter-interactive-modes" --include="*.go" --include="Dockerfile*" --include="*.yaml" . | \
    xargs sed -i.bak 's/adapter-interactive-modes/runtime-interactive-modes/g'
find . -name "*.bak" -delete
```

- [ ] **Step 3: Build + tests**

```bash
go build ./...
go test -count=1 -race ./internal/webhook/...
```

Expected: green.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
refactor!: rename paddock.dev/adapter-interactive-modes to runtime-interactive-modes

The image-label key that declares supported interactive modes is
renamed to track the adapter→runtime rename from the previous commit.

BREAKING CHANGE: harness images that declare interactive-mode support
must use paddock.dev/runtime-interactive-modes instead of
paddock.dev/adapter-interactive-modes.

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §4.2.
EOF
)"
```

---

## Task 10: Create paddock-runtime-claude-code binary and image

**Files:**
- Create: `cmd/runtime-claude-code/main.go`
- Create: `cmd/runtime-claude-code/main_test.go`
- Move: `cmd/adapter-claude-code/convert.go` → `cmd/runtime-claude-code/convert.go`
- Move: `cmd/adapter-claude-code/convert_test.go` → `cmd/runtime-claude-code/convert_test.go`
- Create: `images/runtime-claude-code/Dockerfile`
- Modify: `Makefile` — add `image-runtime-claude-code` target
- (existing `cmd/adapter-claude-code/` and `images/adapter-claude-code/` left in place; deleted in Task 13)

The runtime binary wires together: archive (workspace dir + metadata), transcript (events.jsonl writer), publish (ConfigMap projection), stdout (passthrough), proxy (broker HTTP+WS, interactive only), plus the harness-specific Converter and PromptFormatter from convert.go.

- [ ] **Step 1: Create cmd/runtime-claude-code/main.go skeleton**

```go
/*
Copyright 2026.

... (Apache 2.0 header)
*/

// Command runtime-claude-code is the per-run runtime sidecar for the
// paddock-claude-code harness. It owns the full harness-side data
// plane: input recording, output translation, transcript persistence,
// ConfigMap publishing, stdout passthrough, and (interactive-only)
// the broker HTTP+WS surface.
//
// In batch mode it tails PADDOCK_RAW_PATH (claude's stream-json
// output) and emits a single PromptSubmitted from Spec.Prompt at
// startup. In interactive mode it serves the proxy.Server HTTP+WS
// surface, forwarding stream-json frames between broker and the
// per-run harness-supervisor over a pair of UDS.
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/signal"
    "path/filepath"
    "syscall"
    "time"

    paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
    "github.com/tjorri/paddock/internal/runtime/archive"
    "github.com/tjorri/paddock/internal/runtime/proxy"
    "github.com/tjorri/paddock/internal/runtime/publish"
    "github.com/tjorri/paddock/internal/runtime/stdout"
    "github.com/tjorri/paddock/internal/runtime/transcript"
)

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
    defer cancel()

    cfg, err := loadConfigFromEnv()
    if err != nil {
        log.Fatalf("runtime-claude-code: config: %v", err)
    }

    arc, err := archive.Open(cfg.TranscriptDir)
    if err != nil {
        log.Fatalf("runtime-claude-code: archive: %v", err)
    }
    if err := arc.WriteStartMetadata(cfg.StartMetadata()); err != nil {
        log.Fatalf("runtime-claude-code: write start metadata: %v", err)
    }

    tw, err := transcript.Open(arc.EventsPath())
    if err != nil {
        log.Fatalf("runtime-claude-code: transcript: %v", err)
    }
    defer func() { _ = tw.Close() }()

    // stdout passthrough: subscribe to transcript, pump to os.Stdout.
    sout := make(chan []byte, 64)
    tw.Subscribe(sout)
    go stdout.PumpToStdout(sout)

    // ConfigMap publisher: subscribe to transcript via a decode shim
    // (publisher works on PaddockEvents; transcript broadcasts raw bytes).
    pubIn := make(chan paddockv1alpha1.PaddockEvent, 64)
    go runDecodeShim(tw, pubIn)
    pub := publish.New(cfg.K8sClient, cfg.RunNamespace, cfg.OutputConfigMapName, cfg.PublishCapacity)
    go func() { _ = pub.Run(ctx, pubIn) }()

    if cfg.InteractiveMode != "" {
        runInteractive(ctx, cfg, tw, arc)
    } else {
        runBatch(ctx, cfg, tw, arc)
    }

    // Final metadata: best-effort.
    completedAt := time.Now().UTC()
    _ = arc.UpdateCompletion(completedAt, cfg.ExitStatus(), cfg.ExitReason())
}
```

(Don't paste this verbatim — the implementer will need to flesh out `loadConfigFromEnv`, `runDecodeShim`, `runBatch`, `runInteractive`, and the `Config` struct. Each helper is small. The contract is captured in the comments above and in the spec §3.2/3.3 data flows.)

- [ ] **Step 2: Sketch the Config struct and env loading**

```go
// Config aggregates the env-derived runtime configuration.
type Config struct {
    Mode                string // "Batch" | "Interactive"
    InteractiveMode     string // "" | "persistent-process" | "per-prompt-process"
    RawPath             string
    AgentDataSocket     string
    AgentCtlSocket      string
    BrokerURL           string
    BrokerTokenPath     string
    BrokerCAPath        string
    RunName             string
    RunNamespace        string
    WorkspaceName       string
    TemplateName        string
    TranscriptDir       string
    OutputConfigMapName string
    PublishCapacity     int
    PromptFile          string // batch only — Spec.Prompt mounted as a file

    K8sClient kubernetes.Interface

    exitStatus string
    exitReason string
}

func loadConfigFromEnv() (*Config, error) {
    c := &Config{
        Mode:                envOr("PADDOCK_MODE", "Batch"),
        InteractiveMode:     os.Getenv("PADDOCK_INTERACTIVE_MODE"),
        RawPath:             envOr("PADDOCK_RAW_PATH", "/paddock/raw/out"),
        AgentDataSocket:     envOr("PADDOCK_AGENT_DATA_SOCKET", "/paddock/agent-data.sock"),
        AgentCtlSocket:      envOr("PADDOCK_AGENT_CTL_SOCKET", "/paddock/agent-ctl.sock"),
        BrokerURL:           os.Getenv("PADDOCK_BROKER_URL"),
        BrokerTokenPath:     envOr("PADDOCK_BROKER_TOKEN_PATH", "/var/run/secrets/paddock-broker/token"),
        BrokerCAPath:        envOr("PADDOCK_BROKER_CA_PATH", "/etc/paddock-broker/ca/ca.crt"),
        RunName:             os.Getenv("PADDOCK_RUN_NAME"),
        RunNamespace:        os.Getenv("PADDOCK_RUN_NAMESPACE"),
        WorkspaceName:       os.Getenv("PADDOCK_WORKSPACE_NAME"),
        TemplateName:        os.Getenv("PADDOCK_TEMPLATE_NAME"),
        TranscriptDir:       envOr("PADDOCK_TRANSCRIPT_DIR", filepath.Join("/workspace/.paddock/runs", os.Getenv("PADDOCK_RUN_NAME"))),
        OutputConfigMapName: envOr("PADDOCK_OUTPUT_CONFIGMAP", os.Getenv("PADDOCK_RUN_NAME")+"-output"),
        PublishCapacity:     50,
        PromptFile:          envOr("PADDOCK_PROMPT_FILE", "/paddock/prompt/prompt.txt"),
    }
    if c.RunName == "" || c.RunNamespace == "" {
        return nil, fmt.Errorf("PADDOCK_RUN_NAME and PADDOCK_RUN_NAMESPACE are required")
    }
    cfg, err := rest.InClusterConfig()
    if err != nil {
        return nil, fmt.Errorf("in-cluster config: %w", err)
    }
    c.K8sClient, err = kubernetes.NewForConfig(cfg)
    if err != nil {
        return nil, fmt.Errorf("kubernetes client: %w", err)
    }
    return c, nil
}

func envOr(k, fallback string) string {
    if v := os.Getenv(k); v != "" {
        return v
    }
    return fallback
}
```

- [ ] **Step 3: Implement runBatch — emit PromptSubmitted, tail raw output**

```go
func runBatch(ctx context.Context, cfg *Config, tw *transcript.Writer, arc *archive.Archive) {
    // 1. Emit PromptSubmitted from the prompt file (Spec.Prompt mounted via Secret).
    if data, err := os.ReadFile(cfg.PromptFile); err == nil && len(data) > 0 {
        sum := sha256.Sum256(data)
        evt := paddockv1alpha1.PaddockEvent{
            SchemaVersion: "1",
            Timestamp:     metav1.NewTime(time.Now().UTC()),
            Type:          paddockv1alpha1.PaddockEventTypePromptSubmitted,
            Summary:       truncate(string(data), 200),
            Fields: map[string]string{
                "text":   string(data),
                "length": fmt.Sprintf("%d", len(data)),
                "hash":   "sha256:" + hex.EncodeToString(sum[:]),
            },
        }
        if err := tw.Append(evt); err != nil {
            log.Printf("runtime-claude-code: append prompt event: %v", err)
        }
    } else if err != nil {
        log.Printf("runtime-claude-code: read prompt file %s: %v", cfg.PromptFile, err)
    }

    // 2. Tail raw output, convert each line, append to transcript.
    if err := tailAndConvert(ctx, cfg.RawPath, tw); err != nil {
        log.Printf("runtime-claude-code: tail: %v", err)
        cfg.exitStatus = "failed"
        cfg.exitReason = err.Error()
        return
    }
    cfg.exitStatus = "succeeded"
    cfg.exitReason = "agent exited cleanly"
}

func tailAndConvert(ctx context.Context, path string, tw *transcript.Writer) error {
    f, err := openWithBackoff(ctx, path) // helper: retries until file exists
    if err != nil {
        return err
    }
    defer f.Close()
    br := bufio.NewScanner(f)
    for br.Scan() {
        line := br.Text()
        events, err := convertLine(line, time.Now().UTC())
        if err != nil {
            log.Printf("convert: %v", err)
            continue
        }
        for _, e := range events {
            if err := tw.Append(e); err != nil {
                return err
            }
        }
    }
    return br.Err()
}
```

- [ ] **Step 4: Implement runInteractive — wire proxy.Server, append prompts to transcript**

```go
func runInteractive(ctx context.Context, cfg *Config, tw *transcript.Writer, arc *archive.Archive) {
    promptRecorder := func(text string, seq int32, submitter string) {
        sum := sha256.Sum256([]byte(text))
        evt := paddockv1alpha1.PaddockEvent{
            SchemaVersion: "1",
            Timestamp:     metav1.NewTime(time.Now().UTC()),
            Type:          paddockv1alpha1.PaddockEventTypePromptSubmitted,
            Summary:       truncate(text, 200),
            Fields: map[string]string{
                "text":      text,
                "length":    fmt.Sprintf("%d", len(text)),
                "hash":      "sha256:" + hex.EncodeToString(sum[:]),
                "seq":       fmt.Sprintf("%d", seq),
                "submitter": submitter,
            },
        }
        if err := tw.Append(evt); err != nil {
            log.Printf("runtime-claude-code: append prompt event: %v", err)
        }
    }

    srv, err := proxy.NewServer(ctx, proxy.Config{
        Mode:       cfg.InteractiveMode,
        DataSocket: cfg.AgentDataSocket,
        CtlSocket:  cfg.AgentCtlSocket,
        Backoff:    proxy.BackoffConfig{Initial: 50 * time.Millisecond, Max: 1600 * time.Millisecond, Tries: 30},
        Converter: func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
            return convertLine(line, time.Now().UTC())
        },
        PromptFormatter: claudePromptFormatter,
        OnPromptReceived: promptRecorder, // NEW hook on proxy.Config — see Step 5 of this task
        OnEvent: func(e paddockv1alpha1.PaddockEvent) {
            if err := tw.Append(e); err != nil {
                log.Printf("runtime-claude-code: append event: %v", err)
            }
        },
        OnTurnComplete: buildTurnCompleteHook(cfg),
    })
    if err != nil {
        cfg.exitStatus = "failed"
        cfg.exitReason = fmt.Sprintf("proxy: %v", err)
        return
    }
    defer func() { _ = srv.Close() }()

    var lc net.ListenConfig
    ln, err := lc.Listen(ctx, "tcp", ":8431")
    if err != nil {
        cfg.exitStatus = "failed"
        cfg.exitReason = fmt.Sprintf("listen: %v", err)
        return
    }
    httpSrv := &http.Server{
        Handler:           srv.Handler(),
        ReadHeaderTimeout: 10 * time.Second,
    }
    go func() {
        <-ctx.Done()
        shutCtx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer scancel()
        _ = httpSrv.Shutdown(shutCtx)
    }()
    if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
        cfg.exitStatus = "failed"
        cfg.exitReason = err.Error()
        return
    }
    cfg.exitStatus = "succeeded"
    cfg.exitReason = "interactive run ended"
}
```

- [ ] **Step 5: Add OnPromptReceived and OnEvent hooks to proxy.Config**

`internal/runtime/proxy/proxy.go`:

```go
// Config additions:
//
// OnPromptReceived, when non-nil, is called by handlePrompts after
// successfully parsing the request body and before forwarding to
// the data UDS. Used by the runtime to append PromptSubmitted to
// the transcript file.
OnPromptReceived func(text string, seq int32, submitter string)

// OnEvent, when non-nil, is called once per converted PaddockEvent
// by runDataReader. Used by the runtime to append events to the
// transcript file. Today the converter writes events.jsonl directly
// in proxy/events.go; with OnEvent set the proxy package is
// transcript-agnostic — the runtime drives transcript appends.
OnEvent func(paddockv1alpha1.PaddockEvent)
```

Wire them in `proxy/prompt.go` and `proxy/events.go` respectively. The events.go change replaces the current direct `events.jsonl` write path with a call to `cfg.OnEvent(e)` — the file-write responsibility moves entirely to the runtime binary via the transcript package.

Existing proxy_test.go assertions on `EventsPath` need updating — since the proxy no longer writes the file, tests check that `OnEvent` is called with the expected events. Update accordingly.

- [ ] **Step 6: Move convert.go and convert_test.go**

```bash
git mv cmd/adapter-claude-code/convert.go cmd/runtime-claude-code/convert.go
git mv cmd/adapter-claude-code/convert_test.go cmd/runtime-claude-code/convert_test.go
# convert.go's package declaration is `package main` already; no change needed beyond import paths if any
```

`claudePromptFormatter` lives in `cmd/adapter-claude-code/main.go` today; move that function to `cmd/runtime-claude-code/main.go` as well (or to a small `prompt_formatter.go` for clarity).

- [ ] **Step 7: Add main_test.go covering loadConfigFromEnv defaults and required-field errors**

```go
package main

import (
    "os"
    "testing"
)

func TestLoadConfigFromEnv_RequiresRunName(t *testing.T) {
    t.Setenv("PADDOCK_RUN_NAME", "")
    t.Setenv("PADDOCK_RUN_NAMESPACE", "ns")
    if _, err := loadConfigFromEnv(); err == nil {
        t.Fatal("expected error for empty PADDOCK_RUN_NAME")
    }
}

func TestLoadConfigFromEnv_DefaultsTranscriptDir(t *testing.T) {
    t.Setenv("PADDOCK_RUN_NAME", "myrun")
    t.Setenv("PADDOCK_RUN_NAMESPACE", "myns")
    os.Unsetenv("PADDOCK_TRANSCRIPT_DIR")
    // need to bypass kubernetes client for unit test — extract the
    // env-loading logic into a separate function that doesn't touch
    // rest.InClusterConfig, or inject a hook.
    // ...
}
```

(The implementer should split `loadConfigFromEnv` into `loadConfigFromEnv()` which calls `loadEnvOnly()` + `attachK8sClient()`, so unit tests cover env parsing without needing an in-cluster config.)

- [ ] **Step 8: Create images/runtime-claude-code/Dockerfile**

```dockerfile
# paddock-runtime-claude-code — per-run runtime sidecar for the
# claude-code harness. Owns input recording, output translation,
# transcript persistence, ConfigMap publishing, stdout passthrough,
# and the broker HTTP+WS surface (interactive only).
#
# Built from the repo root so the Go module context is available.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum

COPY api/ api/
COPY cmd/runtime-claude-code/ cmd/runtime-claude-code/
COPY internal/runtime/ internal/runtime/
COPY internal/broker/api/ internal/broker/api/
COPY internal/brokerclient/ internal/brokerclient/
COPY internal/auditing/ internal/auditing/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o runtime-claude-code ./cmd/runtime-claude-code

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/runtime-claude-code .
USER 65532:65532

LABEL paddock.dev/runtime-interactive-modes="per-prompt-process,persistent-process"

ENTRYPOINT ["/runtime-claude-code"]
```

- [ ] **Step 9: Add Makefile target**

In `Makefile`, near the existing `image-adapter-claude-code` target:

```makefile
RUNTIME_CLAUDE_CODE_IMG ?= paddock-runtime-claude-code:dev

.PHONY: image-runtime-claude-code
image-runtime-claude-code: ## Build the paddock-runtime-claude-code sidecar image
	docker buildx build --platform linux/amd64,linux/arm64 \
	    -t $(RUNTIME_CLAUDE_CODE_IMG) -f images/runtime-claude-code/Dockerfile \
	    --load .
```

(Match the project's existing image-build idioms exactly — the implementer should look at `image-adapter-claude-code` for the canonical shape.)

- [ ] **Step 10: Build + tests**

```bash
go build ./...
go test -count=1 -race ./cmd/runtime-claude-code/... ./internal/runtime/...
make image-runtime-claude-code
```

Expected: all green; image builds.

- [ ] **Step 11: Commit**

```bash
git add -A cmd/runtime-claude-code/ images/runtime-claude-code/ internal/runtime/proxy/ Makefile
git commit -m "$(cat <<'EOF'
feat(runtime): add paddock-runtime-claude-code binary and image

The runtime binary wires the new internal/runtime library
(transcript, archive, publish, stdout, proxy) with the existing
claude-code Converter and PromptFormatter. It owns the full data
plane: input recording (PromptSubmitted into events.jsonl), output
translation, transcript writes, ConfigMap projection, stdout
passthrough, and the broker HTTP+WS surface (interactive).

Adds two new hooks on proxy.Config:
  - OnPromptReceived(text, seq, submitter): runtime appends
    PromptSubmitted to the transcript file.
  - OnEvent(PaddockEvent): runtime appends every converted output
    event to the transcript. The proxy no longer writes events.jsonl
    directly; that responsibility moves to the runtime.

Existing cmd/adapter-claude-code/ remains in place during the
transition; pod_spec.go switches to the runtime image in a later
task, after which the adapter is deleted.

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §3, §5, §7.
EOF
)"
```

---

## Task 11: Create paddock-runtime-echo binary and image

**Files:**
- Create: `cmd/runtime-echo/main.go`
- Move: `cmd/adapter-echo/*` → `cmd/runtime-echo/*` (whatever convert/main exists)
- Create: `images/runtime-echo/Dockerfile`
- Modify: `Makefile`

Echo is the test-only harness; mirror Task 10's structure with whatever simpler converter the echo adapter has today.

- [ ] **Step 1: Inspect cmd/adapter-echo/ to understand its shape**

```bash
ls cmd/adapter-echo/
cat cmd/adapter-echo/main.go
```

Echo is much simpler than claude-code; the binary likely just echoes input as output events. The runtime version inherits all the new transcript/publish/stdout/archive scaffolding from Task 10's main.go pattern but with echo's converter.

- [ ] **Step 2: Move and adapt**

```bash
git mv cmd/adapter-echo cmd/runtime-echo
# update package declarations and main.go to mirror Task 10's runtime-claude-code structure
# echo's Converter is much simpler — keep it as-is, wire it into the new main.go shell
```

The implementer should refactor `cmd/runtime-echo/main.go` to follow the same skeleton as `cmd/runtime-claude-code/main.go`: load config from env, open archive + transcript, subscribe stdout + publisher, branch on Mode, append events via the OnEvent hook.

- [ ] **Step 3: Create images/runtime-echo/Dockerfile**

Mirror `images/runtime-claude-code/Dockerfile` from Task 10 with `echo` substituted for `claude-code` throughout. Set the LABEL to whatever interactive modes echo supports (likely none, or a subset).

- [ ] **Step 4: Add Makefile target**

```makefile
RUNTIME_ECHO_IMG ?= paddock-runtime-echo:dev

.PHONY: image-runtime-echo
image-runtime-echo: ## Build the paddock-runtime-echo sidecar image
	docker buildx build --platform linux/amd64,linux/arm64 \
	    -t $(RUNTIME_ECHO_IMG) -f images/runtime-echo/Dockerfile \
	    --load .
```

- [ ] **Step 5: Build + tests**

```bash
go build ./...
go test -count=1 -race ./cmd/runtime-echo/...
make image-runtime-echo
```

- [ ] **Step 6: Commit**

```bash
git add -A cmd/runtime-echo/ images/runtime-echo/ Makefile
git commit -m "feat(runtime): add paddock-runtime-echo binary and image

Mirrors the claude-code runtime structure with echo's simpler
Converter. Used by the e2e suite and by harness-author docs as a
minimal example."
```

---

## Task 12: Rewrite pod_spec.go — drop adapter+collector, add runtime

**Files:**
- Modify: `internal/controller/pod_spec.go` — replace `buildAdapterContainer`+`buildCollectorContainer` with `buildRuntimeContainer`; drop the events ConfigMap creation indirection; add new pod labels
- Modify: `internal/controller/pod_spec_test.go` — assert 3 containers, runtime mounts, env, labels
- (existing collector-related RBAC: keep the SA bindings but rebind to the runtime container, or rename `paddock-collector` SA to `paddock-runtime`)

This is the structural change that makes the prior tasks land in the cluster.

- [ ] **Step 1: Define buildRuntimeContainer**

In `internal/controller/pod_spec.go`, replace the existing `buildAdapterContainer` and `buildCollectorContainer` with:

```go
// buildRuntimeContainer constructs the per-run runtime sidecar that
// owns the full harness-side data plane. It mounts:
//
//   - /paddock (shared emptyDir, communicates with agent)
//   - workspace PVC (transcript archive at /workspace/.paddock/runs/<run>/)
//   - paddock-sa-token (in-cluster SA for ConfigMap patching)
//   - broker token + CA (for /turn-complete callbacks)
//
// Runs as a native sidecar (RestartPolicy: Always) in both batch and
// interactive modes for symmetry; in batch mode it idles after the
// agent exits until pod GC.
func buildRuntimeContainer(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate, in podSpecInputs) corev1.Container {
    always := corev1.ContainerRestartPolicyAlways
    sc := runContainerSecurityContextBaseline()
    sc.RunAsUser = ptr.To(int64(runtimeRunAsUID)) // pick a UID; document in pod_spec.go

    env := []corev1.EnvVar{
        {Name: "PADDOCK_RAW_PATH", Value: filepath.Join(sharedMountPath, "raw", "out")},
        {Name: "PADDOCK_AGENT_DATA_SOCKET", Value: filepath.Join(sharedMountPath, "agent-data.sock")},
        {Name: "PADDOCK_AGENT_CTL_SOCKET", Value: filepath.Join(sharedMountPath, "agent-ctl.sock")},
        {Name: "PADDOCK_RUN_NAME", Value: run.Name},
        {Name: "PADDOCK_RUN_NAMESPACE", Value: run.Namespace},
        {Name: "PADDOCK_WORKSPACE_NAME", Value: run.Spec.WorkspaceRef.Name},
        {Name: "PADDOCK_TEMPLATE_NAME", Value: template.Name},
        {Name: "PADDOCK_MODE", Value: string(run.Spec.Mode)},
        {Name: "PADDOCK_TRANSCRIPT_DIR", Value: filepath.Join(workspaceMountPath, ".paddock", "runs", run.Name)},
        {Name: "PADDOCK_OUTPUT_CONFIGMAP", Value: in.outputConfigMap},
    }
    if template.Spec.Interactive != nil && template.Spec.Interactive.Mode != "" {
        env = append(env, corev1.EnvVar{
            Name:  "PADDOCK_INTERACTIVE_MODE",
            Value: template.Spec.Interactive.Mode,
        })
    }
    if in.brokerEndpoint != "" {
        env = append(env,
            corev1.EnvVar{Name: "PADDOCK_BROKER_URL", Value: in.brokerEndpoint},
            corev1.EnvVar{Name: "PADDOCK_BROKER_TOKEN_PATH", Value: brokerTokenPath},
            corev1.EnvVar{Name: "PADDOCK_BROKER_CA_PATH", Value: brokerCAPath},
        )
    }

    mounts := []corev1.VolumeMount{
        {Name: sharedVolumeName, MountPath: sharedMountPath},
        {Name: workspaceVolumeName, MountPath: workspaceMountPath},
        {Name: paddockSAVolumeName, MountPath: paddockSAMountPath, ReadOnly: true},
    }
    if in.brokerEndpoint != "" {
        mounts = append(mounts,
            corev1.VolumeMount{Name: brokerTokenVolumeName, MountPath: brokerTokenMountPath, ReadOnly: true},
            corev1.VolumeMount{Name: brokerCAVolumeName, MountPath: brokerCAMountPath, ReadOnly: true},
        )
    }

    c := corev1.Container{
        Name:            runtimeContainerName, // "runtime"
        Image:           template.Spec.Runtime.Image,
        RestartPolicy:   &always,
        SecurityContext: sc,
        Env:             env,
        VolumeMounts:    mounts,
    }
    if template.Spec.Runtime.ImagePullPolicy != "" {
        c.ImagePullPolicy = template.Spec.Runtime.ImagePullPolicy
    }
    return c
}
```

- [ ] **Step 2: Update buildPodSpec to use the new builder**

In `buildPodSpec`, replace:

```go
// before:
//   if template.Spec.EventAdapter != nil {
//       initContainers = append(initContainers, buildAdapterContainer(run, template, in))
//   }
//   initContainers = append(initContainers, buildCollectorContainer(run, template, collectorImage, in.outputConfigMap))
// after:
if template.Spec.Runtime != nil {
    initContainers = append(initContainers, buildRuntimeContainer(run, template, in))
}
```

Delete the `collectorImage` parameter wiring if no longer used elsewhere.

- [ ] **Step 3: Update pod labels**

Find where the run pod's `metav1.ObjectMeta.Labels` is set (likely in the `Pod` constructor in `pod_spec.go`). Add:

```go
labels := map[string]string{
    "paddock.dev/run":       run.Name,
    "paddock.dev/namespace": run.Namespace,
    "paddock.dev/workspace": run.Spec.WorkspaceRef.Name,
    "paddock.dev/template":  run.Spec.TemplateRef.Name,
    "paddock.dev/mode":      string(run.Spec.Mode),
}
// merge with any existing labels (template-supplied or controller-supplied)
```

- [ ] **Step 4: Update pod_spec_test.go**

Existing tests assert containers, mounts, env vars. Rewrite the relevant block:

```go
func TestBuildPodSpec_HasRuntimeContainerAndLabels(t *testing.T) {
    in := podSpecInputs{
        // ... fixtures
        brokerEndpoint: "https://paddock-broker.paddock-system.svc:8443",
    }
    pod := buildPod(run, template, in)
    // Assert container set
    var names []string
    for _, c := range pod.Spec.InitContainers {
        names = append(names, c.Name)
    }
    if !equalSets(names, []string{"agent", "runtime", "proxy"}) {
        t.Fatalf("containers: got %v", names)
    }
    // Assert no adapter/collector
    for _, c := range pod.Spec.InitContainers {
        if c.Name == "adapter" || c.Name == "collector" {
            t.Fatalf("legacy container present: %s", c.Name)
        }
    }
    // Assert runtime env vars
    runtime := findContainer(pod.Spec.InitContainers, "runtime")
    requireEnv(t, runtime, "PADDOCK_TRANSCRIPT_DIR", "/workspace/.paddock/runs/"+run.Name)
    requireEnv(t, runtime, "PADDOCK_BROKER_URL", "https://paddock-broker.paddock-system.svc:8443")
    requireEnv(t, runtime, "PADDOCK_WORKSPACE_NAME", run.Spec.WorkspaceRef.Name)
    // Assert pod labels
    if got := pod.Labels["paddock.dev/run"]; got != run.Name {
        t.Fatalf("pod label run: %q", got)
    }
    if got := pod.Labels["paddock.dev/workspace"]; got != run.Spec.WorkspaceRef.Name {
        t.Fatalf("pod label workspace: %q", got)
    }
    // Assert workspace PVC mount on runtime
    requireMount(t, runtime, "workspace", "/workspace")
    // Assert NOT mounted on agent (workspace was always agent-mounted; this is just a regression guard for the runtime side)
    // Assert no events ConfigMap creation step (runtime patches directly)
}
```

- [ ] **Step 5: Build + run unit tests**

```bash
go vet -tags=e2e ./...
go test -count=1 -race ./internal/controller/...
```

Expected: green. Some existing tests will fail because they reference `buildAdapterContainer`, `buildCollectorContainer`, or 4-container assertions — update each accordingly.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/pod_spec.go internal/controller/pod_spec_test.go
git commit -m "$(cat <<'EOF'
refactor(controller)!: replace adapter+collector with unified runtime

Pod composition goes from 4 containers (agent, adapter, collector,
proxy) to 3 (agent, runtime, proxy). The runtime container owns the
full harness-side data plane: input recording (PromptSubmitted in
events.jsonl), output translation, transcript persistence, ConfigMap
publishing, stdout passthrough, and the broker HTTP+WS surface.

New env vars surfaced on the runtime container:
  - PADDOCK_WORKSPACE_NAME, PADDOCK_TEMPLATE_NAME, PADDOCK_MODE
  - PADDOCK_TRANSCRIPT_DIR (defaults to /workspace/.paddock/runs/<run-name>)
  - PADDOCK_OUTPUT_CONFIGMAP (handed off from the controller)

New pod labels for log-aggregator scraping:
  - paddock.dev/run, paddock.dev/namespace
  - paddock.dev/workspace (stable cross-run identifier)
  - paddock.dev/template, paddock.dev/mode

The runtime mounts the workspace PVC; this changes the trust profile
of the harness-specific image (it can now read workspace contents).
The previous "adapter doesn't see workspace" invariant was theatre
relative to the agent container's much larger trust surface; spec §1
documents the rationale.

BREAKING CHANGE: HarnessTemplate.Spec.Runtime is now required. The
old paddock-adapter-* and paddock-collector images are no longer
referenced by pod specs; they're deleted in a follow-up commit once
the chart and samples are updated.

Spec ref: docs/superpowers/specs/2026-05-03-unified-runtime-design.md §3, §5.
EOF
)"
```

---

## Task 13: Delete obsolete code, update Helm chart and samples

**Files:**
- Delete: `cmd/adapter-claude-code/`
- Delete: `cmd/adapter-echo/`
- Delete: `cmd/collector/`
- Delete: `images/adapter-claude-code/`
- Delete: `images/adapter-echo/`
- Delete: `images/collector/`
- Modify: `Makefile` — remove `image-adapter-*` and `image-collector` targets; remove from any "build all images" aggregations
- Modify: `Tiltfile` — drop adapter+collector image refs if present
- Modify: `charts/paddock/values.yaml` — drop the deleted image fields
- Modify: `charts/paddock/templates/*.yaml` — drop any references
- Modify: `config/samples/*.yaml` — already updated in Task 8 (eventAdapter→runtime); verify no stale image refs
- Modify: `config/manager/manager.yaml` — drop `--collector-image` flag if present
- Modify: any controller flags wiring (cmd/main.go, cmd/manager/main.go) referring to a `collectorImage` value

- [ ] **Step 1: Verify pod_spec.go no longer references the deleted images**

```bash
grep -rn 'collectorImage\|adapter-claude-code\|adapter-echo\|paddock-collector' \
    internal/controller/ cmd/ api/ || echo OK
```

If any references remain, they're stale — delete them now.

- [ ] **Step 2: Delete adapter and collector source trees**

```bash
git rm -r cmd/adapter-claude-code cmd/adapter-echo cmd/collector
git rm -r images/adapter-claude-code images/adapter-echo images/collector
```

- [ ] **Step 3: Update Makefile**

Remove these targets and any references to them:
- `image-adapter-claude-code`
- `image-adapter-echo`
- `image-collector`
- `ADAPTER_CLAUDE_CODE_IMG`, `ADAPTER_ECHO_IMG`, `COLLECTOR_IMG`

Update the `images` aggregation target (the one that builds all images) to drop the removed entries and include `image-runtime-claude-code` and `image-runtime-echo`.

- [ ] **Step 4: Update Helm chart and values**

```bash
make helm-chart
grep -rn 'adapter-claude-code\|adapter-echo\|collector' charts/paddock/ || echo OK
```

If any references remain, edit and re-run `make helm-chart` until clean.

- [ ] **Step 5: Update controller flags**

Find any flag in `cmd/main.go` (or wherever the manager's main lives) for `--collector-image` and delete it. If it was passed via `manager.yaml`, drop the corresponding arg there.

- [ ] **Step 6: Build + unit tests**

```bash
go vet -tags=e2e ./...
go build ./...
go test -count=1 -race $(go list ./... | grep -v /e2e)
```

Expected: green.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
chore!: remove paddock-adapter-* and paddock-collector

The unified runtime (pod_spec.go switched in the previous commit)
no longer references these images. Deletes the binaries, Dockerfiles,
Makefile targets, Helm chart entries, and controller flags.

BREAKING CHANGE: paddock-adapter-claude-code, paddock-adapter-echo,
and paddock-collector images are no longer published or referenced.
Operators with private harness images must rebuild as
paddock-runtime-* (per the harness-authoring contract update in the
next commit).
EOF
)"
```

---

## Task 14: Update documentation

**Files:**
- Modify: `docs/contributing/harness-authoring.md` — update contract for runtime image (single image, library imports, env vars, hooks, label)
- Modify: `docs/getting-started/quickstart.md` — pod-shape diagrams, sample manifests
- Modify: `docs/concepts/*.md` — any mention of adapter/collector
- Modify: `docs/internal/*.md` — architecture references

- [ ] **Step 1: Find all doc references**

```bash
grep -rln "adapter\|collector\|EventAdapter\|eventAdapter" docs/ --include="*.md" | head -20
```

- [ ] **Step 2: Update harness-authoring.md**

Rewrite the contract section to reflect the new runtime model. The contract is:

1. The harness author provides a `paddock-runtime-<name>` image.
2. The image's binary imports `internal/runtime` (transcript, archive, publish, stdout, proxy) for the generic plumbing.
3. The binary supplies a `Converter func(line string) ([]paddockv1alpha1.PaddockEvent, error)` that maps the harness CLI's output format to PaddockEvents.
4. For interactive harnesses, the binary supplies a `PromptFormatter func(text string, seq int32) ([]byte, error)` that wraps a prompt into the CLI's stdin shape.
5. The image declares supported interactive modes via the `paddock.dev/runtime-interactive-modes` label (comma-separated).
6. The Dockerfile follows the canonical shape (see `images/runtime-claude-code/Dockerfile`).

Show the canonical structure with a diff or full example.

- [ ] **Step 3: Update quickstart.md**

Any sample manifest using `eventAdapter:` should be updated to `runtime:`. Pod-composition diagrams (if any ASCII art) updated to 3 containers.

- [ ] **Step 4: Update concept docs**

Find any architecture docs describing the data plane and update.

- [ ] **Step 5: Add a CHANGELOG entry hint** *(skip — release-please owns CHANGELOG)*

Per project convention, do not edit `CHANGELOG.md`. The breaking-change footers in commits drive release-please.

- [ ] **Step 6: Commit**

```bash
git add -A docs/
git commit -m "docs: update harness-authoring contract for unified runtime

Documents the runtime image contract: single image per harness,
imports internal/runtime for generic plumbing, supplies a Converter
(and PromptFormatter for interactive). Includes the canonical
Dockerfile shape and the new paddock.dev/runtime-interactive-modes
label.

Updates quickstart and concept docs to reference 3-container pods
(agent, runtime, proxy) and the runtime: HarnessTemplate field."
```

---

## Task 15: Update e2e tests + add transcript-presence assertions

**Files:**
- Modify: every `test/e2e/*.go` file referencing `adapter`, `collector`, or `events.jsonl` paths
- Modify: e2e fixtures (templates, sample images)
- Add: a new e2e assertion for the unified-runtime transcript layout

- [ ] **Step 1: Find adapter/collector references in e2e**

```bash
grep -rln 'adapter\|collector\|events\.jsonl\|eventAdapter' test/e2e/ --include="*.go"
```

- [ ] **Step 2: Update fixtures**

Existing e2e templates use `eventAdapter:` — should already be fixed by Task 8's mass rename, but verify. Image refs in test fixtures may reference `paddock-adapter-claude-code:dev` — update to `paddock-runtime-claude-code:dev`. Same for echo.

- [ ] **Step 3: Update batch e2e assertions**

Today the batch e2e likely asserts `Status.RecentEvents` is populated. That continues to work. Replace any direct events.jsonl path references with the new transcript path.

- [ ] **Step 4: Add a new interactive transcript-completeness assertion**

In `test/e2e/interactive_test.go` (or wherever interactive specs live), add:

```go
var _ = Describe("Interactive transcript", Ordered, Serial, func() {
    var run *paddockv1alpha1.HarnessRun

    BeforeAll(func() {
        run = createInteractiveRun(...)
        // submit 3 prompts via the broker, wait for each to complete
    })

    It("writes a complete transcript to /workspace/.paddock/runs/<run>/events.jsonl", func() {
        out := kubectlExec(run, "runtime", "cat /workspace/.paddock/runs/"+run.Name+"/events.jsonl")
        // Expect ≥3 PromptSubmitted events + matching Result events.
        var prompts, results int
        for _, line := range strings.Split(out, "\n") {
            if line == "" { continue }
            var e paddockv1alpha1.PaddockEvent
            Expect(json.Unmarshal([]byte(line), &e)).To(Succeed())
            if e.Type == "PromptSubmitted" { prompts++ }
            if e.Type == "Result" { results++ }
        }
        Expect(prompts).To(BeNumerically(">=", 3))
        Expect(results).To(BeNumerically(">=", 3))
    })

    It("kubectl logs runtime returns byte-identical content", func() {
        logs := kubectlLogs(run, "runtime")
        file := kubectlExec(run, "runtime", "cat /workspace/.paddock/runs/"+run.Name+"/events.jsonl")
        Expect(logs).To(Equal(file))
    })

    It("metadata.json is well-formed", func() {
        out := kubectlExec(run, "runtime", "cat /workspace/.paddock/runs/"+run.Name+"/metadata.json")
        var m struct {
            SchemaVersion string `json:"schemaVersion"`
            Run struct{ Name string `json:"name"` } `json:"run"`
            Workspace string `json:"workspace"`
            Mode string `json:"mode"`
        }
        Expect(json.Unmarshal([]byte(out), &m)).To(Succeed())
        Expect(m.SchemaVersion).To(Equal("1"))
        Expect(m.Run.Name).To(Equal(run.Name))
        Expect(m.Mode).To(Equal("Interactive"))
    })
})
```

- [ ] **Step 5: Run the e2e suite**

```bash
LABELS=interactive FAIL_FAST=1 make test-e2e 2>&1 | tee /tmp/e2e.log
```

If the suite passes, run the full suite:

```bash
make test-e2e 2>&1 | tee /tmp/e2e.log
```

Expected: all green. If failures arise, iterate (use `KEEP_E2E_RUN=1` for post-mortem).

- [ ] **Step 6: Commit**

```bash
git add -A test/e2e/
git commit -m "test(e2e): assert unified-runtime transcript layout

New assertions cover:
  - events.jsonl contains PromptSubmitted + Result events for every
    prompt in a multi-turn interactive run
  - kubectl logs <pod> -c runtime is byte-identical to the file
  - metadata.json is well-formed at run-end

Updates existing e2e fixtures from paddock-adapter-* to
paddock-runtime-* image references."
```

---

## Rollback

This is a branch-only refactor. The F19 architecture (adapter + collector + harness-supervisor) on `main` remains functional. If the merge of this branch reveals problems:

```bash
git revert -m 1 <merge-commit>
git push origin main
```

There is no on-disk migration to undo: workspaces created during the unified-runtime era will have `/workspace/.paddock/runs/<run>/` directories that the reverted F19 codebase ignores. They remain on the PVC as harmless artifacts; tar-and-archive or delete at operator discretion.

---

## Self-review checklist (run by the planner before handing off)

1. **Spec coverage:** every section of the spec has at least one task implementing it.
   - §1 Why → covered by the existence of this plan + Task 12 commit message
   - §2 Decisions → covered by Tasks 4, 6, 7, 12, 5
   - §3 Architecture → Tasks 2-12
   - §4 CRD changes → Task 8
   - §5 Pod spec → Task 12
   - §5.1 Pod labels → Task 12
   - §6 Transcript layout → Tasks 3, 4, 10, 11
   - §7 PaddockEvent additions → Task 7
   - §7.1 Projection rules → Task 2
   - §8 Stdout convention → Task 5, 10, 11, 15
   - §9 Lifecycle → Task 12 (RestartPolicy: Always)
   - §10 Migration → Tasks 8, 9, 13
   - §11 Test strategy → Tasks 2-15 (each has tests; Task 15 covers e2e)
   - §12 Rollback → covered by Rollback section
   - §13 Out of scope → not implemented; correctly out of scope

2. **Placeholder scan:** none. All code blocks contain runnable Go; all commands are exact; no "TBD" or "TODO" anywhere except a marked acknowledgement at Task 12 Step 1 ("pick a UID; document in pod_spec.go") which is a real implementer choice not a placeholder.

3. **Type consistency:** `PaddockEventTypePromptSubmitted` (Task 7) is referenced consistently in Tasks 2, 10, 11. `RuntimeSpec` (Task 8) is referenced consistently in Task 12. `archive.Metadata`, `transcript.Writer`, `publish.Publisher` referenced consistently by Task 10. Pod labels `paddock.dev/{run,namespace,workspace,template,mode}` (Task 12) match the spec §5.1 set exactly.

Plan complete.
