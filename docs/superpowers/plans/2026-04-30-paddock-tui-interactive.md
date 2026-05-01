# `paddock-tui` × Interactive HarnessRun Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `paddock-tui` to drive Interactive `HarnessRun`s end-to-end — palette-based control surface, broker WebSocket integration, and a controller-side default that puts every agent's `$HOME` on the workspace PVC so tool installs and one-time logins persist across runs.

**Architecture:** Three interlocking changes ship as one unit. (1) The TUI's prompt input becomes pure prompt text; control commands move to a command palette overlay. (2) A new TUI-private `internal/paddocktui/broker/` package opens an authenticated TLS+WSS connection to `paddock-broker` via a programmatic port-forward and exposes Submit/Interrupt/End/Stream. (3) The controller's pod-spec generator unconditionally sets `HOME=<workspaceMount>/.home` and adds a `paddock-home-init` init container that ensures the directory exists.

**Tech Stack:** Go 1.26, `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles`, `github.com/charmbracelet/lipgloss`, `github.com/coder/websocket` (already in go.mod), `k8s.io/client-go/tools/portforward`, `k8s.io/client-go/kubernetes/typed/authentication/v1` (TokenRequest), `sigs.k8s.io/controller-runtime`. Tests: standard `testing`, `httptest`, `sigs.k8s.io/controller-runtime/pkg/client/fake`, `sigs.k8s.io/controller-runtime/pkg/envtest`, the existing `test/e2e/` harness.

**Spec:** `docs/superpowers/specs/2026-04-30-paddock-tui-interactive-design.md`.

**Branch:** `feat/paddock-tui-interactive` (built on top of `feat/paddock-tui`).

---

## File structure

### Phase 1 — `HOME`-from-PVC (controller)

| Path | Responsibility |
|---|---|
| `internal/controller/pod_spec.go` | New constants: `homeSubdirRelPath`, `paddockHomeInitContainerName`, `defaultHomeInitImage`. New helper `effectiveHomePath(template) string`. New helper `buildHomeInitContainer(template) corev1.Container`. `buildEnv` adds `HOME` env var. `buildPodSpec` prepends the new init container to `initContainers`. |
| `internal/controller/pod_spec_test.go` | New tests for `effectiveHomePath`, `buildHomeInitContainer`, env-var presence on agent, init-slot ordering. |
| `internal/controller/harnessrun_controller_test.go` | Existing assertions about init-container counts get nudged. |
| `cmd/manager/main.go` (or wherever flags live) | New `--home-init-image` flag mirroring `--collector-image`/`--proxy-image`. |
| `test/e2e/home_persistence_e2e_test.go` | New e2e: workspace + Batch run A writes `~/.foo`; Batch run B reads it. |
| `docs/guides/workspaces-and-home.md` | New operator guide for the HOME-from-PVC default and the cross-uid footgun. |

### Phase 2 — Command palette (TUI)

| Path | Responsibility |
|---|---|
| `internal/paddocktui/app/palette.go` | Palette state (`PaletteState` struct), parser (`ParsePalette`), command constants, dispatch helper. |
| `internal/paddocktui/app/palette_test.go` | Unit tests for the parser + dispatch. |
| `internal/paddocktui/ui/palette.go` | Palette overlay rendering. |
| `internal/paddocktui/ui/palette_test.go` | Snapshot/golden tests for the overlay. |
| `internal/paddocktui/app/model.go` | Add `Palette PaletteState`, `PendingPrompt string` fields. |
| `internal/paddocktui/app/update.go` | Replace in-prompt slash dispatch with palette dispatch. Wire `:`/`Ctrl-K` to open palette. Wire arrow-keys-in-mainpane to a new `RunCursor` index. |
| `internal/paddocktui/app/slash.go` | Removed (parsing migrates wholly to palette.go). |
| `internal/paddocktui/app/slash_test.go` | Removed alongside `slash.go`. |
| `internal/paddocktui/app/types.go` | Replace `FocusMainScroll` with `FocusMainPane`; add `RunCursor int`. |
| `internal/paddocktui/ui/view.go` | Layer the palette overlay; render the `PendingPrompt` indicator in the status footer. |
| `internal/paddocktui/ui/mainpane.go` | Highlight the row at `RunCursor` when main pane is focused. |
| `internal/paddocktui/ui/modal_help.go` | Updated key reference: palette opens via `:`/`Ctrl-K`, no in-prompt slash commands. |

### Phase 3 — TUI broker client (plumbing)

| Path | Responsibility |
|---|---|
| `internal/paddocktui/broker/client.go` | `Client` struct holding port-forward, CA, token cache; `New(ctx, opts) (*Client, error)`; `Close()`. |
| `internal/paddocktui/broker/client_test.go` | TLS pinning + token refresh against `httptest.Server`. |
| `internal/paddocktui/broker/portforward.go` | Programmatic port-forward via `k8s.io/client-go/tools/portforward`. |
| `internal/paddocktui/broker/portforward_test.go` | Mock-spdy roundtrip tests where feasible; otherwise integration test that's tagged `e2e`. |
| `internal/paddocktui/broker/auth.go` | `TokenRequest` for an SA, with audience pin + caching. |
| `internal/paddocktui/broker/auth_test.go` | Cache hit/miss + refresh-near-expiry tests against `fake.Clientset`. |
| `internal/paddocktui/broker/tls.go` | Read broker CA from a `Secret` and build a `*tls.Config`. |
| `internal/paddocktui/broker/tls_test.go` | Unit tests for CA loading (good cert / missing key / bad PEM). |
| `internal/paddocktui/broker/prompt.go` | `Submit`, `Interrupt`, `End` HTTP methods. |
| `internal/paddocktui/broker/prompt_test.go` | Tests against `httptest.Server` covering 202/409/4xx/5xx. |
| `internal/paddocktui/broker/stream.go` | `Open(ctx, ns, run) (<-chan StreamFrame, error)`; reconnect goroutine. |
| `internal/paddocktui/broker/stream_test.go` | WS round-trip + reconnect-after-close. |

### Phase 4 — Interactive lifecycle (TUI integration)

| Path | Responsibility |
|---|---|
| `internal/paddocktui/app/types.go` | `SessionMode` enum (Batch/Armed/Bound); `InteractiveBinding` struct holding `RunName`, `CurrentTurnSeq`, `LastFrameAt`. Extend `SessionState`. |
| `internal/paddocktui/app/messages.go` | New messages: `interactiveArmedMsg`, `interactiveBoundMsg{Run}`, `interactivePromptSubmittedMsg{Seq}`, `interactiveStreamOpenedMsg{Ch}`, `interactiveFrameMsg{Frame}`, `interactiveStreamClosedMsg{Err}`, `interactiveInterruptedMsg`, `interactiveEndedMsg`, `pendingPromptSetMsg`, `pendingPromptFlushedMsg`. |
| `internal/paddocktui/app/commands.go` | New `tea.Cmd`s: `submitInteractivePromptCmd`, `interruptInteractiveCmd`, `endInteractiveCmd`, `openInteractiveStreamCmd`, `nextInteractiveFrameCmd`, `detectBoundRunCmd`. |
| `internal/paddocktui/app/update.go` | Palette dispatch for `interactive`/`cancel`/`end`/`reattach`; submit routing (Batch vs Interactive); pending-prompt buffer; frame folding into bound-run events; terminal-phase fallback. |
| `internal/paddocktui/runs/list.go` | New: `List(ctx, c, ns, workspaceRef) ([]HarnessRun, error)` — filter by `spec.workspaceRef`. Used for bound-run detection. |
| `internal/paddocktui/runs/list_test.go` | Filtering + ordering tests against `fake.Client`. |
| `internal/paddocktui/runs/create.go` | Extend `CreateOptions` with `Mode paddockv1alpha1.HarnessRunMode`; default Batch keeps existing behaviour. |
| `internal/paddocktui/runs/create_test.go` | Test the Interactive path explicitly. |
| `internal/paddocktui/cmd/tui.go` | Wire `*broker.Client` lifecycle: open at startup, close on quit. New flags `--broker-service`, `--broker-namespace`, `--broker-port`, `--broker-sa`, `--home-init-image`. |
| `internal/paddocktui/ui/mainpane.go` | When session is `Bound`, render the bound run as ONE growing run instead of one box per turn. |

### Phase 5 — Docs + release notes

| Path | Responsibility |
|---|---|
| `docs/guides/claude-code-tui-quickstart.md` | Update to reflect one-time `claude /login` (HOME persists). |
| `docs/guides/interactive-harnessruns.md` | Cross-link to TUI flow. |
| `docs/guides/README.md` | Index update for `workspaces-and-home.md`. |
| `docs/reference/cli/paddock-tui.md` (or equivalent in the existing tree) | New flags. |
| `docs/internal/migrations/<latest>.md` | Add migration note about HOME-from-PVC and palette move. |
| `docs/superpowers/plans/2026-04-30-paddock-tui-interactive.md` | This plan. |

### Phase 6 — End-to-end tests

| Path | Responsibility |
|---|---|
| `test/e2e/interactive_tui_e2e_test.go` | Drive an Interactive run through the broker endpoints from a programmatic TUI client; assert `/end` terminates with `reason: explicit`. |
| `test/e2e/home_persistence_e2e_test.go` | Already listed under Phase 1 — created at the same time as the controller change so the change is provably covered by an integration check. |

---

## Phase 1 — `HOME`-from-PVC (controller)

The controller change is the most cross-cutting piece and the easiest to test in isolation. It ships first inside this branch so subsequent TUI commits can build on a workspace shape that already persists `~/.claude/`.

### Task 1: `effectiveHomePath` helper + tests

**Files:**
- Modify: `internal/controller/pod_spec.go` (around line 871, beside `effectiveWorkspaceMount`)
- Modify: `internal/controller/pod_spec_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/controller/pod_spec_test.go`:

```go
func TestEffectiveHomePath(t *testing.T) {
    cases := []struct {
        name       string
        mountPath  string
        wantPrefix string
    }{
        {"default mount", "", "/workspace/.home"},
        {"custom mount", "/repo", "/repo/.home"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            tmpl := &resolvedTemplate{}
            tmpl.Spec.Workspace.MountPath = tc.mountPath
            if got := effectiveHomePath(tmpl); got != tc.wantPrefix {
                t.Errorf("effectiveHomePath() = %q, want %q", got, tc.wantPrefix)
            }
        })
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestEffectiveHomePath -v`
Expected: FAIL — undefined `effectiveHomePath`.

- [ ] **Step 3: Implement the helper**

In `internal/controller/pod_spec.go`, add a new constant and helper just after `effectiveWorkspaceMount`:

```go
// homeSubdirRelPath is the relative path under the workspace mount
// that hosts the agent's HOME. Spec: "single shared HOME at
// <mount>/.home/" — partitioning to per-harness HOMEs was explicitly
// rejected during brainstorming so tool installs and login state
// carry across harness families.
const homeSubdirRelPath = ".home"

// effectiveHomePath returns the absolute HOME directory for the
// agent container of any run using template. The path lives under
// the same workspace mount the agent already sees as cwd.
func effectiveHomePath(template *resolvedTemplate) string {
    return path.Join(effectiveWorkspaceMount(template), homeSubdirRelPath)
}
```

Add `"path"` to the import block if not already present.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestEffectiveHomePath -v`
Expected: PASS for both subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/pod_spec.go internal/controller/pod_spec_test.go
git commit -m "feat(controller): effectiveHomePath helper for HOME-from-PVC"
```

---

### Task 2: Inject `HOME` env on the agent container

**Files:**
- Modify: `internal/controller/pod_spec.go` (in `buildEnv`, around line 819)
- Modify: `internal/controller/pod_spec_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/controller/pod_spec_test.go`:

```go
func TestBuildEnv_HomeFromPVC(t *testing.T) {
    run := &paddockv1alpha1.HarnessRun{}
    run.Name = "hr-x"
    tmpl := &resolvedTemplate{}
    env := buildEnv(run, tmpl, podSpecInputs{})
    var got string
    for _, e := range env {
        if e.Name == "HOME" {
            got = e.Value
        }
    }
    if got != "/workspace/.home" {
        t.Errorf("HOME env = %q, want %q", got, "/workspace/.home")
    }
}

func TestBuildEnv_HomeFollowsCustomMount(t *testing.T) {
    run := &paddockv1alpha1.HarnessRun{}
    run.Name = "hr-y"
    tmpl := &resolvedTemplate{}
    tmpl.Spec.Workspace.MountPath = "/repo"
    env := buildEnv(run, tmpl, podSpecInputs{})
    var got string
    for _, e := range env {
        if e.Name == "HOME" {
            got = e.Value
        }
    }
    if got != "/repo/.home" {
        t.Errorf("HOME env = %q, want %q", got, "/repo/.home")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestBuildEnv_Home -v`
Expected: FAIL — `HOME` env not present.

- [ ] **Step 3: Implement**

In `internal/controller/pod_spec.go`, inside `buildEnv`, after the
existing `PADDOCK_*` block (around line 829), append:

```go
    // HOME-from-PVC default (spec 2026-04-30-paddock-tui-interactive
    // §3 / Resolved Q3): the agent's HOME lives on the workspace PVC
    // so tool installs and one-time logins (e.g. claude /login)
    // persist across runs in the same workspace. Single shared HOME
    // for every harness — partitioning was rejected because
    // continuity across harness families is the desired behaviour.
    env = append(env, corev1.EnvVar{
        Name:  "HOME",
        Value: effectiveHomePath(template),
    })
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestBuildEnv_Home -v`
Expected: PASS for both subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/pod_spec.go internal/controller/pod_spec_test.go
git commit -m "feat(controller): set HOME on agent container to <workspace>/.home"
```

---

### Task 3: `paddock-home-init` init container

**Files:**
- Modify: `internal/controller/pod_spec.go` (new helper + register flag)
- Modify: `internal/controller/pod_spec_test.go`
- Modify: `cmd/manager/main.go` (or the file housing other image flags)

- [ ] **Step 1: Write the failing test**

Append to `internal/controller/pod_spec_test.go`:

```go
func TestBuildHomeInitContainer(t *testing.T) {
    tmpl := &resolvedTemplate{}
    in := podSpecInputs{homeInitImage: "busybox:1.36"}
    c := buildHomeInitContainer(tmpl, in)

    if c.Name != paddockHomeInitContainerName {
        t.Errorf("Name = %q, want %q", c.Name, paddockHomeInitContainerName)
    }
    if c.Image != "busybox:1.36" {
        t.Errorf("Image = %q, want busybox:1.36", c.Image)
    }
    if want := "/workspace"; len(c.VolumeMounts) == 0 || c.VolumeMounts[0].MountPath != want {
        t.Errorf("workspace volume not mounted at %q; got %+v", want, c.VolumeMounts)
    }
    if !strings.Contains(strings.Join(c.Command, " ")+" "+strings.Join(c.Args, " "), "/workspace/.home") {
        t.Errorf("init command does not reference /workspace/.home; got %v %v", c.Command, c.Args)
    }
    if c.SecurityContext == nil || c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != 0 {
        t.Errorf("init container must run as root for chmod; got SC=%+v", c.SecurityContext)
    }
}
```

Add `"strings"` import if not already there.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestBuildHomeInitContainer -v`
Expected: FAIL — `buildHomeInitContainer`, `paddockHomeInitContainerName`, `homeInitImage` undefined.

