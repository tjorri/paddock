# `paddock-tui`: interactive multi-session TUI for Paddock

- Status: Draft
- Owner: @tjorri
- Branch: `feat/paddock-tui`
- Successor artifact: implementation plan at `docs/superpowers/plans/2026-04-29-paddock-tui.md` (written next)

## Summary

A new standalone binary, `paddock-tui`, gives users an interactive terminal experience for working against Paddock. The TUI shows the user's sessions in a sidebar, lets them focus one at a time, accepts prompts that become individual `HarnessRuns` against the session's `Workspace`, and renders streaming `PaddockEvents` back into a run-bracketed transcript. Sessions outlive the TUI process; a user can quit and reattach later from any terminal.

A "session" in this design is **just a long-lived `Workspace` carrying a `paddock.dev/session=true` label** — there is no new CRD, no controller change, and no broker change. The MVP is purely a new client-side binary plus three labels/annotations on `Workspace`. Each user prompt becomes one fire-and-forget `HarnessRun`; the TUI synthesizes per-run boundaries client-side from `HarnessRun` status transitions.

The binary is structured to be **easily liftable into a separate repository later**: it lives in its own `cmd/paddock-tui/` entrypoint and a self-contained `internal/paddocktui/` package whose only Paddock-internal import is `api/v1alpha1`. Lift-out cost stays at "filter-repo two paths, create new module."

The spec also sketches the **minimum HarnessRun extension** that would be needed to support a future single-Pod, multi-prompt, attach/detach interactive run mode, so the MVP doesn't paint that future into a corner.

## Resolved open questions

Brainstorming settled five questions before writing this spec:

### 1. Conversation continuity across prompts: working-tree only

A second prompt in the same session does **not** see a previous prompt's reasoning. Each `HarnessRun` starts a fresh agent process; the only continuity is files on the PVC. Adapter-mediated transcript resume (e.g. `claude --resume`) is intentionally deferred — it becomes natural when the persistent-`HarnessRun` design lands and there is one long-lived agent process, and is not worth the per-run resume complexity in the meantime.

### 2. UI shape: full TUI, not REPL

The user-facing shape is a Bubble Tea–based full-screen TUI with a sidebar of sessions and a focused main pane, **not** a per-session attached REPL. Single-pane-of-glass switching between concurrent sessions was a stated requirement and is the dominant UX driver. The lighter "manager + per-session attach" alternative was considered but rejected once the multi-session view was the target.

### 3. Session data model: labeled Workspace

A session is a `Workspace` with `metadata.labels[paddock.dev/session]: "true"`. No new CRD; no spec field; reuses every `Workspace` lifecycle behavior (PVC creation, `activeRunRef` serialization, `lastActivity` and `totalRuns` accounting, drain on delete). The label keeps the TUI's session list clean — it filters out `Workspaces` created by CI or one-off flows that the user doesn't think of as "sessions."

### 4. In-flight prompt UX: client-side queue with `:cancel`

While a `HarnessRun` is in flight, the TUI's prompt input still accepts new prompts and queues them in process; as soon as `Workspace.status.activeRunRef` clears, the next queued prompt is submitted. The queue is intentionally not durable — quitting the TUI loses queued prompts. Durable queueing would need cluster state and is not worth the complexity for human typing speeds. `Ctrl-X` (or a `:cancel` slash command) interrupts the in-flight run.

### 5. Persistent-HarnessRun scope: future-direction sketch only

The MVP designs and ships the per-prompt-`HarnessRun` model in detail. A future single-Pod, attach/detach, multi-prompt mode is sketched in §10 with enough specificity to identify the **minimum HarnessRun extension** needed (a `spec.mode` field, an `Idle` phase, an `idleTimeoutSeconds`, an `Attached` condition) without resolving the connection-mechanism question — the latter has too many security tradeoffs to settle without real MVP usage informing the model.

## Non-goals