- [ ] **Step 3: Implement constants and helper**

In `internal/controller/pod_spec.go` constants block (around line 60):

```go
    paddockHomeInitContainerName = "paddock-home-init"
    // defaultHomeInitImage is overridable via --home-init-image. Kept
    // tag-pinned so a controller image upgrade doesn't silently shift
    // the init image.
    DefaultHomeInitImage = "busybox:1.36"
```

In `podSpecInputs`:

```go
    // homeInitImage is the image used for the paddock-home-init init
    // container. Empty falls back to DefaultHomeInitImage.
    homeInitImage string
```

Add the helper near `buildIPTablesInitContainer`:

```go
// buildHomeInitContainer returns the one-shot init container that
// ensures HOME exists on the workspace PVC before the agent starts.
// Idempotent (mkdir -p) so repeat runs are no-ops; runs as root only
// because the volume's existing ownership may not match the agent's
// runtime UID. FSGroup on the pod (gid 65532) handles cross-container
// group writability — see the comment on buildPodSpec's SecurityContext.
func buildHomeInitContainer(template *resolvedTemplate, in podSpecInputs) corev1.Container {
    img := in.homeInitImage
    if img == "" {
        img = DefaultHomeInitImage
    }
    home := effectiveHomePath(template)
    return corev1.Container{
        Name:    paddockHomeInitContainerName,
        Image:   img,
        Command: []string{"/bin/sh", "-c"},
        Args:    []string{fmt.Sprintf("mkdir -p %q && chmod 0775 %q", home, home)},
        SecurityContext: &corev1.SecurityContext{
            RunAsUser:                ptr.To(int64(0)),
            AllowPrivilegeEscalation: ptr.To(false),
            Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
            SeccompProfile: &corev1.SeccompProfile{
                Type: corev1.SeccompProfileTypeRuntimeDefault,
            },
        },
        VolumeMounts: []corev1.VolumeMount{
            {Name: workspaceVolumeName, MountPath: effectiveWorkspaceMount(template)},
        },
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestBuildHomeInitContainer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/pod_spec.go internal/controller/pod_spec_test.go
git commit -m "feat(controller): paddock-home-init container for HOME-from-PVC"
```

---

### Task 4: Slot the init container into pod-spec assembly

**Files:**
- Modify: `internal/controller/pod_spec.go` (`buildPodSpec`, around line 313)
- Modify: `internal/controller/pod_spec_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/controller/pod_spec_test.go`:

```go
func TestBuildPodSpec_HomeInitFirst(t *testing.T) {
    run := &paddockv1alpha1.HarnessRun{}
    run.Name = "hr-1"
    tmpl := &resolvedTemplate{}
    tmpl.Spec.Image = "alpine:latest"
    spec := buildPodSpec(run, tmpl, podSpecInputs{
        homeInitImage:  "busybox:1.36",
        serviceAccount: "default",
    })
    if len(spec.InitContainers) == 0 || spec.InitContainers[0].Name != paddockHomeInitContainerName {
        var names []string
        for _, c := range spec.InitContainers {
            names = append(names, c.Name)
        }
        t.Fatalf("paddock-home-init must be the first init container; got order: %v", names)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestBuildPodSpec_HomeInitFirst -v`
Expected: FAIL — init container not present.

- [ ] **Step 3: Implement**

In `internal/controller/pod_spec.go`, locate `buildPodSpec` (around line 313 today). Modify the `initContainers := make(...)` block:

```go
    initContainers := make([]corev1.Container, 0, 5)

    // paddock-home-init runs FIRST. mkdir is filesystem-only — no
    // dependency on iptables/proxy/sidecar lifecycle — and HOME points
    // into the workspace PVC, so the dir must exist before any
    // container that reads $HOME starts.
    initContainers = append(initContainers, buildHomeInitContainer(template, in))

    // iptables-init runs next — it must complete before the proxy
    // sidecar starts so the agent's TCP traffic is caught by the
    // REDIRECT chain from the first packet.
    if proxyEnabled(in) && in.interceptionMode == paddockv1alpha1.InterceptionModeTransparent {
        initContainers = append(initContainers, buildIPTablesInitContainer(in))
    }

    if template.Spec.EventAdapter != nil {
        initContainers = append(initContainers, buildAdapterContainer(run, template))
    }
    initContainers = append(initContainers, buildCollectorContainer(run, template, collectorImage, in.outputConfigMap))
    if proxyEnabled(in) {
        initContainers = append(initContainers, buildProxyContainer(run, in))
    }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestBuildPodSpec_HomeInitFirst -v`
Expected: PASS.

- [ ] **Step 5: Existing init-count assertions**

Run the full controller test suite to surface any incidental breakage:

```bash
go test ./internal/controller/... 2>&1 | tail -40
```

If any test asserts `len(spec.InitContainers) == N`, increment by 1. Touch only assertions that fail; don't reformat unrelated code.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/pod_spec.go internal/controller/pod_spec_test.go internal/controller/harnessrun_controller_test.go
git commit -m "feat(controller): slot paddock-home-init at the head of init containers"
```

---

### Task 5: Plumb `--home-init-image` flag

**Files:**
- Modify: `cmd/manager/main.go` (or wherever `--collector-image` is registered — grep first)
- Modify: `internal/controller/harnessrun_controller.go` (where `podSpecInputs` is populated — grep for `collectorImage:` literal)

- [ ] **Step 1: Locate the flag-registration site**

```bash
grep -rn "collector-image" /Users/ttj/projects/personal/paddock/cmd /Users/ttj/projects/personal/paddock/internal/controller
```

Expected: a file (typically `cmd/manager/main.go` or the manager's `Reconciler` struct definition) that registers `--collector-image`, `--proxy-image`, and friends. Open it.

- [ ] **Step 2: Add the flag and pipe it through**

Add a sibling flag and `Reconciler` field:

```go
flag.StringVar(&homeInitImage, "home-init-image", controller.DefaultHomeInitImage,
    "image used for the paddock-home-init init container that creates HOME on the workspace PVC")
```

…and on the reconciler:

```go
// HomeInitImage overrides the image used for the paddock-home-init
// init container. Empty falls back to controller.DefaultHomeInitImage.
HomeInitImage string
```

…wire it into `podSpecInputs`:

```go
in := podSpecInputs{
    ...
    homeInitImage:  r.HomeInitImage,
    ...
}
```

- [ ] **Step 3: Verify build + tests still green**

```bash
go build ./...
go test ./internal/controller/...
```

Expected: build clean; tests pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/manager/main.go internal/controller/harnessrun_controller.go
git commit -m "feat(controller): --home-init-image flag for the paddock-home-init container"
```

---

### Task 6: e2e — HOME persistence across two Batch runs

**Files:**
- Create: `test/e2e/home_persistence_e2e_test.go`

- [ ] **Step 1: Write the test**

Create `test/e2e/home_persistence_e2e_test.go`:

```go
//go:build e2e

package e2e

import (
    "context"
    "fmt"
    "testing"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/types"

    paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// TestHomePersistence locks the HOME-from-PVC default: two Batch runs
// in the same workspace see each other's HOME mutations. A first run
// writes a marker file under $HOME; a second run reads it back.
func TestHomePersistence(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
    defer cancel()

    ns := provisionNamespace(t, ctx)
    ws := provisionWorkspace(t, ctx, ns)
    tmpl := provisionEchoTemplate(t, ctx, ns) // existing test helper that creates an echo HarnessTemplate

    write := &paddockv1alpha1.HarnessRun{
        ObjectMeta: metav1.ObjectMeta{Name: "home-write", Namespace: ns},
        Spec: paddockv1alpha1.HarnessRunSpec{
            TemplateRef:  paddockv1alpha1.LocalObjectReference{Name: tmpl.Name},
            WorkspaceRef: ws.Name,
            Prompt:       "echo done > $HOME/.persisted",
        },
    }
    mustCreate(t, ctx, write)
    waitForPhase(t, ctx, write, paddockv1alpha1.HarnessRunPhaseSucceeded, 5*time.Minute)

    read := &paddockv1alpha1.HarnessRun{
        ObjectMeta: metav1.ObjectMeta{Name: "home-read", Namespace: ns},
        Spec: paddockv1alpha1.HarnessRunSpec{
            TemplateRef:  paddockv1alpha1.LocalObjectReference{Name: tmpl.Name},
            WorkspaceRef: ws.Name,
            Prompt:       "test -f $HOME/.persisted && cat $HOME/.persisted",
        },
    }
    mustCreate(t, ctx, read)
    waitForPhase(t, ctx, read, paddockv1alpha1.HarnessRunPhaseSucceeded, 5*time.Minute)

    var got paddockv1alpha1.HarnessRun
    if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "home-read"}, &got); err != nil {
        t.Fatalf("get read run: %v", err)
    }
    if got.Status.Phase != paddockv1alpha1.HarnessRunPhaseSucceeded {
        t.Fatalf("read run did not succeed; phase=%s, message=%s",
            got.Status.Phase, fmt.Sprintf("%+v", got.Status.Conditions))
    }
}
```

If `provisionEchoTemplate`, `provisionWorkspace`, `provisionNamespace`,  `mustCreate`, `waitForPhase`, `kubeClient` aren't existing helpers, scan the e2e package for the closest equivalents and adapt the names — these helpers are well-trodden in the existing suite.

- [ ] **Step 2: Don't run the e2e suite locally per CLAUDE.md**

Skip running. The e2e suite is gated to a separate job. Trust the build-tag compile check.

```bash
go vet -tags=e2e ./test/e2e/...
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/home_persistence_e2e_test.go
git commit -m "test(e2e): HOME persistence across two Batch runs in the same workspace"
```

---

### Task 7: New guide page — `workspaces-and-home.md`

**Files:**
- Create: `docs/guides/workspaces-and-home.md`
- Modify: `docs/guides/README.md`

- [ ] **Step 1: Write the guide**

Create `docs/guides/workspaces-and-home.md`. Cover:

- The default behaviour: HOME points into the workspace PVC, persists across runs.
- Path layout: `<workspace mount>/.home/`.
- Practical payoffs: tool installs persist, `claude /login` is one-time.
- Known limitation: cross-uid HOME stomping if a workspace runs harnesses under different runtime UIDs.
- Workarounds for the limitation: dedicate a workspace per harness; or pin all harness images to the same runtime UID.

Aim for ~80–120 lines of user-facing prose. Match the style of the existing guides under `docs/guides/`.

- [ ] **Step 2: Index the new page**

In `docs/guides/README.md`, under the "Operational workflows" list, add:

```markdown
- [workspaces-and-home.md](./workspaces-and-home.md) — the
  HOME-from-PVC default and how to think about workspace continuity.
```

- [ ] **Step 3: Commit**

```bash
git add docs/guides/workspaces-and-home.md docs/guides/README.md
git commit -m "docs(guides): workspaces-and-home — HOME-from-PVC default and known limitations"
```

---

## Phase 2 — Command palette refactor (TUI)

The palette change has to land before the interactive integration because the interactive control surface (`interactive`, `cancel`-as-interrupt, `end`) lives in the palette by design.

### Task 8: Palette state types

**Files:**
- Create: `internal/paddocktui/app/palette.go`
- Create: `internal/paddocktui/app/palette_test.go`

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/paddocktui/app/ -run TestParsePalette -v
```
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement**

Create `internal/paddocktui/app/palette.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
... (apply the standard 15-line header used elsewhere in the package)
*/

package app

import "strings"

// PaletteCmd identifies a recognised palette command. PaletteEmpty
// covers the "user opened the palette but hasn't typed anything yet"
// case; PaletteUnknown carries the typed token back to the caller for
// error reporting.
type PaletteCmd int

const (
    PaletteEmpty PaletteCmd = iota
    PaletteCancel
    PaletteEnd
    PaletteInteractive
    PaletteTemplate
    PaletteReattach
    PaletteStatus
    PaletteEdit
    PaletteHelp
    PaletteUnknown
)

// PaletteState tracks the palette overlay's runtime state. Closed
// palettes carry no input; opening starts with an empty buffer.
type PaletteState struct {
    open  bool
    input string
}

func (p PaletteState) Open() bool   { return p.open }
func (p PaletteState) Input() string { return p.input }

// WithOpen toggles the palette open/closed. Closing clears any
// in-progress input so the next open lands on an empty prompt.
func (p PaletteState) WithOpen(open bool) PaletteState {
    if !open {
        p.input = ""
    }
    p.open = open
    return p
}

// WithInput sets the in-progress input string. Caller is responsible
// for keeping the palette open; closed palettes ignore input writes
// (a no-op so the field stays "" on close).
func (p PaletteState) WithInput(s string) PaletteState {
    if !p.open {
        return p
    }
    p.input = s
    return p
}

// ParsePalette classifies a palette command line. The returned arg is
// any whitespace-separated tail (e.g. for `template claude-code`).
// Empty input returns PaletteEmpty so the dispatcher can treat
// Enter-on-empty as a no-op cleanly.
func ParsePalette(input string) (PaletteCmd, string) {
    in := strings.TrimSpace(input)
    if in == "" {
        return PaletteEmpty, ""
    }
    parts := strings.SplitN(in, " ", 2)
    head := parts[0]
    arg := ""
    if len(parts) == 2 {
        arg = strings.TrimSpace(parts[1])
    }
    switch head {
    case "cancel":
        return PaletteCancel, ""
    case "end":
        return PaletteEnd, ""
    case "interactive":
        return PaletteInteractive, ""
    case "template":
        return PaletteTemplate, arg
    case "reattach":
        return PaletteReattach, ""
    case "status":
        return PaletteStatus, ""
    case "edit":
        return PaletteEdit, ""
    case "help":
        return PaletteHelp, ""
    default:
        return PaletteUnknown, head
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/paddocktui/app/ -run TestParsePalette -v
go test ./internal/paddocktui/app/ -run TestPaletteState -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/app/palette.go internal/paddocktui/app/palette_test.go
git commit -m "feat(paddock-tui): palette state types and parser"
```

---

### Task 9: Palette overlay rendering

**Files:**
- Create: `internal/paddocktui/ui/palette.go`
- Create: `internal/paddocktui/ui/palette_test.go`

- [ ] **Step 1: Write the failing test**

```go
package ui

import (
    "strings"
    "testing"

    "paddock.dev/paddock/internal/paddocktui/app"
)

func TestPaletteView_Closed(t *testing.T) {
    if got := PaletteView(app.PaletteState{}, 80); got != "" {
        t.Errorf("closed palette should render empty; got %q", got)
    }
}

func TestPaletteView_OpenShowsHints(t *testing.T) {
    var p app.PaletteState
    p = p.WithOpen(true)
    p = p.WithInput("can")
    out := PaletteView(p, 80)
    if !strings.Contains(out, "can") {
        t.Errorf("palette overlay should echo current input; got\n%s", out)
    }
    if !strings.Contains(out, "cancel") {
        t.Errorf("palette overlay should hint matching commands; got\n%s", out)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/paddocktui/ui/ -run TestPaletteView -v
```
Expected: FAIL — undefined `PaletteView`.

- [ ] **Step 3: Implement**

Create `internal/paddocktui/ui/palette.go`:

```go
/*
Copyright 2026. ... (15-line license header)
*/

package ui

import (
    "fmt"
    "strings"

    "github.com/charmbracelet/lipgloss"

    "paddock.dev/paddock/internal/paddocktui/app"
)

// paletteCommands is the static catalogue rendered into hint rows when
// the palette is open. Order is the order shown to the user; prefix
// match against the current input filters the visible set.
var paletteCommands = []string{
    "cancel",
    "end",
    "interactive",
    "template <name>",
    "reattach",
    "status",
    "edit",
    "help",
}

// PaletteView renders the command palette overlay. Returns the empty
// string when the palette is closed so the caller can layer it
// unconditionally.
func PaletteView(p app.PaletteState, width int) string {
    if !p.Open() {
        return ""
    }
    in := p.Input()
    var hints []string
    for _, c := range paletteCommands {
        if strings.HasPrefix(c, in) {
            hints = append(hints, "  "+c)
        }
    }
    if len(hints) == 0 {
        hints = append(hints, "  (no matching commands)")
    }
    body := strings.Join(append([]string{"› " + in + "_"}, hints...), "\n")
    style := lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        Padding(0, 1).
        Width(width - 4)
    return style.Render(fmt.Sprintf("Command palette (Esc to close)\n%s", body))
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/paddocktui/ui/ -run TestPaletteView -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/ui/palette.go internal/paddocktui/ui/palette_test.go
git commit -m "feat(paddock-tui): palette overlay view"
```

---

### Task 10: Wire palette open/close key handling

**Files:**
- Modify: `internal/paddocktui/app/model.go` (add `Palette` field)
- Modify: `internal/paddocktui/app/update.go` (extend `handleKeyMsg`)
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

Append to `update_test.go`:

```go
func TestUpdate_ColonOpensPalette(t *testing.T) {
    m := newTestModel(t)
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
    nm := next.(Model)
    if !nm.Palette.Open() {
        t.Fatal(": on empty prompt should open palette")
    }
    if nm.PromptInput != "" {
        t.Errorf("prompt input should not have received the colon; got %q", nm.PromptInput)
    }
}

func TestUpdate_ColonInsidePromptIsLiteral(t *testing.T) {
    m := newTestModel(t)
    m.PromptInput = "hello"
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
    nm := next.(Model)
    if nm.Palette.Open() {
        t.Fatal(": with non-empty prompt should be literal, not open palette")
    }
    if nm.PromptInput != "hello:" {
        t.Errorf("PromptInput = %q, want %q", nm.PromptInput, "hello:")
    }
}

func TestUpdate_CtrlKOpensPaletteRegardlessOfPrompt(t *testing.T) {
    m := newTestModel(t)
    m.PromptInput = "halfway typed"
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
    nm := next.(Model)
    if !nm.Palette.Open() {
        t.Fatal("Ctrl-K should open the palette unconditionally")
    }
    if nm.PromptInput != "halfway typed" {
        t.Errorf("Ctrl-K must not consume prompt input; got %q", nm.PromptInput)
    }
}

func TestUpdate_EscClosesPalette(t *testing.T) {
    m := newTestModel(t)
    m.Palette = m.Palette.WithOpen(true).WithInput("can")
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
    nm := next.(Model)
    if nm.Palette.Open() {
        t.Fatal("Esc should close the palette")
    }
    if nm.Palette.Input() != "" {
        t.Errorf("closed palette must have empty input; got %q", nm.Palette.Input())
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/paddocktui/app/ -run TestUpdate_Colon -v
```
Expected: FAIL — `Model.Palette` does not exist.

- [ ] **Step 3: Add `Palette` field to Model**

In `internal/paddocktui/app/model.go`, in the Model struct:

```go
// Palette tracks the command palette overlay's open/closed state and
// in-progress input. See palette.go.
Palette PaletteState
```

- [ ] **Step 4: Wire key handling**

In `internal/paddocktui/app/update.go`, locate `handleKeyMsg`. Before any focus-specific dispatch, insert:

```go
    // Palette key handling — palette claims keys when open; from
    // closed it can be opened by ":" (only on an empty prompt) or
    // Ctrl-K (anywhere).
    if m.Palette.Open() {
        return handlePaletteKey(m, msg)
    }
    if msg.Type == tea.KeyCtrlK {
        m.Palette = m.Palette.WithOpen(true)
        return m, nil
    }
    if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ':' && m.PromptInput == "" {
        m.Palette = m.Palette.WithOpen(true)
        return m, nil
    }
```

Then add the helper:

```go
// handlePaletteKey routes keystrokes while the palette is open: Esc
// closes; Enter executes the parsed command; Backspace edits;
// printable runes append to the input. Tab autocompletes the unique
// matching command name when exactly one matches.
func handlePaletteKey(m Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch msg.Type {
    case tea.KeyEsc:
        m.Palette = m.Palette.WithOpen(false)
        return m, nil
    case tea.KeyEnter:
        cmd, arg := ParsePalette(m.Palette.Input())
        m.Palette = m.Palette.WithOpen(false)
        return dispatchPalette(m, cmd, arg)
    case tea.KeyBackspace:
        in := m.Palette.Input()
        if len(in) > 0 {
            m.Palette = m.Palette.WithInput(in[:len(in)-1])
        }
        return m, nil
    case tea.KeyTab:
        m.Palette = m.Palette.WithInput(autocompletePalette(m.Palette.Input()))
        return m, nil
    case tea.KeySpace:
        m.Palette = m.Palette.WithInput(m.Palette.Input() + " ")
        return m, nil
    case tea.KeyRunes:
        m.Palette = m.Palette.WithInput(m.Palette.Input() + string(msg.Runes))
        return m, nil
    }
    return m, nil
}

// autocompletePalette returns the unique prefix-matching command
// name when exactly one candidate matches; otherwise returns input
// unchanged.
func autocompletePalette(input string) string {
    var match string
    n := 0
    for _, c := range []string{"cancel", "end", "interactive", "template ", "reattach", "status", "edit", "help"} {
        if strings.HasPrefix(c, input) {
            match = c
            n++
        }
    }
    if n == 1 {
        return match
    }
    return input
}
```

`dispatchPalette` is added in Task 11.

- [ ] **Step 5: Stub `dispatchPalette` so the file compiles**

In `update.go`:

```go
// dispatchPalette routes a parsed palette command. Each branch is
// filled in by subsequent tasks; for now they're stubs returning the
// model unchanged so the palette open/close key wiring can be tested
// in isolation.
func dispatchPalette(m Model, cmd PaletteCmd, arg string) (tea.Model, tea.Cmd) {
    _ = arg
    switch cmd {
    case PaletteEmpty, PaletteUnknown,
        PaletteCancel, PaletteEnd, PaletteInteractive,
        PaletteTemplate, PaletteReattach,
        PaletteStatus, PaletteEdit, PaletteHelp:
        return m, nil
    }
    return m, nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
go test ./internal/paddocktui/app/ -run TestUpdate -v
```
Expected: PASS for the four new tests; existing tests untouched.

- [ ] **Step 7: Commit**

```bash
git add internal/paddocktui/app/model.go internal/paddocktui/app/update.go internal/paddocktui/app/update_test.go
git commit -m "feat(paddock-tui): wire palette open/close keys (':' on empty prompt, Ctrl-K, Esc)"
```

---

### Task 11: Migrate existing slash commands into `dispatchPalette`

**Files:**
- Modify: `internal/paddocktui/app/update.go` (`dispatchPalette` + remove old `dispatchSlash`)
- Delete: `internal/paddocktui/app/slash.go`, `internal/paddocktui/app/slash_test.go`
- Modify: `internal/paddocktui/app/update_test.go`
- Modify: `internal/paddocktui/app/prompt.go` (remove the `:`-prefix detour from prompt-submit)
- Modify: `internal/paddocktui/app/prompt_test.go`

- [ ] **Step 1: Write the failing test**

Append to `update_test.go` (re-uses existing test fixtures for sessions/templates):

```go
func TestPalette_HelpOpensHelpModal(t *testing.T) {
    m := newTestModel(t)
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
    nm := next.(Model)
    for _, r := range []rune{'h', 'e', 'l', 'p'} {
        next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
        nm = next.(Model)
    }
    next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyEnter})
    nm = next.(Model)
    if nm.Modal != ModalHelp {
        t.Errorf("expected help modal open, got %v", nm.Modal)
    }
}

func TestPalette_TemplateUpdatesLastTemplate(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName, LastTemplate: "old"},
    }
    m.SessionOrder = []string{testSessionName}
    m.Focused = testSessionName
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
    for _, s := range []string{"t", "e", "m", "p", "l", "a", "t", "e", " ", "n", "e", "w"} {
        next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(s[0])}})
    }
    next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
    if got := next.(Model).Sessions[testSessionName].Session.LastTemplate; got != "new" {
        t.Errorf("LastTemplate = %q, want %q", got, "new")
    }
}
```

(Adapt the loop above to use a string-runes utility if the existing test file has one.)

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/paddocktui/app/ -run TestPalette_ -v
```
Expected: FAIL — palette dispatch is still stubs.

- [ ] **Step 3: Replace stubs in `dispatchPalette`**

In `update.go`, replace `dispatchPalette` with the full mapping. For each branch, pull the body from the existing `dispatchSlash` (which today lives in `update.go` near the other key handlers — find by `case SlashHelp`, `case SlashTemplate`, etc.). The new dispatcher reads as:

```go
func dispatchPalette(m Model, cmd PaletteCmd, arg string) (tea.Model, tea.Cmd) {
    switch cmd {
    case PaletteEmpty:
        return m, nil

    case PaletteUnknown:
        m.ErrBanner = fmt.Sprintf("unknown command: %s", arg)
        return m, nil

    case PaletteHelp:
        m.Modal = ModalHelp
        return m, nil

    case PaletteStatus:
        // existing :status body — tail of dispatchSlash's SlashStatus
        // case lifted verbatim. Add a copy-paste with the existing
        // ErrBanner / status-line writes.
        return m, nil

    case PaletteEdit:
        // existing :edit body — same lift.
        return m, nil

    case PaletteTemplate:
        if arg == "" {
            m.ErrBanner = "template requires a name (e.g. `template claude-code`)"
            return m, nil
        }
        focused := m.Sessions[m.Focused]
        if focused == nil {
            m.ErrBanner = errNoSessionFocused
            return m, nil
        }
        focused.Session.LastTemplate = arg
        return m, patchLastTemplateCmd(m.Client, m.Namespace, m.Focused, arg)

    case PaletteCancel:
        // Filled in Task 22 (interactive) and Task 12 (Batch fallback).
        return m, nil

    case PaletteEnd:
        // Filled in Task 23.
        return m, nil

    case PaletteInteractive:
        // Filled in Task 19.
        return m, nil

    case PaletteReattach:
        // Filled in Task 26.
        return m, nil
    }
    return m, nil
}
```

For now, fill in the cases for `PaletteHelp`, `PaletteStatus`, `PaletteEdit`, `PaletteTemplate`, and `PaletteUnknown` — those have direct equivalents in the existing slash dispatcher. Leave `PaletteCancel`, `PaletteEnd`, `PaletteInteractive`, `PaletteReattach` as `return m, nil` placeholders; they're fleshed out later in Phase 4. (The plan tracks these — they're called out in subsequent tasks.)

- [ ] **Step 4: Delete `slash.go` and its test**

```bash
git rm internal/paddocktui/app/slash.go internal/paddocktui/app/slash_test.go
```

Remove the corresponding `case SlashXxx:` branches from the previous slash dispatcher in `update.go` and any references to `dispatchSlash` and the `SlashCmd` type. Search:

```bash
grep -rn "SlashCmd\|SlashHelp\|SlashTemplate\|SlashCancel\|SlashInteractive\|SlashStatus\|SlashEdit\|SlashUnknown\|SlashQueue\|dispatchSlash\|ParseSlash" internal/paddocktui/
```

Drop every match. The palette is now the single command surface.

- [ ] **Step 5: Update prompt-submit to never parse slashes**

In `internal/paddocktui/app/prompt.go`, locate the prompt-submit handler. Today it pipes `m.PromptInput` through `ParseSlash` before deciding to submit; remove that detour entirely so submit always treats the input as prompt text.

- [ ] **Step 6: Update `prompt_test.go`**

Any test that asserted slash-prefix parsing on prompt submit moves into `palette_test.go` as a palette dispatch test. Delete the slash-parsing prompt tests; keep the plain-text submit tests.

- [ ] **Step 7: Run the full TUI test suite**

```bash
go test ./internal/paddocktui/...
```
Expected: PASS — including the two new palette tests.

- [ ] **Step 8: Commit**

```bash
git add internal/paddocktui/
git commit -m "refactor(paddock-tui): migrate slash commands to the command palette"
```

---

### Task 12: `cancel` palette command — Batch fallback

For sessions in Batch mode (no interactive binding), `cancel` keeps its
existing meaning: cancel the most-recently-created HarnessRun via the
controller. Interactive fan-out lands in Task 22.