- New CRD or new CRD field. Sessions are `Workspaces` carrying a label and two annotations.
- Any controller, broker, proxy, adapter, or webhook change. The MVP is purely a new client binary.
- Conversation transcript continuity across `HarnessRuns` (deferred — see §10).
- A persistent / long-lived / attach-detach `HarnessRun` mode (sketched in §10, not built).
- Multi-user concurrent attach to the same session with collaborative editing semantics.
- Idle GC of stale sessions. Sessions live until the user explicitly ends them.
- Archiving the PVC of an ended session (`--keep-pvc`). Deferred.
- e2e test coverage. Defer to a follow-up; MVP relies on unit + snapshot tests.
- Replacement of any existing `kubectl paddock` subcommand. `paddock-tui` is additive.

## Architecture

### 1. Vocabulary and data model

A **session** is a `Workspace` resource in the user's namespace carrying:

| Where | Key | Value | Purpose |
|---|---|---|---|
| `metadata.labels` | `paddock.dev/session` | `"true"` | Sidebar filter; identifies user-facing sessions. |
| `metadata.annotations` | `paddock.dev/session-default-template` | `<HarnessTemplate name>` | Pre-fills the prompt input's target template; chosen at session creation. |
| `metadata.annotations` | `paddock.dev/session-last-template` | `<HarnessTemplate name>` | Last template actually used (may differ from default after the user overrides via the slash command); persisted so reattach restores the correct target. |

These are all the cluster-side state the MVP introduces. `paddock.dev/preserve-pvc` is deliberately *not* in the MVP; archival-on-end is a deferred feature.

### 2. Run lifecycle within a session

Each user prompt is one `HarnessRun` against the session's `Workspace`:

1. TUI creates a `HarnessRun` with `spec.workspaceRef = <session name>`, `spec.templateRef = <session-last-template>`, and `spec.prompt = <user input>`.
2. Controller admits + reconciles as today; `Workspace.status.activeRunRef` advances to the new run.
3. Adapter (per the harness's existing batch contract) writes `PaddockEvents` to `/workspace/.paddock/runs/<run name>/events.jsonl` on the PVC.
4. TUI tails the events file (same mechanism as today's `kubectl paddock events --follow`), renders events into the focused session's main pane, and watches the `HarnessRun` resource for phase transitions.
5. On terminal phase, the TUI marks the run's transcript block "Succeeded / Failed / Cancelled · Ns" and dequeues the next prompt if any.
6. The next prompt waits until `activeRunRef` clears — `Workspace` already serializes this; the TUI just respects it.

No CRD, no controller, no broker change is required for any of this. The MVP is entirely client-side.

### 3. Package layout and isolation

The new code is structured for easy lift-out into a separate repository:

```
cmd/paddock-tui/
  main.go              # Cobra root; registers TUI launcher + non-TUI subcommands.

internal/paddocktui/
  app/                 # Bubble Tea program: model, update, view, messages.
  ui/                  # Lip Gloss styles + Bubbles widgets (list, viewport, textinput).
  session/             # Workspace-as-session primitives: list, create, end, watch.
  runs/                # HarnessRun create + watch helpers.
  events/              # PaddockEvents tail (events.jsonl on PVC).
  cmd/                 # Non-TUI Cobra subcommands (`session list`, `session new --no-tui`, `session end`).
```

**Import rule (load-bearing):** `internal/paddocktui/...` may import from:

- `paddock.dev/paddock/api/v1alpha1` (CRD types — already a stable public Go surface; the project's Go module path is `paddock.dev/paddock`)
- External libraries (`charmbracelet/bubbletea`, `charmbracelet/bubbles`, `charmbracelet/lipgloss`, `k8s.io/client-go`, `k8s.io/apimachinery`, `sigs.k8s.io/controller-runtime`, etc.)

It **must not** import from `internal/cli/`, `internal/broker/`, `internal/controller/`, `internal/auditing/`, `internal/policy/`, `internal/proxy/`, `internal/webhook/`, `internal/brokerclient/`, or any other Paddock-internal package. If functionality from those packages turns out to be useful, copy what's needed into `internal/paddocktui/` rather than depending on them — the small duplication cost is the price of clean lift-out.

A lift-out in the future is then mechanically:

1. `git filter-repo --path cmd/paddock-tui --path internal/paddocktui` into a new repo.
2. Rename `internal/paddocktui/` → `paddocktui/` (so external consumers can import it if useful).
3. Add a top-level `go.mod` declaring direct dependence on a published `paddock` Go module for the API types.

The release pipeline grows one new binary artifact (`paddock-tui-<os>-<arch>`); the Helm chart is unaffected (the TUI is a client tool, not a deployable workload).

### 4. Cobra command surface

`paddock-tui` is the binary; its subcommand tree:

| Command | Behavior |
|---|---|
| `paddock-tui` (no args) | Launches the TUI focused on the namespace from the user's kubeconfig (or `--namespace`). |
| `paddock-tui session` | Alias for the no-args form (kept for muscle memory and `paddock-tui session ...` symmetry). |
| `paddock-tui session list` | Non-TUI: prints sessions as a table (NAME, TEMPLATE-DEFAULT, AGE, LAST-ACTIVITY, ACTIVE-RUN, RUNS-TOTAL). For scripts and piping. |
| `paddock-tui session new --name N --template T [--seed-repo R] [--no-tui]` | Non-TUI: creates a labeled `Workspace`. Without `--no-tui`, opens the TUI focused on the new session. |
| `paddock-tui session end NAME` | Non-TUI: deletes the labeled `Workspace`, with an interactive confirmation prompt (or `--yes` to skip). |
| `paddock-tui version` | Prints the binary version. |

`kubectl paddock submit`, `events`, `logs`, `cancel`, `status`, `list`, `policy`, `audit`, `describe`, and `version` all stay in `kubectl-paddock` and are unaffected. The two binaries are independent and can be installed separately.

### 5. TUI layout

The TUI is a single full-screen view with a sidebar (left, fixed ~25 columns) and a main pane (right, fills remaining space):

```
┌─ paddock-tui ─────────────────────────────────────────── ns: default ─┐
│ Sessions (3)                  │ starlight-7 · claude-code             │
├───────────────────────────────┤                                       │
│ ▸ starlight-7    [running]    │ ╭─ hr-starlight-7-001 · 14:22:11 ────╮│
│   moonbeam-3     [idle  3m]   │ │ > add a CHANGELOG entry for the    ││
│ ! thunderbird-2  [failed]     │ │   new session command              ││
│                               │ │                                    ││
│   [+ new session]             │ │ • read CHANGELOG.md                ││
│                               │ │ • edited CHANGELOG.md              ││
│ ─────────                     │ │ Added an entry under [Unreleased]  ││
│ Active runs: 1                │ │ describing `paddock session`.      ││
│ Total runs: 47                │ ╰─ Succeeded · 47s ──────────────────╯│
│                               │                                       │
│                               │ > _                                   │
├───────────────────────────────┴───────────────────────────────────────┤
│ ↑↓ select · Enter focus · n new · e end · / search · q quit · ? help  │
└───────────────────────────────────────────────────────────────────────┘
```

#### Sidebar

- Sorted by `Workspace.status.lastActivity` desc.
- Status glyphs: `▸` running (active `HarnessRun`), `·` idle, `!` last `HarnessRun` reached `Failed`. The focused row is indicated by selection styling, not a glyph.
- A sticky `[+ new session]` entry — always visible as the last item, doesn't scroll out — opens the new-session modal.
- Footer holds aggregate counters: active runs across all sessions; sum of `Workspace.status.totalRuns` across all visible sessions.

#### Main pane

- Header: `<session name> · <session-last-template>`.
- Run timeline: each `HarnessRun` rendered as a bracketed block. Header: run name, `started-at`, prompt (truncated to one line; full prompt on hover/expand). Body: `PaddockEvents` pretty-rendered (tool calls as `• <verb> <target>`, assistant text as wrapped paragraphs, errors highlighted). Footer: terminal phase, duration.
- New runs append at the bottom; viewport auto-scrolls until the user scrolls up, then locks until they scroll back. A small footer indicator ("3 new events ↓") notifies of new content while scrolled away.
- Prompt input at the bottom: single-line by default; `Ctrl-J` inserts a literal newline for multi-line composition; `Ctrl-E` opens `$EDITOR` for longer prompts.

#### Modal dialogs

- **New session** (`n` or selecting `[+ new session]`): wizard with template picker (lists `HarnessTemplates` with one-line descriptions from `metadata.annotations[paddock.dev/description]` if present, else the image), name input (auto-suggests a fun name if blank), optional seed-repo URL, optional storage size (default from template). Submit / Cancel.
- **End session** (`e` on the selected sidebar item): confirmation. The MVP does **not** offer the "keep PVC" option — see §11.
- **Help** (`?`): full keybinding list, dismissable with `Esc` or `?`.

### 6. Key bindings

| Key | Context | Action |
|---|---|---|
| `↑`/`↓` or `j`/`k` | Sidebar focus | Move sidebar selection. |
| `Enter` | Sidebar focus | Focus the selected session in the main pane. |
| `Tab` | Anywhere | Cycle focus: sidebar → prompt input → main pane scroll → sidebar. |
| `Ctrl-N` or `n` | Sidebar focus | Open new-session modal. |
| `e` | Sidebar focus | Open end-session modal for the selected session. |
| `/` | Sidebar focus | Filter sidebar by substring. |
| `Esc` | Modal open | Close the modal. |
| `Esc` | Prompt focus | Unfocus prompt; return focus to sidebar. |
| `Enter` | Prompt focus | Submit the prompt as a `HarnessRun`. If a run is in flight, queue it. |
| `Ctrl-J` | Prompt focus | Insert a literal newline (multi-line composition). |
| `Ctrl-E` | Prompt focus | Open `$EDITOR` for the prompt buffer. |
| `Ctrl-X` | Prompt focus or main pane focus | Cancel the focused session's in-flight `HarnessRun`. |
| `Ctrl-Q` | Anywhere | Open queue popover; each queued prompt cancellable with `q` while popover is open. |
| `Ctrl-R` | Main pane focus | Toggle raw-JSONL view of events. |
| `?` | Anywhere | Open help. |
| `q` | Sidebar focus or main pane focus | Quit the TUI. Sessions and in-flight runs persist; local queue is lost. |

### 7. Slash commands inside the prompt input

A small set of `:`-prefixed commands inside the prompt input are interpreted by the TUI rather than submitted as prompts:

| Command | Action |
|---|---|
| `:cancel` | Cancel the in-flight run for the focused session (alias for `Ctrl-X`). |
| `:queue` | Open the queue popover (alias for `Ctrl-Q`). |
| `:edit` | Open `$EDITOR` for the prompt buffer (alias for `Ctrl-E`). |
| `:status` | Print a compact session summary inline in the main pane. |
| `:template T` | Change the session's `paddock.dev/session-last-template` annotation; future prompts in this session target template `T`. |
| `:interactive` | **Stub in MVP.** Prints "interactive mode is not yet implemented" with a link to the persistent-`HarnessRun` design when it lands. The keybinding is reserved so the future feature has a stable invocation point. |
| `:help` | Open help. |

The `:` prefix avoids colliding with prompts that start with `/` (which is common in code-related instructions, e.g. `/etc/passwd`).

### 8. Per-session state

Held in TUI memory:

- The list of recent `HarnessRuns` (last N, newest first; older runs lazy-loaded if the user scrolls into history).
- For the **focused** session only: the tail of the most recent run's `events.jsonl`.
- The local prompt queue (in-process; lost on quit, by design).

Non-focused sessions keep only a status summary (last run phase, active-run name, `lastActivity`, `totalRuns`) — sourced from the `Workspace` watch and a lightweight `HarnessRun` list watch. We do not tail event files for unfocused sessions; switching focus loads the latest run's events on demand.

## 9. Error handling and edge cases

| Situation | Behavior |
|---|---|
| `Workspace` stuck in `Seeding` or `Failed`. | Sidebar surfaces the phase and condition message inline. Selecting the session shows the failed condition in the main pane instead of the prompt input. End-session works as normal; the `Workspace` controller drains as today. |
| `HarnessRun` creation rejected (admission policy, template not found, credential not granted). | TUI catches the API error, surfaces it inline near the prompt input ("Run rejected: <message>"). Prompt buffer is preserved so the user can edit and retry. |
| Apiserver disconnect / watch error. | Watcher reconnects with exponential backoff. TUI shows a banner ("reconnecting..."). The local queue is preserved across reconnect — only TUI exit drops it. |
| Concurrent TUI processes attached to the same `Workspace`. | Both see the same authoritative state from the apiserver. Each has its own local queue. Last-writer-wins on the `session-last-template` annotation patch — accepted because human-driven update rate is far below conflict-likely. |
| User scrolls up while a run is producing events. | Auto-scroll locks; a footer indicator ("N new events ↓") signals pending content. Pressing `End` or scrolling to the bottom resumes auto-scroll. |
| `events.jsonl` doesn't exist yet (run hasn't started writing). | Tail polls until the file appears or the run reaches a terminal phase, whichever comes first. No spurious error to the user. |
| User quits with queued prompts pending. | TUI prints, on stdout after teardown, a one-line warning naming the dropped prompts. No cluster state was created for them. |
| `session new` with an explicit name that's already taken. | API call returns `AlreadyExists`; modal surfaces "name in use" inline; user edits and retries. |

## 10. Future direction: persistent `HarnessRun` (sketch only)

The persistent-`HarnessRun` mode — one Pod, multi-prompt, attach/detach, durable agent process — is **out of scope to implement** in this spec. It is sketched here so the MVP doesn't paint us into a corner.

### 10.1 Minimal `HarnessRun` extension

The smallest `HarnessRun` shape that supports it:

- `HarnessRun.spec.mode: Batch | Interactive` (default `Batch`, today's behavior). `Interactive` signals the controller and adapter to run a long-lived agent process and accept stdin streams.
- `HarnessRun.spec.idleTimeoutSeconds` — auto-terminate after no input or activity for N seconds. Required to prevent resource leaks from forgotten interactive runs.
- `HarnessRun.status.phase` gains an `Idle` phase: agent is alive, waiting for input. Distinct from `Running` (currently processing a turn).
- `HarnessRun.status.conditions` gains:
  - `Attached` — at least one client connected.
  - `IdleSince` — timestamp the agent last finished a turn.

Pre-1.0 evolve-in-place: these go directly into `v1alpha1`; no version bump.

### 10.2 Adapter responsibility

The harness adapter (`adapter-claude-code` or equivalent) gains a stdin-pump role: in `Interactive` mode it watches a per-run prompt-input channel (a named pipe on the PVC, or a UDS the proxy mediates) and feeds new user prompts to the agent's stdin. The existing event-tail responsibility is unchanged. `PaddockEvents` already include enough turn-boundary information that the TUI can render per-prompt boundaries without a new event type.

### 10.3 Connection mechanism (open question — to be resolved in its own brainstorm)

How `paddock-tui` streams stdin to the adapter and reads the live event channel back has three plausible answers, none yet evaluated:

1. **`kubectl exec`-style stream via the Kubernetes API.** Cheapest; bypasses Paddock's auth and audit model.
2. **Broker-mediated WebSocket.** Preserves Paddock's auth / audit; the broker grows a streaming surface.
3. **Per-run `Service` + port-forward.** Simple in cluster, exposes a new auth surface.

Choosing among these requires real MVP usage informing the security model and is the reason this spec defers the persistent design.

### 10.4 TUI promotion path

A future `:interactive` slash command (already reserved as a stub in the MVP) would:

1. Cancel the focused session's current `Batch` `HarnessRun` cleanly.
2. Create a new `HarnessRun` with `spec.mode: Interactive`.
3. Switch the prompt input to stream-to-stdin mode.

From the user's perspective the session is the same; only the lifetime of the underlying Pod changes. Reserving the keybinding now means we don't have to re-train muscle memory when the persistent feature ships.

### 10.5 Constraints the MVP must respect (so we don't paint ourselves in)

- Don't bake "one `HarnessRun` per prompt" into any CRD field. The TUI knows it; `HarnessRun` should not.
- Don't put TUI-only state (current focus, queue, scroll position) into `HarnessRun.status`. Run boundaries are synthesized client-side.
- Don't grow new "must reach a terminal phase before the next prompt" assumptions in any controller code we touch — the queue already lives client-side.

## 11. Open questions (deliberately deferred)

These are intentionally unresolved in this spec and can be addressed when real usage tells us they matter:

1. **`--keep-pvc` on session end.** Annotation-driven PVC retention is plausible (`paddock.dev/preserve-pvc=true`) but requires controller plumbing. Not in MVP. Add when there's a stated archival workflow.
2. **Idle GC.** Stale sessions persist indefinitely. A `--max-idle-days` controller flag or per-session annotation could auto-cleanup. Not in MVP; document in user docs that session cleanup is the user's responsibility.
3. **Multi-namespace sessions.** MVP scopes the sidebar to a single namespace (from kubeconfig or `--namespace`). An `--all-namespaces` mode is plausible but adds RBAC complexity; punt.
4. **Conversation continuity (transcript resume).** Out of MVP per Resolved Question 1; revisit alongside the persistent-`HarnessRun` design where it falls out naturally.
5. **Per-session credential overrides.** MVP inherits the template's credential bindings unchanged. A future modal field could let the user select credential alternatives at session-creation time.
6. **Persistent-`HarnessRun` connection mechanism.** §10.3 above.

## 12. Testing strategy

- **Unit tests** in `internal/paddocktui/app/` cover the Bubble Tea reducer with synthetic message streams: state transitions for prompt submission, queue behavior on `activeRunRef` clear, modal open/close, focus changes. These are pure-Go tests; no terminal needed.
- **Snapshot tests** (golden-file) of the View function across a representative set of model states (empty, single session, multiple sessions with mixed phases, run in flight, run failed, modal open). Catches rendering regressions without requiring an interactive terminal in CI.
- **Helper-package tests** for `internal/paddocktui/session/`, `runs/`, `events/`: against fake apiserver clients, exercise list / create / watch / tail behavior. Pure-Go, fast.
- **No e2e tests** in MVP. Deferred — once the data plane stabilizes, a small Ginkgo spec can drive the underlying API operations end-to-end against `kind`. Owner can decide when to invest.
- **No interactive integration tests** (i.e. driving the actual TUI rendering). Bubble Tea's design encourages testing the model and rendering separately; we follow that.

## 13. Out of scope (explicit)

- Bundling `paddock-tui` into the Helm chart. It is a client tool, distributed as a binary, installed by the user.
- Adding a `paddock-tui` install script to the project's release artifacts. The first cut releases the binary on GitHub Releases alongside the existing artifacts; install instructions live in user docs.
- Web-based or browser-based session view.
- Mobile / terminal-multiplexer-aware features beyond standard Bubble Tea behavior.
- Any change to `HarnessTemplate`, `BrokerPolicy`, `AuditEvent`, or other CRDs.
- Any change to the broker, proxy, controller, webhooks, or adapters.