**Files:**
- Modify: `internal/paddocktui/app/update.go` (fill `PaletteCancel` in `dispatchPalette`)
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPalette_CancelBatchTriggersControllerCancel(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName, ActiveRunRef: "hr-running"},
    }
    m.Focused = testSessionName
    _, cmd := dispatchPalette(m, PaletteCancel, "")
    if cmd == nil {
        t.Fatal("expected cancelRunCmd, got nil")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/paddocktui/app/ -run TestPalette_CancelBatch -v
```
Expected: FAIL.

- [ ] **Step 3: Implement**

Replace the placeholder for `PaletteCancel`:

```go
case PaletteCancel:
    focused := m.Sessions[m.Focused]
    if focused == nil {
        m.ErrBanner = errNoSessionFocused
        return m, nil
    }
    if focused.Interactive != nil {
        // Interactive path lives in Task 22; left as a TODO marker
        // — not a placeholder string in the doc, an actual case
        // branch to be filled when interactive lands.
        return m, interruptInteractiveCmd(m.BrokerClient, m.Namespace, focused.Interactive.RunName)
    }
    if focused.Session.ActiveRunRef == "" {
        m.ErrBanner = "nothing to cancel"
        return m, nil
    }
    return m, cancelRunCmd(m.Client, m.Namespace, focused.Session.ActiveRunRef)
```

The reference to `focused.Interactive` and `interruptInteractiveCmd` would compile-fail at this point — the `Interactive` field lands in Task 13 (the new sequencing has it before the buffer task) and the broker `Interrupt` command lands in Task 24. So this case block is also filled in stages. Keep only the Batch fallback for now; the interactive branch is added in Task 27 once both dependencies are in place.

```go
case PaletteCancel:
    focused := m.Sessions[m.Focused]
    if focused == nil {
        m.ErrBanner = errNoSessionFocused
        return m, nil
    }
    if focused.Session.ActiveRunRef == "" {
        m.ErrBanner = "nothing to cancel"
        return m, nil
    }
    return m, cancelRunCmd(m.Client, m.Namespace, focused.Session.ActiveRunRef)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/paddocktui/app/ -run TestPalette_CancelBatch -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/app/update.go internal/paddocktui/app/update_test.go
git commit -m "feat(paddock-tui): cancel palette command for Batch sessions"
```

---

### Task 13: `InteractiveBinding` shape on `SessionState`

The PendingPrompt buffer in Task 14 needs the `Interactive` field on `SessionState`, so the type lands first.

**Files:**
- Modify: `internal/paddocktui/app/types.go`

(No new tests — a type-only change exercised by every subsequent task.)

- [ ] **Step 1: Add the type**

In `internal/paddocktui/app/types.go`:

```go
// SessionMode is the high-level state of a TUI session.
type SessionMode int

const (
    SessionBatch SessionMode = iota
    SessionArmed
    SessionBound
)

// InteractiveBinding holds the TUI's view of an Interactive HarnessRun
// the focused session is bound to. CurrentTurnSeq mirrors
// HarnessRun.status.interactive.currentTurnSeq — non-nil means a turn
// is in flight; nil means the run is between prompts.
type InteractiveBinding struct {
    RunName        string
    CurrentTurnSeq *int32
    LastFrameAt    time.Time
}

// Mode reports the session's current high-level state, derived from
// SessionState fields.
func (s *SessionState) Mode() SessionMode {
    if s.Interactive != nil {
        return SessionBound
    }
    if s.Armed {
        return SessionArmed
    }
    return SessionBatch
}
```

…and extend `SessionState`:

```go
// Armed is true when the user has run the `interactive` palette
// command but hasn't yet typed the kick-off prompt.
Armed bool

// Interactive holds the bound interactive run, when the session is
// in SessionBound. Nil otherwise.
Interactive *InteractiveBinding
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
go test ./internal/paddocktui/...
```
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add internal/paddocktui/app/types.go
git commit -m "feat(paddock-tui): SessionMode + InteractiveBinding shape"
```

---

### Task 14: Single-prompt buffer (`PendingPrompt`)

Type-ahead while a turn is in flight: the second prompt is held in a
single-slot buffer. Submitting a third while one is buffered replaces
it. Builds on the `Interactive` field added in Task 13.

**Files:**
- Modify: `internal/paddocktui/app/model.go` (add `PendingPrompt` field)
- Modify: `internal/paddocktui/app/prompt.go`
- Modify: `internal/paddocktui/app/prompt_test.go`
- Modify: `internal/paddocktui/ui/view.go` (status footer shows the buffer)

- [ ] **Step 1: Write the failing test**

```go
func TestPrompt_SubmitWhileTurnInFlightBuffers(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName},
        Interactive: &InteractiveBinding{
            RunName: "hr-int", CurrentTurnSeq: ptrInt32(2),
        },
    }
    m.Focused = testSessionName
    m.PromptInput = "next idea"
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
    nm := next.(Model)
    if nm.PendingPrompt != "next idea" {
        t.Errorf("PendingPrompt = %q, want %q", nm.PendingPrompt, "next idea")
    }
    if nm.PromptInput != "" {
        t.Errorf("PromptInput should clear after buffering; got %q", nm.PromptInput)
    }
}

func ptrInt32(v int32) *int32 { return &v }
```

- [ ] **Step 2: Run failing test**

```bash
go test ./internal/paddocktui/app/ -run TestPrompt_SubmitWhileTurnInFlight -v
```
Expected: FAIL — `PendingPrompt` field doesn't exist.

- [ ] **Step 3: Add the field**

In `internal/paddocktui/app/model.go`:

```go
// PendingPrompt holds a single submitted prompt that's waiting for
// the broker to stop returning 409 (an in-flight turn on the bound
// interactive run). Submitting another prompt while non-empty
// replaces this one. The status footer surfaces a hint.
PendingPrompt string
```

- [ ] **Step 4: Modify the submit handler**

In `internal/paddocktui/app/prompt.go`'s submit path:

```go
focused := m.Sessions[m.Focused]
if focused != nil && focused.Interactive != nil && focused.Interactive.CurrentTurnSeq != nil {
    m.PendingPrompt = m.PromptInput
    m.PromptInput = ""
    return m, nil
}
```

This branch executes BEFORE the existing Batch submit path. The Interactive non-buffered submit path is added in Task 24.

- [ ] **Step 5: Surface the buffer in the status footer**

In `internal/paddocktui/ui/view.go`, locate the status-footer assembly. Append a hint when non-empty:

```go
if m.PendingPrompt != "" {
    statusLine = "queued: " + truncate(m.PendingPrompt, 60) + "  ·  " + statusLine
}
```

Add a `truncate(s string, n int) string` helper in `view.go` if one doesn't exist.

- [ ] **Step 6: Run tests**

```bash
go test ./internal/paddocktui/app/ -run TestPrompt -v
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/paddocktui/app/model.go internal/paddocktui/app/prompt.go internal/paddocktui/app/prompt_test.go internal/paddocktui/ui/view.go
git commit -m "feat(paddock-tui): single-prompt buffer for type-ahead during in-flight turns"
```

---

### Task 15: Run navigation — Tab cycle + arrow-keys-in-mainpane

**Files:**
- Modify: `internal/paddocktui/app/types.go` (`FocusArea` rename)
- Modify: `internal/paddocktui/app/model.go` (add `RunCursor int`)
- Modify: `internal/paddocktui/app/update.go` (`handleKeyMsg` Tab + Up/Down)
- Modify: `internal/paddocktui/app/update_test.go`
- Modify: `internal/paddocktui/ui/mainpane.go` (highlight cursor row)

- [ ] **Step 1: Write the failing test**

```go
func TestNavigation_TabCyclesFocus(t *testing.T) {
    m := newTestModel(t)
    if m.FocusArea != FocusPrompt {
        t.Fatalf("default focus = %v, want FocusPrompt", m.FocusArea)
    }
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
    if next.(Model).FocusArea != FocusSidebar {
        t.Errorf("after one Tab, focus = %v, want FocusSidebar", next.(Model).FocusArea)
    }
    next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyTab})
    if next.(Model).FocusArea != FocusMainPane {
        t.Errorf("after two Tabs, focus = %v, want FocusMainPane", next.(Model).FocusArea)
    }
    next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyTab})
    if next.(Model).FocusArea != FocusPrompt {
        t.Errorf("after three Tabs, focus should wrap to FocusPrompt; got %v", next.(Model).FocusArea)
    }
}

func TestNavigation_ArrowsMoveRunCursorWhenMainPaneFocused(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName},
        Runs: []RunSummary{{Name: "r1"}, {Name: "r2"}, {Name: "r3"}},
    }
    m.Focused = testSessionName
    m.FocusArea = FocusMainPane
    next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
    if next.(Model).RunCursor != 1 {
        t.Errorf("Down should advance RunCursor; got %d", next.(Model).RunCursor)
    }
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/paddocktui/app/ -run TestNavigation_ -v
```
Expected: FAIL — `FocusMainPane`, `RunCursor` undefined.

- [ ] **Step 3: Rename `FocusMainScroll` → `FocusMainPane`**

In `internal/paddocktui/app/types.go`:

```go
const (
    FocusSidebar FocusArea = iota
    FocusPrompt
    FocusMainPane
)
```

Search-replace `FocusMainScroll` → `FocusMainPane` across the package.

- [ ] **Step 4: Add `RunCursor` to Model**

```go
// RunCursor indexes into the focused session's Runs slice for
// keyboard navigation. Only meaningful when FocusArea == FocusMainPane.
RunCursor int
```

- [ ] **Step 5: Implement Tab + arrows in `handleKeyMsg`**

In `update.go`:

```go
case tea.KeyTab:
    switch m.FocusArea {
    case FocusPrompt:
        m.FocusArea = FocusSidebar
    case FocusSidebar:
        m.FocusArea = FocusMainPane
    case FocusMainPane:
        m.FocusArea = FocusPrompt
    }
    return m, nil
```

Within the existing `case tea.KeyDown:` and `case tea.KeyUp:` branches, add a guard:

```go
if m.FocusArea == FocusMainPane {
    state := m.Sessions[m.Focused]
    if state == nil {
        return m, nil
    }
    if msg.Type == tea.KeyDown && m.RunCursor+1 < len(state.Runs) {
        m.RunCursor++
    } else if msg.Type == tea.KeyUp && m.RunCursor > 0 {
        m.RunCursor--
    }
    return m, nil
}
```

- [ ] **Step 6: Highlight cursor row in mainpane**

In `internal/paddocktui/ui/mainpane.go`, when iterating runs, render the row at `m.RunCursor` with a focused style if `m.FocusArea == FocusMainPane`. Cosmetic; bias toward minimal diff (e.g. wrap the header line in a focused-style render).

- [ ] **Step 7: Run tests**

```bash
go test ./internal/paddocktui/...
```
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/paddocktui/
git commit -m "feat(paddock-tui): Tab cycle focus + Up/Down navigates runs in main pane"
```

---

## Phase 3 — TUI broker client

The broker client is a self-contained package. It has zero TUI knowledge — its consumers (Phase 4) glue it into the Bubble Tea reducer.

### Task 16: Broker client skeleton

**Files:**
- Create: `internal/paddocktui/broker/client.go`
- Create: `internal/paddocktui/broker/client_test.go`

- [ ] **Step 1: Write the failing test**

```go
package broker

import (
    "context"
    "testing"
)

func TestNew_ValidatesOpts(t *testing.T) {
    cases := []Options{
        {},
        {Service: "paddock-broker"},
        {Service: "paddock-broker", Namespace: "paddock-system"},
    }
    for i, opts := range cases {
        if _, err := New(context.Background(), opts); err == nil {
            t.Errorf("case %d: expected error from incomplete opts %+v", i, opts)
        }
    }
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/paddocktui/broker/ -v
```
Expected: FAIL — package doesn't compile.

- [ ] **Step 3: Create the skeleton**

```go
/*
Copyright 2026. ... (15-line license header)
*/

// Package broker is the TUI-private HTTP+WebSocket client for the
// paddock-broker. It opens a programmatic port-forward to the broker
// Service, pins the cluster-issued CA, and mints SA-bound,
// audience-pinned tokens via the TokenRequest API.
package broker

import (
    "context"
    "crypto/tls"
    "errors"
    "fmt"
    "net/http"

    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
)

// Options configure a Client. All four fields are required; Source is
// the rest.Config for the cluster the broker lives in.
type Options struct {
    Service        string
    Namespace      string
    Port           int
    ServiceAccount string

    // Source is the rest.Config used for port-forward + TokenRequest.
    Source *rest.Config

    // CASecretName, CASecretNamespace, CASecretKey identify the
    // Kubernetes Secret containing the broker's serving CA. Empty
    // CASecretKey defaults to "ca.crt".
    CASecretName      string
    CASecretNamespace string
    CASecretKey       string
}

// Client owns the broker connection. New starts a port-forward and a
// background token refresher; Close stops both.
type Client struct {
    opts    Options
    kube    kubernetes.Interface
    httpCli *http.Client
    tlsCfg  *tls.Config
    auth    *tokenCache
    pf      *forwarder
}

// New initialises a Client. Returns an error if the port-forward
// fails or the CA cannot be loaded.
func New(ctx context.Context, opts Options) (*Client, error) {
    if opts.Service == "" || opts.Namespace == "" || opts.Port == 0 {
        return nil, errors.New("broker.New: Service, Namespace, Port required")
    }
    if opts.Source == nil {
        return nil, errors.New("broker.New: Source rest.Config required")
    }
    kc, err := kubernetes.NewForConfig(opts.Source)
    if err != nil {
        return nil, fmt.Errorf("broker.New: kube client: %w", err)
    }
    c := &Client{opts: opts, kube: kc}
    // Subsequent tasks fill in tlsCfg, auth, pf, httpCli.
    return c, nil
}

// Close releases the port-forward and stops background goroutines.
func (c *Client) Close() error {
    if c.pf != nil {
        return c.pf.Close()
    }
    return nil
}

// forwarder is the port-forward handle; defined in portforward.go.
type forwarder struct{}

func (f *forwarder) Close() error { return nil }

// tokenCache is defined in auth.go.
type tokenCache struct{}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/paddocktui/broker/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/broker/client.go internal/paddocktui/broker/client_test.go
git commit -m "feat(paddock-tui): broker client skeleton with options validation"
```

---

### Task 17: TLS CA loading from cluster Secret

**Files:**
- Create: `internal/paddocktui/broker/tls.go`
- Create: `internal/paddocktui/broker/tls_test.go`

- [ ] **Step 1: Write the failing test**

```go
package broker

import (
    "context"
    "crypto/x509"
    "encoding/pem"
    "testing"

    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes/fake"
)

const testCAPEM = `-----BEGIN CERTIFICATE-----
MIIBmDCCAT+gAwIBAgIRAOq...
-----END CERTIFICATE-----
`

func TestLoadCAFromSecret_Found(t *testing.T) {
    sec := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{Name: "broker-tls", Namespace: "ns"},
        Data:       map[string][]byte{"ca.crt": []byte(testCAPEM)},
    }
    kc := fake.NewSimpleClientset(sec)
    pool, err := loadCAFromSecret(context.Background(), kc, "ns", "broker-tls", "ca.crt")
    if err != nil {
        t.Fatal(err)
    }
    if len(pool.Subjects()) == 0 {
        t.Error("expected at least one subject in the cert pool")
    }
}

func TestLoadCAFromSecret_DefaultKey(t *testing.T) {
    sec := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{Name: "broker-tls", Namespace: "ns"},
        Data:       map[string][]byte{"ca.crt": []byte(testCAPEM)},
    }
    kc := fake.NewSimpleClientset(sec)
    if _, err := loadCAFromSecret(context.Background(), kc, "ns", "broker-tls", ""); err != nil {
        t.Errorf("empty key should fall back to ca.crt; got %v", err)
    }
}

func TestLoadCAFromSecret_Missing(t *testing.T) {
    kc := fake.NewSimpleClientset()
    if _, err := loadCAFromSecret(context.Background(), kc, "ns", "missing", "ca.crt"); err == nil {
        t.Error("expected error for missing Secret")
    }
}

func TestLoadCAFromSecret_BadPEM(t *testing.T) {
    sec := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{Name: "broker-tls", Namespace: "ns"},
        Data:       map[string][]byte{"ca.crt": []byte("not pem")},
    }
    kc := fake.NewSimpleClientset(sec)
    if _, err := loadCAFromSecret(context.Background(), kc, "ns", "broker-tls", "ca.crt"); err == nil {
        t.Error("expected error for non-PEM data")
    }
}

// silence unused imports
var _ = pem.Decode
var _ = x509.NewCertPool
```

Replace `testCAPEM` with a real self-signed cert generated for the test (use `crypto/x509` to produce one in a test helper or check in a fixture under `testdata/`).

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/paddocktui/broker/ -run TestLoadCAFromSecret -v
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package broker

import (
    "context"
    "crypto/x509"
    "errors"
    "fmt"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
)

// loadCAFromSecret reads a PEM-encoded CA bundle from a Kubernetes
// Secret. Defaults the key to "ca.crt" when empty (matching
// cert-manager's emitted secrets).
func loadCAFromSecret(ctx context.Context, kc kubernetes.Interface, ns, name, key string) (*x509.CertPool, error) {
    if key == "" {
        key = "ca.crt"
    }
    sec, err := kc.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
    if err != nil {
        return nil, fmt.Errorf("broker: load CA Secret %s/%s: %w", ns, name, err)
    }
    pem, ok := sec.Data[key]
    if !ok || len(pem) == 0 {
        return nil, fmt.Errorf("broker: Secret %s/%s missing key %q", ns, name, key)
    }
    pool := x509.NewCertPool()
    if !pool.AppendCertsFromPEM(pem) {
        return nil, errors.New("broker: CA Secret contains no parseable PEM")
    }
    return pool, nil
}
```

Wire it into `New` in `client.go`:

```go
pool, err := loadCAFromSecret(ctx, kc, opts.CASecretNamespace, opts.CASecretName, opts.CASecretKey)
if err != nil {
    return nil, err
}
c.tlsCfg = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
```

…and add the new fields to `Options` validation.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/paddocktui/broker/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/broker/
git commit -m "feat(paddock-tui): broker client loads + pins CA from a cluster Secret"
```

---

### Task 18: Programmatic port-forward

**Files:**
- Create: `internal/paddocktui/broker/portforward.go`
- Create: `internal/paddocktui/broker/portforward_test.go` (build-tag `e2e`)

- [ ] **Step 1: Write the failing unit test (compile-only)**

For unit-level coverage of the spdy-port-forward primitive, a true
end-to-end exchange requires a real cluster — so the bulk of testing
lives in the e2e tier (Task 30). The local unit test asserts surface
shape only.

```go
package broker

import "testing"

func TestForwarderPlaceholder(t *testing.T) {
    var f *forwarder
    if f != nil {
        t.Fatal("uninitialised forwarder should be nil")
    }
}
```

- [ ] **Step 2: Implement**

`internal/paddocktui/broker/portforward.go`:

```go
package broker

import (
    "bytes"
    "context"
    "fmt"
    "net"
    "net/http"
    "net/url"
    "strings"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/portforward"
    "k8s.io/client-go/transport/spdy"
)

type forwarder struct {
    fwd     *portforward.PortForwarder
    stopCh  chan struct{}
    readyCh chan struct{}
    local   int
}

// startForwarder picks a healthy paddock-broker Pod, opens a SPDY
// stream, and returns a forwarder bound to a local random port. The
// chosen port is available via Local(). Caller must Close() to drop
// the underlying SPDY connection.
func startForwarder(ctx context.Context, kc kubernetes.Interface, cfg *rest.Config, ns, svc string, targetPort int) (*forwarder, error) {
    pods, err := kc.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
        LabelSelector: "app.kubernetes.io/name=" + svc,
    })
    if err != nil {
        return nil, fmt.Errorf("broker: list pods for service %s: %w", svc, err)
    }
    var podName string
    for i := range pods.Items {
        p := pods.Items[i]
        if p.Status.Phase == "Running" {
            podName = p.Name
            break
        }
    }
    if podName == "" {
        return nil, fmt.Errorf("broker: no Ready pod backing service %s/%s", ns, svc)
    }

    transport, upgrader, err := spdy.RoundTripperFor(cfg)
    if err != nil {
        return nil, fmt.Errorf("broker: spdy round-tripper: %w", err)
    }
    serverURL, err := url.Parse(strings.TrimRight(cfg.Host, "/") + fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", ns, podName))
    if err != nil {
        return nil, err
    }
    dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, serverURL)

    stopCh := make(chan struct{})
    readyCh := make(chan struct{})
    var out, errOut bytes.Buffer
    fwd, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", targetPort)}, stopCh, readyCh, &out, &errOut)
    if err != nil {
        return nil, fmt.Errorf("broker: portforward.New: %w", err)
    }
    go func() { _ = fwd.ForwardPorts() }()
    select {
    case <-readyCh:
    case <-ctx.Done():
        close(stopCh)
        return nil, ctx.Err()
    }
    ports, err := fwd.GetPorts()
    if err != nil {
        close(stopCh)
        return nil, fmt.Errorf("broker: portforward GetPorts: %w", err)
    }
    if len(ports) == 0 {
        close(stopCh)
        return nil, fmt.Errorf("broker: portforward returned no ports; stderr=%s", errOut.String())
    }
    return &forwarder{fwd: fwd, stopCh: stopCh, readyCh: readyCh, local: int(ports[0].Local)}, nil
}

func (f *forwarder) Local() int { return f.local }

func (f *forwarder) Close() error {
    if f.stopCh != nil {
        close(f.stopCh)
    }
    return nil
}

// Address returns 127.0.0.1:<local-port> for HTTP/WS dialing.
func (f *forwarder) Address() string {
    return net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", f.local))
}
```

In `client.go`, in `New`:

```go
pf, err := startForwarder(ctx, kc, opts.Source, opts.Namespace, opts.Service, opts.Port)
if err != nil {
    return nil, err
}
c.pf = pf
```

…and replace the placeholder `forwarder` struct + `Close` from Task 16.

- [ ] **Step 3: Verify build**

```bash
go build ./...
go test ./internal/paddocktui/broker/ -v
```
Expected: build clean, unit test passes.

- [ ] **Step 4: Commit**

```bash
git add internal/paddocktui/broker/
git commit -m "feat(paddock-tui): programmatic port-forward to the paddock-broker pod"
```

---

### Task 19: TokenRequest + caching

**Files:**
- Create: `internal/paddocktui/broker/auth.go`
- Create: `internal/paddocktui/broker/auth_test.go`

- [ ] **Step 1: Write the failing test**

```go
package broker

import (
    "context"
    "testing"
    "time"

    authv1 "k8s.io/api/authentication/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/client-go/kubernetes/fake"
    ktesting "k8s.io/client-go/testing"
)

func TestTokenCache_RefreshesNearExpiry(t *testing.T) {
    kc := fake.NewSimpleClientset()
    issued := 0
    kc.PrependReactor("create", "serviceaccounts/token", func(a ktesting.Action) (bool, runtime.Object, error) {
        issued++
        return true, &authv1.TokenRequestStatus{}, nil // placeholder
    })
    cache := newTokenCache(kc, "ns", "default", "paddock-broker", time.Hour)
    if _, err := cache.Get(context.Background()); err != nil {
        t.Fatal(err)
    }
    if _, err := cache.Get(context.Background()); err != nil {
        t.Fatal(err)
    }
    if issued != 1 {
        t.Errorf("expected 1 issued token (cached on second call); got %d", issued)
    }
    cache.expireForTest() // helper that pushes the cached expiry into the past
    if _, err := cache.Get(context.Background()); err != nil {
        t.Fatal(err)
    }
    if issued != 2 {
        t.Errorf("expected refresh after expiry; total issued = %d", issued)
    }
    _ = metav1.Now() // silence import
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/paddocktui/broker/ -run TestTokenCache -v
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package broker

import (
    "context"
    "fmt"
    "sync"
    "time"

    authv1 "k8s.io/api/authentication/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
)

// tokenCache mints SA-bound, audience-pinned tokens via the
// TokenRequest API and caches them until ~half their TTL has elapsed.
type tokenCache struct {
    kc       kubernetes.Interface
    ns       string
    sa       string
    audience string
    ttl      time.Duration

    mu      sync.Mutex
    token   string
    expires time.Time
}

func newTokenCache(kc kubernetes.Interface, ns, sa, audience string, ttl time.Duration) *tokenCache {
    return &tokenCache{kc: kc, ns: ns, sa: sa, audience: audience, ttl: ttl}
}

// Get returns a non-expired token, refreshing if needed. Refreshing
// happens once half of TTL has elapsed; this avoids the TUI doing a
// hot-path token request on every API call.
func (t *tokenCache) Get(ctx context.Context) (string, error) {
    t.mu.Lock()
    defer t.mu.Unlock()
    if t.token != "" && time.Until(t.expires) > t.ttl/2 {
        return t.token, nil
    }
    seconds := int64(t.ttl.Seconds())
    req := &authv1.TokenRequest{
        Spec: authv1.TokenRequestSpec{
            Audiences:         []string{t.audience},
            ExpirationSeconds: &seconds,
        },
    }
    res, err := t.kc.CoreV1().ServiceAccounts(t.ns).CreateToken(ctx, t.sa, req, metav1.CreateOptions{})
    if err != nil {
        return "", fmt.Errorf("broker: TokenRequest for %s/%s: %w", t.ns, t.sa, err)
    }
    t.token = res.Status.Token
    if !res.Status.ExpirationTimestamp.IsZero() {
        t.expires = res.Status.ExpirationTimestamp.Time
    } else {
        t.expires = time.Now().Add(t.ttl)
    }
    return t.token, nil
}

func (t *tokenCache) expireForTest() {
    t.mu.Lock()
    t.expires = time.Now().Add(-time.Second)
    t.mu.Unlock()
}
```

In `client.go::New`:

```go
c.auth = newTokenCache(kc, opts.Namespace, opts.ServiceAccount, "paddock-broker", time.Hour)
```

(The Phase 1 ServiceAccount selection — `opts.ServiceAccount` defaults to `default` in the workspace's namespace — is enforced by the caller in Phase 5 wire-up.)

- [ ] **Step 4: Run tests**

```bash
go test ./internal/paddocktui/broker/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/broker/auth.go internal/paddocktui/broker/auth_test.go internal/paddocktui/broker/client.go
git commit -m "feat(paddock-tui): SA-bound, audience-pinned TokenRequest cache"
```

---

### Task 20: HTTP `Submit` / `Interrupt` / `End`

**Files:**
- Create: `internal/paddocktui/broker/prompt.go`
- Create: `internal/paddocktui/broker/prompt_test.go`

- [ ] **Step 1: Write the failing test**

```go
package broker

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
    t.Helper()
    return &Client{
        opts:    Options{ServiceAccount: "default", Namespace: "ns"},
        httpCli: srv.Client(),
        baseURL: srv.URL,
        auth:    &tokenCache{token: "test-token", expires: time.Now().Add(time.Hour)},
    }
}

func TestSubmit_Returns202_PassesText(t *testing.T) {
    var got struct{ Text string `json:"text"` }
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/runs/ns/run-x/prompts" {
            t.Errorf("path = %s", r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer test-token" {
            t.Errorf("missing bearer; got %q", r.Header.Get("Authorization"))
        }
        if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
            t.Fatal(err)
        }
        w.WriteHeader(http.StatusAccepted)
        _, _ = io.WriteString(w, `{"seq":3}`)
    }))
    defer srv.Close()

    c := newTestClient(t, srv)
    seq, err := c.Submit(context.Background(), "ns", "run-x", "hello")
    if err != nil {
        t.Fatal(err)
    }
    if seq != 3 {
        t.Errorf("seq = %d, want 3", seq)
    }
    if got.Text != "hello" {
        t.Errorf("body text = %q, want hello", got.Text)
    }
}

func TestSubmit_Returns409AsTurnInFlight(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusConflict)
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    if _, err := c.Submit(context.Background(), "ns", "run-x", "hello"); !IsTurnInFlight(err) {
        t.Errorf("expected IsTurnInFlight, got %v", err)
    }
}

func TestEnd_PassesReason(t *testing.T) {
    var got struct{ Reason string `json:"reason"` }
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !strings.HasSuffix(r.URL.Path, "/end") {
            t.Errorf("path = %s", r.URL.Path)
        }
        _ = json.NewDecoder(r.Body).Decode(&got)
        w.WriteHeader(http.StatusAccepted)
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    if err := c.End(context.Background(), "ns", "run-x", "user-quit"); err != nil {
        t.Fatal(err)
    }
    if got.Reason != "user-quit" {
        t.Errorf("body reason = %q, want user-quit", got.Reason)
    }
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/paddocktui/broker/ -run TestSubmit -v
go test ./internal/paddocktui/broker/ -run TestEnd -v
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package broker

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
)

// ErrTurnInFlight is returned by Submit when the broker reports 409
// (currentTurnSeq != nil). Callers buffer the prompt locally.
var ErrTurnInFlight = errors.New("broker: a turn is already in flight on this run")

// IsTurnInFlight reports whether err wraps ErrTurnInFlight.
func IsTurnInFlight(err error) bool { return errors.Is(err, ErrTurnInFlight) }

// Submit POSTs a new prompt to /v1/runs/{ns}/{run}/prompts. Returns
// the broker-assigned turn sequence on success.
func (c *Client) Submit(ctx context.Context, ns, run, text string) (int32, error) {
    body, _ := json.Marshal(struct{ Text string `json:"text"` }{Text: text})
    res, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/v1/runs/%s/%s/prompts", ns, run), bytes.NewReader(body))
    if err != nil {
        return 0, err
    }
    defer res.Body.Close()
    if res.StatusCode == http.StatusConflict {
        return 0, ErrTurnInFlight
    }
    if res.StatusCode != http.StatusAccepted {
        return 0, fmt.Errorf("broker: Submit unexpected status %d", res.StatusCode)
    }
    var out struct{ Seq int32 `json:"seq"` }
    if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
        return 0, fmt.Errorf("broker: decode submit response: %w", err)
    }
    return out.Seq, nil
}

// Interrupt POSTs to /v1/runs/{ns}/{run}/interrupt. The broker drops
// the in-flight turn (if any); the run stays alive.
func (c *Client) Interrupt(ctx context.Context, ns, run string) error {
    res, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/v1/runs/%s/%s/interrupt", ns, run), nil)
    if err != nil {
        return err
    }
    defer res.Body.Close()
    if res.StatusCode != http.StatusAccepted && res.StatusCode != http.StatusOK {
        return fmt.Errorf("broker: Interrupt unexpected status %d", res.StatusCode)
    }
    return nil
}

// End POSTs to /v1/runs/{ns}/{run}/end with a reason. The broker
// terminates the run cleanly.
func (c *Client) End(ctx context.Context, ns, run, reason string) error {
    body, _ := json.Marshal(struct{ Reason string `json:"reason"` }{Reason: reason})
    res, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/v1/runs/%s/%s/end", ns, run), bytes.NewReader(body))
    if err != nil {
        return err
    }
    defer res.Body.Close()
    if res.StatusCode != http.StatusAccepted && res.StatusCode != http.StatusOK {
        return fmt.Errorf("broker: End unexpected status %d", res.StatusCode)
    }
    return nil
}

// do is the central HTTP send: token attach, base URL prefix.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
    tok, err := c.auth.Get(ctx)
    if err != nil {
        return nil, err
    }
    req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
    if err != nil {
        return nil, err
    }
    req.Header.Set("Authorization", "Bearer "+tok)
    req.Header.Set("Content-Type", "application/json")
    return c.httpCli.Do(req)
}
```

In `client.go`, give the Client a `baseURL` field (initialised from the port-forwarder's address as `https://<addr>`) and an `httpCli` configured with `c.tlsCfg`:

```go
c.baseURL = "https://" + pf.Address()
c.httpCli = &http.Client{
    Transport: &http.Transport{TLSClientConfig: c.tlsCfg},
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/paddocktui/broker/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/broker/
git commit -m "feat(paddock-tui): broker client Submit/Interrupt/End"
```

---

### Task 21: WebSocket `Open` + frame channel + reconnect

**Files:**
- Create: `internal/paddocktui/broker/stream.go`
- Create: `internal/paddocktui/broker/stream_test.go`

- [ ] **Step 1: Write the failing test**

```go
package broker

import (
    "context"
    "testing"
    "time"
)

func TestStreamFrame_Round(t *testing.T) {
    // Stand up a tiny WS server that accepts the paddock.stream.v1
    // subprotocol, sends two text frames, and closes. Use
    // github.com/coder/websocket which is already in go.mod.
    // Body filled by the engineer using existing patterns from
    // internal/broker/stream_test.go (the broker-side tests).
    _ = context.Background
    _ = time.Now
    t.Skip("filled by engineer — uses coder/websocket round-trip pattern")
}
```

…and a real test once the implementation lands. Use
`internal/broker/stream*_test.go` (broker-side tests, already in tree)
as the round-trip pattern reference.

- [ ] **Step 2: Implement**

```go
package broker

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "math"
    "net/url"
    "time"

    "github.com/coder/websocket"
)

// StreamFrame is one event frame off the broker stream. Type and Data
// are passed through verbatim from the adapter; the TUI projects them
// into PaddockEvents.
type StreamFrame struct {
    Type string          `json:"type"`
    Data json.RawMessage `json:"data"`
}

// Open opens the broker stream for run ns/run. Frames flow on the
// returned channel until ctx is cancelled or the connection closes.
// Reconnects with exponential backoff (1s/2s/4s/8s, max 5 attempts);
// emits a final close-frame error after exhaustion.
func (c *Client) Open(ctx context.Context, ns, run string) (<-chan StreamFrame, error) {
    out := make(chan StreamFrame, 16)
    tok, err := c.auth.Get(ctx)
    if err != nil {
        return nil, err
    }

    u, err := url.Parse(c.baseURL)
    if err != nil {
        return nil, err
    }
    u.Scheme = "wss"
    u.Path = fmt.Sprintf("/v1/runs/%s/%s/stream", ns, run)

    go func() {
        defer close(out)
        for attempt := 0; attempt < 5; attempt++ {
            conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
                HTTPHeader:   tokenHeader(tok),
                Subprotocols: []string{"paddock.stream.v1"},
                HTTPClient:   c.httpCli,
            })
            if err != nil {
                backoff(ctx, attempt)
                continue
            }
            attempt = 0 // successful dial resets the counter
            if !readFrames(ctx, conn, out) {
                return // ctx cancelled
            }
            // connection ended; loop reconnects after a short delay
            backoff(ctx, attempt)
        }
    }()
    return out, nil
}

func tokenHeader(tok string) (h url.Values) {
    h = url.Values{}
    h.Set("Authorization", "Bearer "+tok)
    return h
}

func readFrames(ctx context.Context, conn *websocket.Conn, out chan<- StreamFrame) bool {
    defer conn.Close(websocket.StatusNormalClosure, "")
    for {
        _, data, err := conn.Read(ctx)
        if err != nil {
            if errors.Is(err, context.Canceled) {
                return false
            }
            return true // reconnect
        }
        var f StreamFrame
        if err := json.Unmarshal(data, &f); err != nil {
            continue
        }
        select {
        case out <- f:
        case <-ctx.Done():
            return false
        }
    }
}

func backoff(ctx context.Context, attempt int) {
    d := time.Duration(math.Pow(2, float64(attempt))) * time.Second
    if d > 8*time.Second {
        d = 8 * time.Second
    }
    select {
    case <-time.After(d):
    case <-ctx.Done():
    }
}
```

(Replace the bogus `tokenHeader` `url.Values` with `http.Header` — the
`websocket.DialOptions` field expects `http.Header`. Adjust during
implementation if the actual `coder/websocket` API needs a tweak.)

- [ ] **Step 3: Write a real round-trip test**

Adapt the `internal/broker/stream_test.go` pattern: stand up a `httptest.Server` that upgrades to `websocket`, sends two frames, closes; assert both arrive on `Open`'s channel; then close the server and assert the channel closes within a short timeout.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/paddocktui/broker/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/broker/stream.go internal/paddocktui/broker/stream_test.go
git commit -m "feat(paddock-tui): broker WS stream with exponential-backoff reconnect"
```

---

## Phase 4 — Interactive lifecycle (TUI integration)

### Task 22: `runs.List` helper + bound-run detection

**Files:**
- Create: `internal/paddocktui/runs/list.go`
- Create: `internal/paddocktui/runs/list_test.go`
- Modify: `internal/paddocktui/runs/create.go` (extend `CreateOptions`)
- Modify: `internal/paddocktui/runs/create_test.go`

- [ ] **Step 1: Write the failing test**

```go
package runs

import (
    "context"
    "testing"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/client/fake"

    paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestList_FilterByWorkspace(t *testing.T) {
    sch := newScheme(t) // existing helper
    a := paddockv1alpha1.HarnessRun{}
    a.Name, a.Namespace, a.Spec.WorkspaceRef = "a", "ns", "ws-x"
    a.CreationTimestamp = metav1.NewTime(time.Now())
    b := paddockv1alpha1.HarnessRun{}
    b.Name, b.Namespace, b.Spec.WorkspaceRef = "b", "ns", "ws-y"
    b.CreationTimestamp = metav1.NewTime(time.Now())
    cl := fake.NewClientBuilder().WithScheme(sch).WithObjects(&a, &b).Build()

    got, err := List(context.Background(), cl, "ns", "ws-x")
    if err != nil {
        t.Fatal(err)
    }
    if len(got) != 1 || got[0].Name != "a" {
        t.Errorf("got %v, want 1 item with name 'a'", got)
    }
    _ = client.IgnoreNotFound // silence import
}
```

- [ ] **Step 2: Run failing test**

```bash
go test ./internal/paddocktui/runs/ -run TestList_FilterByWorkspace -v
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package runs

import (
    "context"
    "sort"

    "sigs.k8s.io/controller-runtime/pkg/client"

    paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// List returns HarnessRuns in ns whose Spec.WorkspaceRef matches.
// Ordered newest-first by CreationTimestamp so callers can pick the
// freshest match for a workspace.
func List(ctx context.Context, c client.Client, ns, workspaceRef string) ([]paddockv1alpha1.HarnessRun, error) {
    var all paddockv1alpha1.HarnessRunList
    if err := c.List(ctx, &all, client.InNamespace(ns)); err != nil {
        return nil, err
    }
    out := all.Items[:0]
    for _, r := range all.Items {
        if r.Spec.WorkspaceRef == workspaceRef {
            out = append(out, r)
        }
    }
    sort.SliceStable(out, func(i, j int) bool {
        return out[i].CreationTimestamp.After(out[j].CreationTimestamp.Time)
    })
    return out, nil
}
```

Extend `CreateOptions` in `internal/paddocktui/runs/create.go`:

```go
type CreateOptions struct {
    Namespace    string
    WorkspaceRef string
    Template     string
    Prompt       string
    // Mode selects between Batch (zero value) and Interactive. Defaults
    // to Batch so existing callers stay green.
    Mode paddockv1alpha1.HarnessRunMode
}
```

…and in the body that constructs the `HarnessRun`:

```go
if opts.Mode != "" {
    run.Spec.Mode = opts.Mode
}
```

Add a test in `create_test.go`:

```go
func TestCreate_InteractiveModePropagates(t *testing.T) {
    sch := newScheme(t)
    cl := fake.NewClientBuilder().WithScheme(sch).Build()
    name, err := Create(context.Background(), cl, CreateOptions{
        Namespace: "ns", WorkspaceRef: "ws", Template: "t",
        Prompt: "hi", Mode: paddockv1alpha1.HarnessRunModeInteractive,
    })
    if err != nil { t.Fatal(err) }
    var got paddockv1alpha1.HarnessRun
    if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: name}, &got); err != nil {
        t.Fatal(err)
    }
    if got.Spec.Mode != paddockv1alpha1.HarnessRunModeInteractive {
        t.Errorf("Mode = %q, want Interactive", got.Spec.Mode)
    }
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/paddocktui/runs/ -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/runs/
git commit -m "feat(paddock-tui): runs.List filtered by workspace; CreateOptions.Mode"
```

---

### Task 23: `interactive` palette command — arm session

**Files:**
- Modify: `internal/paddocktui/app/update.go` (`PaletteInteractive` branch)
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPalette_InteractiveArmsSession(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName},
    }
    m.Focused = testSessionName
    nm, _ := dispatchPalette(m, PaletteInteractive, "")
    s := nm.(Model).Sessions[testSessionName]
    if !s.Armed {
        t.Error("session should be Armed after interactive palette command")
    }
    if s.Interactive != nil {
        t.Error("Armed sessions must not yet have Interactive binding")
    }
}
```

- [ ] **Step 2: Run failing test**

```bash
go test ./internal/paddocktui/app/ -run TestPalette_InteractiveArms -v
```
Expected: FAIL.

- [ ] **Step 3: Implement**

In `dispatchPalette`:

```go
case PaletteInteractive:
    focused := m.Sessions[m.Focused]
    if focused == nil {
        m.ErrBanner = errNoSessionFocused
        return m, nil
    }
    if focused.Interactive != nil {
        m.ErrBanner = "session already bound to an interactive run"
        return m, nil
    }
    focused.Armed = true
    return m, nil
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/paddocktui/app/ -run TestPalette_InteractiveArms -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/app/update.go internal/paddocktui/app/update_test.go
git commit -m "feat(paddock-tui): interactive palette command arms the focused session"
```

---

### Task 24: Submit branches by `SessionMode` (kick-off vs `/prompts` vs Batch)

**Files:**
- Modify: `internal/paddocktui/app/prompt.go`
- Modify: `internal/paddocktui/app/commands.go`
- Modify: `internal/paddocktui/app/messages.go`
- Modify: `internal/paddocktui/app/prompt_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPrompt_ArmedSubmitCreatesInteractiveRun(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName, LastTemplate: "claude-interactive"},
        Armed:   true,
    }
    m.Focused = testSessionName
    m.PromptInput = "kick"
    next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
    if cmd == nil {
        t.Fatal("expected submitRunCmd cmd; got nil")
    }
    nm := next.(Model)
    if nm.Sessions[testSessionName].Armed {
        t.Error("Armed should clear once the kick-off prompt is submitted")
    }
}

func TestPrompt_BoundSubmitCallsBrokerSubmit(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session:     pdksession.Session{Name: testSessionName},
        Interactive: &InteractiveBinding{RunName: "hr-int"},
    }
    m.Focused = testSessionName
    m.PromptInput = "next prompt"
    _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
    if cmd == nil {
        t.Fatal("expected submitInteractivePromptCmd; got nil")
    }
}
```

- [ ] **Step 2: Run failing tests**

```bash
go test ./internal/paddocktui/app/ -run TestPrompt_ArmedSubmit -v
go test ./internal/paddocktui/app/ -run TestPrompt_BoundSubmit -v
```
Expected: FAIL.

- [ ] **Step 3: Add the messages**

In `internal/paddocktui/app/messages.go`:

```go
type interactivePromptSubmittedMsg struct {
    WorkspaceRef string
    Seq          int32
}

type interactiveBoundMsg struct {
    WorkspaceRef string
    RunName      string
}

type interactiveStreamOpenedMsg struct {
    RunName string
    Ch      <-chan paddockbroker.StreamFrame
}

type interactiveFrameMsg struct {
    RunName string
    Frame   paddockbroker.StreamFrame
}

type interactiveStreamClosedMsg struct {
    RunName string
    Err     error
}

type interactiveInterruptedMsg struct{ RunName string }
type interactiveEndedMsg struct{ RunName string }
```

Use the alias `paddockbroker "paddock.dev/paddock/internal/paddocktui/broker"`.

- [ ] **Step 4: Add the commands**

In `internal/paddocktui/app/commands.go`:

```go
func submitInteractivePromptCmd(c *paddockbroker.Client, ns, run, text, workspaceRef string) tea.Cmd {
    return func() tea.Msg {
        seq, err := c.Submit(context.Background(), ns, run, text)
        if err != nil {
            return errMsg{Err: err}
        }
        return interactivePromptSubmittedMsg{WorkspaceRef: workspaceRef, Seq: seq}
    }
}

func interruptInteractiveCmd(c *paddockbroker.Client, ns, run string) tea.Cmd {
    return func() tea.Msg {
        if err := c.Interrupt(context.Background(), ns, run); err != nil {
            return errMsg{Err: err}
        }
        return interactiveInterruptedMsg{RunName: run}
    }
}

func endInteractiveCmd(c *paddockbroker.Client, ns, run, reason string) tea.Cmd {
    return func() tea.Msg {
        if err := c.End(context.Background(), ns, run, reason); err != nil {
            return errMsg{Err: err}
        }
        return interactiveEndedMsg{RunName: run}
    }
}

func openInteractiveStreamCmd(ctx context.Context, c *paddockbroker.Client, ns, run string) tea.Cmd {
    return func() tea.Msg {
        ch, err := c.Open(ctx, ns, run)
        if err != nil {
            return errMsg{Err: err}
        }
        return interactiveStreamOpenedMsg{RunName: run, Ch: ch}
    }
}

func nextInteractiveFrameCmd(run string, ch <-chan paddockbroker.StreamFrame) tea.Cmd {
    return func() tea.Msg {
        f, ok := <-ch
        if !ok {
            return interactiveStreamClosedMsg{RunName: run}
        }
        return interactiveFrameMsg{RunName: run, Frame: f}
    }
}
```

- [ ] **Step 5: Branch the submit handler**

In `internal/paddocktui/app/prompt.go`'s submit path, replace the
existing single-path submit with:

```go
focused := m.Sessions[m.Focused]
if focused == nil {
    return m, nil
}
prompt := m.PromptInput
m.PromptInput = ""

switch focused.Mode() {
case SessionBound:
    if focused.Interactive.CurrentTurnSeq != nil {
        // Buffered — Task 13 already wired this branch.
        m.PendingPrompt = prompt
        return m, nil
    }
    return m, submitInteractivePromptCmd(m.BrokerClient, m.Namespace, focused.Interactive.RunName, prompt, m.Focused)

case SessionArmed:
    focused.Armed = false
    return m, submitRunCmd(m.Client, m.Namespace, m.Focused, focused.Session.LastTemplate, prompt, paddockv1alpha1.HarnessRunModeInteractive)

default: // SessionBatch
    return m, submitRunCmd(m.Client, m.Namespace, m.Focused, focused.Session.LastTemplate, prompt, "")
}
```

(Update `submitRunCmd`'s signature to take a `Mode paddockv1alpha1.HarnessRunMode` argument; default to Batch when empty. The `runs.Create` extension from Task 22 already accepts the mode.)

Add `BrokerClient *paddockbroker.Client` to `Model` in `model.go`.

- [ ] **Step 6: Run tests**

```bash
go test ./internal/paddocktui/app/ -v
```
Expected: PASS for the new tests; existing tests still green (the non-Interactive `Mode` path is the zero value).

- [ ] **Step 7: Commit**

```bash
git add internal/paddocktui/app/
git commit -m "feat(paddock-tui): submit branches by SessionMode (Batch/Armed/Bound)"
```

---

### Task 25: Bound-run kick-off message + open the stream

**Files:**
- Modify: `internal/paddocktui/app/update.go`
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestUpdate_RunCreatedForArmedKickoffBindsAndOpensStream(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName},
    }
    m.Focused = testSessionName
    next, _ := m.Update(runCreatedMsg{
        WorkspaceRef: testSessionName,
        RunName:      "hr-int",
        Mode:         paddockv1alpha1.HarnessRunModeInteractive,
    })
    nm := next.(Model)
    if nm.Sessions[testSessionName].Interactive == nil ||
        nm.Sessions[testSessionName].Interactive.RunName != "hr-int" {
        t.Errorf("expected Interactive binding to hr-int; got %+v", nm.Sessions[testSessionName].Interactive)
    }
}
```

…note `runCreatedMsg` needs a new `Mode` field — Task 25 step 3 adds it.

- [ ] **Step 2: Run failing test**

```bash
go test ./internal/paddocktui/app/ -run TestUpdate_RunCreatedForArmedKickoff -v
```
Expected: FAIL.

- [ ] **Step 3: Extend `runCreatedMsg`**

```go
type runCreatedMsg struct {
    WorkspaceRef string
    RunName      string
    Mode         paddockv1alpha1.HarnessRunMode
}
```

…and update `submitRunCmd` to populate `Mode` so the reducer can branch:

```go
return runCreatedMsg{
    WorkspaceRef: workspaceRef,
    RunName:      name,
    Mode:         opts.Mode,
}
```

- [ ] **Step 4: Branch in `Update`'s `runCreatedMsg` handler**

Replace the existing `case runCreatedMsg:` body with:

```go
case runCreatedMsg:
    state := m.Sessions[msg.WorkspaceRef]
    if state == nil {
        return m, nil
    }
    if msg.Mode != paddockv1alpha1.HarnessRunModeInteractive {
        return m, nil
    }
    state.Interactive = &InteractiveBinding{RunName: msg.RunName}
    return m, openInteractiveStreamCmd(m.ctx, m.BrokerClient, m.Namespace, msg.RunName)
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/paddocktui/app/ -v
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/paddocktui/app/
git commit -m "feat(paddock-tui): bind session on Interactive kickoff and open the stream"
```

---

### Task 26: Frame folding into bound run's events

**Files:**
- Modify: `internal/paddocktui/app/update.go`
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestUpdate_FrameFoldsIntoBoundRun(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session:     pdksession.Session{Name: testSessionName},
        Interactive: &InteractiveBinding{RunName: "hr-int"},
        Events:      map[string][]paddockv1alpha1.PaddockEvent{},
    }
    m.Focused = testSessionName
    frame := paddockbroker.StreamFrame{Type: "Message", Data: json.RawMessage(`{"summary":"hi"}`)}
    next, _ := m.Update(interactiveFrameMsg{RunName: "hr-int", Frame: frame})
    evs := next.(Model).Sessions[testSessionName].Events["hr-int"]
    if len(evs) != 1 || evs[0].Type != "Message" || evs[0].Summary != "hi" {
        t.Errorf("expected one Message event with Summary=hi; got %+v", evs)
    }
}
```

- [ ] **Step 2: Run failing test**

Expected: FAIL.

- [ ] **Step 3: Implement**

Add a helper `frameToEvent(frame) paddockv1alpha1.PaddockEvent` that deserialises `frame.Data` into a `PaddockEvent`-shaped struct (with `summary`, `fields` etc.) and stamps `Type` from the frame. Then in `Update`:

```go
case interactiveFrameMsg:
    state := m.Sessions[m.Focused]
    if state == nil || state.Interactive == nil || state.Interactive.RunName != msg.RunName {
        return m, nil
    }
    ev := frameToEvent(msg.Frame)
    state.Events[msg.RunName] = appendEventDedup(state.Events[msg.RunName], ev)
    state.Interactive.LastFrameAt = time.Now()
    return m, nextInteractiveFrameCmd(msg.RunName, m.interactiveFrames[msg.RunName])
```

(Add an `interactiveFrames map[string]<-chan paddockbroker.StreamFrame` to Model; populate on `interactiveStreamOpenedMsg`.)

- [ ] **Step 4: Run tests**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/app/
git commit -m "feat(paddock-tui): fold broker stream frames into bound run's event ring"
```

---

### Task 27: `cancel` and `end` palette commands during interactive

**Files:**
- Modify: `internal/paddocktui/app/update.go`
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPalette_CancelDuringInteractiveCallsInterrupt(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session:     pdksession.Session{Name: testSessionName},
        Interactive: &InteractiveBinding{RunName: "hr-int"},
    }
    m.Focused = testSessionName
    _, cmd := dispatchPalette(m, PaletteCancel, "")
    if cmd == nil {
        t.Fatal("cancel during interactive must produce interruptInteractiveCmd")
    }
}

func TestPalette_EndDuringInteractiveCallsEnd(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session:     pdksession.Session{Name: testSessionName},
        Interactive: &InteractiveBinding{RunName: "hr-int"},
    }
    m.Focused = testSessionName
    _, cmd := dispatchPalette(m, PaletteEnd, "")
    if cmd == nil {
        t.Fatal("end during interactive must produce endInteractiveCmd")
    }
}

func TestPalette_EndOutsideInteractiveErrorBanner(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName},
    }
    m.Focused = testSessionName
    next, cmd := dispatchPalette(m, PaletteEnd, "")
    if cmd != nil {
        t.Error("end without interactive binding must not fire any cmd")
    }
    if next.(Model).ErrBanner == "" {
        t.Error("end outside interactive must surface an error banner")
    }
}
```

- [ ] **Step 2: Implement**

Replace the placeholder `case PaletteCancel:` from Task 12 with:

```go
case PaletteCancel:
    focused := m.Sessions[m.Focused]
    if focused == nil {
        m.ErrBanner = errNoSessionFocused
        return m, nil
    }
    if focused.Interactive != nil {
        return m, interruptInteractiveCmd(m.BrokerClient, m.Namespace, focused.Interactive.RunName)
    }
    if focused.Session.ActiveRunRef == "" {
        m.ErrBanner = "nothing to cancel"
        return m, nil
    }
    return m, cancelRunCmd(m.Client, m.Namespace, focused.Session.ActiveRunRef)
```

…and `case PaletteEnd:`:

```go
case PaletteEnd:
    focused := m.Sessions[m.Focused]
    if focused == nil {
        m.ErrBanner = errNoSessionFocused
        return m, nil
    }
    if focused.Interactive == nil {
        m.ErrBanner = "no interactive run bound to this session"
        return m, nil
    }
    return m, endInteractiveCmd(m.BrokerClient, m.Namespace, focused.Interactive.RunName, "user-requested")
```

- [ ] **Step 3: Run tests**

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/paddocktui/app/
git commit -m "feat(paddock-tui): cancel→/interrupt and end→/end during interactive"
```

---

### Task 28: Detect bound interactive run on TUI launch + reattach

**Files:**
- Modify: `internal/paddocktui/app/commands.go` (new `detectBoundRunCmd`)
- Modify: `internal/paddocktui/app/update.go` (handle the resulting message)
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestDetectBoundRun_ReattachesOnFocus(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName},
    }
    m.Focused = testSessionName
    hr := paddockv1alpha1.HarnessRun{}
    hr.Name, hr.Namespace, hr.Spec.WorkspaceRef = "hr-live", "default", testSessionName
    hr.Spec.Mode = paddockv1alpha1.HarnessRunModeInteractive
    hr.Status.Phase = paddockv1alpha1.HarnessRunPhaseRunning
    msg := boundRunDetectedMsg{WorkspaceRef: testSessionName, Run: hr}
    next, cmd := m.Update(msg)
    nm := next.(Model)
    if nm.Sessions[testSessionName].Interactive == nil {
        t.Fatal("expected Interactive binding after detection")
    }
    if cmd == nil {
        t.Fatal("expected openInteractiveStreamCmd")
    }
}
```

- [ ] **Step 2: Implement**

In `messages.go`:

```go
type boundRunDetectedMsg struct {
    WorkspaceRef string
    Run          paddockv1alpha1.HarnessRun
}
type noBoundRunMsg struct{ WorkspaceRef string }
```

In `commands.go`:

```go
func detectBoundRunCmd(c client.Client, ns, workspaceRef string) tea.Cmd {
    return func() tea.Msg {
        all, err := pdkruns.List(context.Background(), c, ns, workspaceRef)
        if err != nil {
            return errMsg{Err: err}
        }
        for _, r := range all {
            if r.Spec.Mode != paddockv1alpha1.HarnessRunModeInteractive {
                continue
            }
            switch r.Status.Phase {
            case paddockv1alpha1.HarnessRunPhasePending,
                paddockv1alpha1.HarnessRunPhaseRunning,
                paddockv1alpha1.HarnessRunPhaseIdle:
                return boundRunDetectedMsg{WorkspaceRef: workspaceRef, Run: r}
            }
        }
        return noBoundRunMsg{WorkspaceRef: workspaceRef}
    }
}
```

In `update.go`:

```go
case boundRunDetectedMsg:
    state := m.Sessions[msg.WorkspaceRef]
    if state == nil {
        return m, nil
    }
    state.Interactive = &InteractiveBinding{RunName: msg.Run.Name}
    return m, openInteractiveStreamCmd(m.ctx, m.BrokerClient, m.Namespace, msg.Run.Name)

case noBoundRunMsg:
    // Nothing to do; session is in Batch mode.
    return m, nil
```

In `Init` (or wherever the initial commands are batched), append a `detectBoundRunCmd` for every loaded session, OR fire it on focus change. For MVP we fire on focus + on initial sessions-loaded.

- [ ] **Step 3: Run tests**

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/paddocktui/app/
git commit -m "feat(paddock-tui): detect + auto-reattach bound interactive run on focus"
```

---

### Task 29: Run termination while disconnected — banner

**Files:**
- Modify: `internal/paddocktui/app/update.go` (`runUpdatedMsg` case)
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestUpdate_BoundRunReachingTerminalSurfacesBanner(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session:     pdksession.Session{Name: testSessionName},
        Interactive: &InteractiveBinding{RunName: "hr-int"},
    }
    m.Focused = testSessionName

    hr := paddockv1alpha1.HarnessRun{}
    hr.Name, hr.Namespace = "hr-int", "default"
    hr.Status.Phase = paddockv1alpha1.HarnessRunPhaseCancelled
    cond := metav1.Condition{Type: "InteractiveRunTerminated", Reason: "detach"}
    hr.Status.Conditions = []metav1.Condition{cond}

    next, _ := m.Update(runUpdatedMsg{WorkspaceRef: testSessionName, Run: hr})
    nm := next.(Model)
    if nm.Sessions[testSessionName].Interactive != nil {
        t.Error("Interactive binding must clear on terminal phase")
    }
    if nm.Banner == "" {
        t.Error("expected a banner explaining the termination")
    }
}
```

- [ ] **Step 2: Implement**

Extend the existing `runUpdatedMsg` handler. After the current `upsertRun(m, msg)`:

```go
state := m.Sessions[msg.WorkspaceRef]
if state != nil && state.Interactive != nil && state.Interactive.RunName == msg.Run.Name {
    if isTerminalPhase(msg.Run.Status.Phase) {
        reason := terminationReason(msg.Run)
        state.Interactive = nil
        m.Banner = fmt.Sprintf("interactive run ended (%s) — next prompt creates a Batch run", reason)
    } else {
        if msg.Run.Status.Interactive != nil {
            state.Interactive.CurrentTurnSeq = msg.Run.Status.Interactive.CurrentTurnSeq
        }
    }
}
```

Add `Banner string` to `Model` if it doesn't already exist (alongside `ErrBanner`); `Banner` is non-error context, `ErrBanner` is failure.

Implement small helpers:

```go
func isTerminalPhase(p paddockv1alpha1.HarnessRunPhase) bool {
    switch p {
    case paddockv1alpha1.HarnessRunPhaseSucceeded,
        paddockv1alpha1.HarnessRunPhaseFailed,
        paddockv1alpha1.HarnessRunPhaseCancelled:
        return true
    }
    return false
}

func terminationReason(hr paddockv1alpha1.HarnessRun) string {
    for _, c := range hr.Status.Conditions {
        if c.Type == "InteractiveRunTerminated" {
            return c.Reason
        }
    }
    return string(hr.Status.Phase)
}
```

- [ ] **Step 3: Drain pending prompt on Bound→Batch**

When a bound run terminates, any pending prompt in the buffer is now stale. Surface an additional hint:

```go
if m.PendingPrompt != "" {
    m.Banner += " · pending prompt cleared"
    m.PendingPrompt = ""
}
```

- [ ] **Step 4: Run tests**

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paddocktui/app/
git commit -m "feat(paddock-tui): clear interactive binding + banner on terminal phase"
```

---

### Task 30: Render bound run as one growing run

**Files:**
- Modify: `internal/paddocktui/ui/mainpane.go`
- Modify: `internal/paddocktui/ui/mainpane_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestMainPane_BoundRunRendersAsOneBlock(t *testing.T) {
    startTs := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
    m := app.Model{
        Sessions: map[string]*app.SessionState{
            "alpha": {
                Session:     pdksession.Session{Name: "alpha", LastTemplate: "claude-interactive"},
                Interactive: &app.InteractiveBinding{RunName: "hr-live"},
                Runs: []app.RunSummary{{
                    Name:           "hr-live",
                    Phase:          paddockv1alpha1.HarnessRunPhaseRunning,
                    StartTime:      startTs,
                }},
                Events: map[string][]paddockv1alpha1.PaddockEvent{
                    "hr-live": {
                        {Type: "Message", Summary: "first turn reply"},
                        {Type: "Message", Summary: "second turn reply"},
                    },
                },
            },
        },
        Focused:      "alpha",
        SessionOrder: []string{"alpha"},
    }
    out := MainPaneView(m, 80, 0)
    if strings.Count(out, "╭─") != 1 {
        t.Errorf("bound run must render as ONE box (one ╭─ header); got %d", strings.Count(out, "╭─"))
    }
    if !strings.Contains(out, "first turn reply") || !strings.Contains(out, "second turn reply") {
        t.Errorf("both turn replies must render inside the single box")
    }
}
```

- [ ] **Step 2: Implement**

In `mainpane.go::mainPaneContent`, the existing `for i := range s.Runs { … }` already collapses naturally for the bound case (one run → one box) — but only if `state.Runs` doesn't have stale Batch runs from before the bind. Decision:

- Keep all historical Batch runs visible (they're past, not part of the interactive dialogue).
- Render the BOUND run with a slightly different header marker (`╭═` instead of `╭─`) so the user can distinguish "this is the live interactive run" at a glance.
- Per-turn dividers within the bound run: render a `┼` between consecutive `Message` events whose timestamps differ by more than 100ms (heuristic — turn boundaries). Optional polish; MVP can skip and render all events linearly.

Implement just the header marker change:

```go
header := r.Name + " · " + r.StartTime.Format("15:04:05")
if isBound {
    sections = append(sections, StyleRunHeader.Render("╭═ "+header+" ═"+strings.Repeat("═", 8)))
} else {
    sections = append(sections, StyleRunHeader.Render("╭─ "+header+" ─"+strings.Repeat("─", 8)))
}
```

(Pass `isBound` from the caller based on `state.Interactive != nil && state.Interactive.RunName == r.Name`.)

- [ ] **Step 3: Run tests**

Expected: PASS — including a tweak to the existing snapshot tests, which the engineer should `UPDATE_GOLDEN=1` once and inspect the diff.

- [ ] **Step 4: Commit**

```bash
git add internal/paddocktui/ui/
git commit -m "feat(paddock-tui): bound interactive run renders with a distinct header marker"
```

---

### Task 31: `reattach` palette command

**Files:**
- Modify: `internal/paddocktui/app/update.go`
- Modify: `internal/paddocktui/app/update_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPalette_ReattachIssuesDetectCmd(t *testing.T) {
    m := newTestModel(t)
    m.Sessions[testSessionName] = &SessionState{
        Session: pdksession.Session{Name: testSessionName},
    }
    m.Focused = testSessionName
    _, cmd := dispatchPalette(m, PaletteReattach, "")
    if cmd == nil {
        t.Fatal("reattach should fire detectBoundRunCmd")
    }
}
```

- [ ] **Step 2: Implement**

Replace the placeholder for `PaletteReattach`:

```go
case PaletteReattach:
    if m.Focused == "" {
        m.ErrBanner = errNoSessionFocused
        return m, nil
    }
    return m, detectBoundRunCmd(m.Client, m.Namespace, m.Focused)
```

- [ ] **Step 3: Run tests + commit**

```bash
go test ./internal/paddocktui/app/ -run TestPalette_Reattach -v
git add internal/paddocktui/app/
git commit -m "feat(paddock-tui): reattach palette command re-runs bound-run detection"
```

---

## Phase 5 — Wire-up + docs

### Task 32: Wire `BrokerClient` lifecycle into `cmd/tui.go`

**Files:**
- Modify: `internal/paddocktui/cmd/tui.go`

- [ ] **Step 1: Add the new flags**

In `tui.go`, add to the existing flag block:

```go
brokerService   string
brokerNamespace string
brokerPort      int
brokerSA        string
brokerCASecret  string
```

Register:

```go
cmd.Flags().StringVar(&brokerService, "broker-service", "paddock-broker", "broker Service name")
cmd.Flags().StringVar(&brokerNamespace, "broker-namespace", "paddock-system", "broker Service namespace")
cmd.Flags().IntVar(&brokerPort, "broker-port", 8443, "broker Service port")
cmd.Flags().StringVar(&brokerSA, "broker-sa", "default", "ServiceAccount whose token authenticates to the broker (mints audience=paddock-broker tokens)")
cmd.Flags().StringVar(&brokerCASecret, "broker-ca-secret", "paddock-broker-tls", "Secret in --broker-namespace holding the broker's serving CA under key ca.crt")
```

- [ ] **Step 2: Construct a `*broker.Client` in `RunE`**

Before `tea.NewProgram(...)`:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

brokerClient, err := paddockbroker.New(ctx, paddockbroker.Options{
    Service:           brokerService,
    Namespace:         brokerNamespace,
    Port:              brokerPort,
    ServiceAccount:    brokerSA,
    Source:            cfg, // existing rest.Config
    CASecretName:      brokerCASecret,
    CASecretNamespace: brokerNamespace,
})
if err != nil {
    return fmt.Errorf("connect to broker: %w", err)
}
defer brokerClient.Close()
```

Pass `brokerClient` into the `Model` struct via the existing model-construction site.

- [ ] **Step 3: Verify build**

```bash
make paddock-tui 2>&1 | tail -10
```
Expected: build clean.

- [ ] **Step 4: Commit**

```bash
git add internal/paddocktui/cmd/tui.go
git commit -m "feat(paddock-tui): wire broker.Client lifecycle into the tui command"
```

---

### Task 33: Update `claude-code-tui-quickstart.md` for one-time login

**Files:**
- Modify: `docs/guides/claude-code-tui-quickstart.md`

- [ ] **Step 1: Replace the per-run OAuth section**

Find the section that today walks the user through `CLAUDE_CODE_OAUTH_TOKEN` `UserSuppliedSecret` setup. Add a new sub-section above it:

```markdown
### One-time `claude /login`

As of Paddock vX.Y, every agent's `$HOME` lives on the workspace PVC.
That means a `claude /login` you run inside an interactive session
persists in `~/.claude/` on the workspace and survives across runs —
no `UserSuppliedSecret` plumbing required for repeat use. Use the
`UserSuppliedSecret` path only for first-run automation or for
non-interactive Batch chains where you can't drop into a TUI shell.
```

- [ ] **Step 2: Add a callout to `interactive-harnessruns.md`**

In `docs/guides/interactive-harnessruns.md`, near the top:

```markdown
> **TUI integration.** `paddock-tui` drives Interactive runs end-to-end
> via the broker endpoints described below. See
> [claude-code-tui-quickstart.md](./claude-code-tui-quickstart.md) for
> the operator walkthrough.
```

- [ ] **Step 3: Commit**

```bash
git add docs/guides/claude-code-tui-quickstart.md docs/guides/interactive-harnessruns.md
git commit -m "docs(guides): claude-code TUI quickstart for one-time login + cross-link"
```

---

### Task 34: Migration note

**Files:**
- Create or modify: `docs/internal/migrations/<latest>.md` (find the most recent file under that dir; if there's none for the upcoming version, create one)

- [ ] **Step 1: Add a section**

```markdown
## HOME-from-PVC default

As of vX.Y, the controller sets `HOME=<workspaceMount>/.home` on every
agent container, regardless of `mode`. A new `paddock-home-init` init
container ensures the directory exists.

Operator action: usually none. Existing harness images that bake
config under their original HOME no longer see those files at runtime;
seed at runtime instead, or migrate the bake step into a Workspace
seed Job.

Known limitation: workspaces shared across harnesses with different
runtime UIDs will cross-chown each other's HOME on every run. For
maximum continuity, dedicate a workspace per harness or pin all
images to the same UID.

## Slash commands moved to the command palette

`paddock-tui` no longer parses `:`-prefixed strings inside the prompt
input. Open the command palette with `:` (on an empty prompt) or
`Ctrl-K`, type the command, and press Enter. The set of commands is
unchanged; `:cancel`, `:end`, `:interactive`, `:template`,
`:status`, `:edit`, `:help`, plus the new `:reattach`.
```

- [ ] **Step 2: Commit**

```bash
git add docs/internal/migrations/
git commit -m "docs(migrations): note HOME-from-PVC default + palette move"
```

---

## Phase 6 — End-to-end

### Task 35: e2e — TUI drives an Interactive run

**Files:**
- Create: `test/e2e/interactive_tui_e2e_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
    "context"
    "encoding/json"
    "testing"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

    paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
    paddockbroker "paddock.dev/paddock/internal/paddocktui/broker"
)

// TestInteractiveTUIE2E spins up an Interactive HarnessRun in a fresh
// namespace, opens the broker stream via paddocktui's broker client,
// asserts a frame arrives, then ends the run cleanly.
func TestInteractiveTUIE2E(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
    defer cancel()

    ns := provisionNamespace(t, ctx)
    ws := provisionWorkspace(t, ctx, ns)
    tmpl := provisionInteractiveEchoTemplate(t, ctx, ns)
    grantBrokerPolicy(t, ctx, ns, tmpl.Name, []string{"runs.interact"})

    run := &paddockv1alpha1.HarnessRun{
        ObjectMeta: metav1.ObjectMeta{Name: "tui-int", Namespace: ns},
        Spec: paddockv1alpha1.HarnessRunSpec{
            TemplateRef:  paddockv1alpha1.LocalObjectReference{Name: tmpl.Name},
            WorkspaceRef: ws.Name,
            Mode:         paddockv1alpha1.HarnessRunModeInteractive,
            Prompt:       "ping",
        },
    }
    mustCreate(t, ctx, run)
    waitForPhase(t, ctx, run, paddockv1alpha1.HarnessRunPhaseRunning, 5*time.Minute)

    bc, err := paddockbroker.New(ctx, paddockbroker.Options{
        Service:           "paddock-broker",
        Namespace:         "paddock-system",
        Port:              8443,
        ServiceAccount:    "default",
        Source:            kubeRESTConfig,
        CASecretName:      "paddock-broker-tls",
        CASecretNamespace: "paddock-system",
    })
    if err != nil { t.Fatal(err) }
    defer bc.Close()

    ch, err := bc.Open(ctx, ns, run.Name)
    if err != nil { t.Fatal(err) }

    select {
    case f := <-ch:
        var body map[string]any
        _ = json.Unmarshal(f.Data, &body)
        if f.Type == "" {
            t.Errorf("expected a typed frame; got %+v", f)
        }
    case <-time.After(2 * time.Minute):
        t.Fatal("no frame within 2 minutes")
    }

    if err := bc.End(ctx, ns, run.Name, "test-cleanup"); err != nil {
        t.Fatalf("End: %v", err)
    }
    waitForPhase(t, ctx, run, paddockv1alpha1.HarnessRunPhaseCancelled, 2*time.Minute)
}
```

`provisionInteractiveEchoTemplate` extends the existing echo template
fixture to set `spec.interactive.mode: per-prompt-process` (the
echo adapter already declares that in its annotations — verify in
`images/paddock-echo/`).

`grantBrokerPolicy` is an existing helper or trivially built on top of
`mustCreate` for a `BrokerPolicy` object.

- [ ] **Step 2: Compile-only check**

```bash
go vet -tags=e2e ./test/e2e/...
```
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/interactive_tui_e2e_test.go
git commit -m "test(e2e): TUI drives an Interactive run end-to-end through the broker"
```

---

## Self-review notes

- **Spec coverage:** Every section of `2026-04-30-paddock-tui-interactive-design.md` maps to at least one task above. The "non-goals" remain non-goals (no run detail dialog task, no shell-attach task) — intentional.
- **Type consistency:** `InteractiveBinding`, `SessionMode`, `Palette*` constants, `boundRunDetectedMsg`, `interactiveFrameMsg` are referenced under the same names across all tasks that mention them.
- **Frequent commits:** Every task ends with a commit step. 35 tasks → ≥35 commits.
- **TDD:** Each implementation task starts with a failing test.

The plan is large because the spec is three interlocking changes. The phase order is deliberate: HOME-from-PVC ships first (it's decoupled from TUI work and ships value to existing Batch users immediately), then the palette, then the broker client, then the integration. A reviewer can follow phases 1–3 as foundation work and phases 4–6 as the headline feature.
