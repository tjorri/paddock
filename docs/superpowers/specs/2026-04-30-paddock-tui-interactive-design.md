# `paddock-tui` × Interactive `HarnessRun` integration

- Status: Draft
- Owner: @tjorri
- Branch: `feat/paddock-tui-interactive`
- Built on top of: `feat/paddock-tui` (the TUI MVP) and PR #87 (Interactive
  `HarnessRun` feature). This spec assumes both are present.
- Companion artifacts:
  - `2026-04-29-paddock-tui-design.md` — TUI MVP spec; §10 sketches the
    interactive future direction this spec now realises.
  - `2026-04-29-interactive-harnessrun-design.md` — broker-side endpoints,
    grants, watchdog. The wire we're plugging into.
- Successor artifact: implementation plan at
  `docs/superpowers/plans/2026-04-30-paddock-tui-interactive.md`
  (written next).

## Summary

Three changes ship as one unit:

1. **TUI integration with Interactive `HarnessRun`s.** A user in a TUI
   session arms an interactive run via a palette command, types the
   kick-off prompt, and from that point on every prompt routes to the
   broker's `POST /v1/runs/.../prompts` endpoint with events streaming
   back over the `/stream` WebSocket. Disconnecting the TUI leaves the
   run running; relaunching reattaches automatically. `:end` terminates;
   the session falls back to Batch.
2. **Command palette refactor.** The TUI's prompt input becomes pure
   prompt text — no more `:`-prefixed slash command parsing inside it.
   All control commands (`cancel`, `end`, `interactive`, `template`,
   `status`, `edit`, `help`, plus the new ones) move into a command
   palette overlay triggered by `:` (when the prompt is empty) or
   Ctrl-K. The palette is the single answer to "where do control
   gestures go now that we have multiple ways to operate on a run".
3. **`HOME`-from-PVC for *all* runs.** The controller's pod-spec
   generator now sets `HOME=<workspace.mountPath>/.home` on every
   agent container, Batch and Interactive alike, with an unconditional
   init container that ensures the directory exists with correct
   ownership. Tool installations (`~/.local/bin`, `~/.cargo`,
   `~/.claude`, `~/.cache`) and one-time logins (e.g. `claude /login`)
   persist across runs in the workspace. This is a behaviour change
   for the whole product, not opt-in — the new default going forward.

Items (1) and (2) are mostly TUI-internal. Item (3) is a controller
behaviour change with cross-cutting consequences for existing harness
images.

## Resolved questions

Brainstorming settled four questions before writing this spec.

### 1. Interactive UX shape

Per-session interactive lifecycle. The user arms the next prompt with a
palette command (`interactive`); the next submission becomes a
`mode: Interactive` `HarnessRun` with that prompt as `spec.prompt`. The
session is then bound to that run: every subsequent prompt routes via
`POST /prompts` until the run terminates (idle/detach watchdog,
`max-lifetime`, or explicit `end`). `Ctrl-C` disconnects but does not
end the run. Relaunching the TUI auto-reattaches.

### 2. `HOME`-from-PVC scope

Applies to **all** runs in **all** workspaces. Not gated on `mode`, not
template-opt-in, not workspace-opt-in. The reasoning is that the
"continuity" payoff (carry tool installs, OAuth tokens, agent caches
across runs) is a general feature; bolting it onto Interactive only
would be the work twice and miss the bigger Batch-chain payoff.

### 3. `HOME` partitioning

Single shared `HOME` at `<workspace.mountPath>/.home/`. Every harness
that mounts the workspace sees the same `HOME`. Tools that namespace
nicely (`~/.claude/`, `~/.cargo/`, `~/.local/bin/`) coexist; tools that
don't, don't. The decision is biased toward letting installs and
caches carry across harness families — a deliberate trade against
isolation.

### 4. Control vs. content separation

The existing slash-command pattern (`:cancel`, `:end`, …) inside the
prompt input mixes two semantically different inputs into one surface.
A user prompt that happens to start with `:` shouldn't be at risk of
being interpreted as a Paddock command. The fix is a command palette
overlay: prompt input becomes pure prompt text; control commands move
to the palette, triggered by `:` (palette muscle memory for the
`vi`-fingered) or Ctrl-K (palette muscle memory for the IDE-fingered).
This is a meaningful refactor of the TUI input pipeline, but it's
load-bearing for the interactive integration: with the palette in
place, type-ahead in the prompt input becomes the natural mechanism
for "queue the next prompt while a turn is in flight" — there's no
`:queue` concept to define.

## Non-goals (deferred)

- **Run detail dialog.** Pressing Enter on a focused run row in the
  main pane is reserved for a future overlay that shows full run
  metadata, status conditions, and recent events. MVP registers the
  keybinding as a no-op and documents it as future.
- **Shell attach.** The broker exposes `/v1/runs/.../shell` already;
  the TUI does not wire it in this unit. Operators continue to use
  `wscat` per the [interactive-harnessruns guide](../../guides/interactive-harnessruns.md).
- **Concurrent prompt queueing.** The prompt input holds at most one
  pending submission. If the user submits while a turn is in flight,
  the TUI shows the buffered prompt and submits it the moment the
  broker stops returning 409. There is no list of queued prompts, no
  reordering, no edit-while-queued.
- **Cross-uid `HOME` safety.** The known footgun where `claude-code`
  (uid 1000) and a hypothetical `python-agent` (uid 1001) sharing a
  workspace cross-chown each other's files is documented but not
  fixed in this unit. Single-harness workspaces are the dominant
  case; multi-harness mitigation lands when there's a concrete
  reported pain point.
- **`Phase=Idle` rendering.** The controller still leaves Interactive
  runs in `Phase=Running` between prompts (issue #89). The TUI uses
  `Status.Interactive.CurrentTurnSeq != nil` as the in-flight
  indicator. When #89 lands, the TUI's projection continues to work
  unchanged.
- **Reattach prompts.** On TUI relaunch, if the focused session has a
  live interactive run, the TUI reattaches silently. No "attach? y/N"
  prompt; the user explicitly disconnected by quitting and is
  explicitly relaunching now.

## Architecture

### TUI surfaces

Two distinct input surfaces replace the existing single prompt input:

#### Prompt input (modified)

- Always pure prompt text. No `:`-prefix parsing.
- `Enter` submits.
- **Type-ahead during in-flight turn.** While
  `Status.Interactive.CurrentTurnSeq != nil`, the prompt input remains
  enabled. A submitted prompt is held in a single-slot buffer; the
  TUI submits it the moment the broker stops returning 409 (which
  happens when the adapter signals turn completion). One pending
  prompt at a time; submitting a second while the first is buffered
  replaces the first. The buffer's existence is surfaced in the
  status bar ("queued: <prompt-preview>").
- An empty prompt input + `:` keypress opens the palette (preserves
  the muscle memory of users coming from the slash-command MVP). After
  the first character, `:` is just a `:`.

#### Command palette (new)

- Overlay over the main pane. Triggered by `:` (when prompt is empty)
  or `Ctrl-K` from anywhere.
- Static command list for MVP. Filtering is prefix-match on the
  command name. No fuzzy search, no shell-out, no plugins.
- Each command line: `<name> [args]`. Tab autocompletes the name;
  Enter executes; Esc dismisses.
- Commands available (the union of today's slash commands plus the
  new interactive ones):
  - `cancel` — interactive: `POST /interrupt`. Batch: controller-side
    cancel (existing behaviour).
  - `end` — Interactive only: `POST /end`. Surfaces an error in the
    status bar if the session isn't bound to an interactive run.
  - `interactive` — arms the next prompt as the kick-off for an
    interactive run.
  - `template <name>` — switches the session's `LastTemplate`
    annotation for the next run.
  - `reattach` — force a re-detect of the session's bound interactive
    run and re-open the WS stream. Used after a "5 reconnect attempts
    failed" banner.
  - `status`, `edit`, `help` — unchanged from MVP semantics, just
    rehomed.

#### Run navigation

- Tab cycles focus: sidebar → main pane → prompt input → sidebar.
- Main pane focused, ↑/↓ moves a row cursor through the run history.
- `Enter` on a focused run is a no-op in MVP. The keybinding is
  registered and documented as the entry point for the future run
  detail dialog.

### Interactive flow (state machine)

A session is in one of three meta-states:

```
[Batch]  --(palette: interactive)-->  [Armed]
[Armed]  --(submit prompt)-------->   [Bound]      (creates HR mode=Interactive)
[Bound]  --(palette: end)---------->  [Batch]      (POST /end)
[Bound]  --(run reaches terminal)-->  [Batch]      (watchdog, max-lifetime, etc.)
```

While `[Bound]`:

- Submit → `POST /prompts` (or buffer if `CurrentTurnSeq != nil`).
- `cancel` palette → `POST /interrupt`.
- WS stream is open; frames render into the bound run's body.
- Reattach detection runs continuously; if the bound run leaves
  `phase ∈ {Pending, Running, Idle}`, the session transitions back
  to `[Batch]` and a banner surfaces the terminal `reason`.

### Reattach on TUI launch

For each session in the sidebar:

1. List `HarnessRun`s in the workspace's namespace where
   `spec.workspaceRef == session.Name`.
2. Filter to `spec.mode == Interactive` AND
   `status.phase ∈ {Pending, Running, Idle}`.
3. If exactly one matches, mark the session as `[Bound]` to that run.
   Defensively: if multiple match (controller bug or race), pick the
   newest by `creationTimestamp`.
4. On focus, open the WS stream to that run.
5. On open, the existing `Status.RecentEvents` ring is folded into
   the local event buffer (with the dedupe path used today by Batch
   reattach), so the user sees recent context immediately.

### Wire-level integration

#### Broker connectivity

The TUI today talks only to the k8s API server via `controller-runtime`.
The broker `paddock-broker.paddock-system.svc:8443` isn't reachable
from outside the cluster, and it serves a cert-manager-issued cert.
Three new concerns:

- **Reachability.** TUI opens a programmatic port-forward at startup
  via `k8s.io/client-go/tools/portforward`, tunnelling
  `127.0.0.1:NNNN` → broker Pod:8443. User does not manage a
  side-terminal `kubectl port-forward`. Service name/namespace/port
  configurable via `--broker-service`, `--broker-namespace`,
  `--broker-port` flags with sensible defaults.
- **TLS.** TUI reads the broker's serving CA from the Kubernetes
  Secret backing the broker's cert (the cert-manager `Certificate`'s
  `secretName`) and pins it as the root for HTTPS+WSS. No
  `--insecure-skip-verify` path; failing to read the CA is a startup
  error.
- **Auth.** Broker validates tokens via `TokenReview` audience-pinned
  to `paddock-broker`. The TUI calls the `TokenRequest` API to mint
  an audience-pinned token for a ServiceAccount. SA selection:
  `--broker-sa <name>` flag with default `default` in the workspace's
  namespace; operator grants `runs.interact` to that SA via
  `BrokerPolicy`. Token cached for half its `expirationSeconds`,
  refreshed lazily on demand.

#### New TUI-internal package

Under `internal/paddocktui/broker/` (TUI-private; deliberately
separate from the existing `internal/brokerclient/` which is the
proxy↔broker substrate):

- `client.go` — HTTP + WebSocket client. Holds the port-forward, CA,
  and token cache. Lifecycle tied to the TUI process.
- `prompt.go`:
  - `Submit(ctx, ns, run, text) (seq int32, err error)`
  - `Interrupt(ctx, ns, run) error`
  - `End(ctx, ns, run, reason string) error`
- `stream.go`:
  - `Open(ctx, ns, run) (<-chan StreamFrame, error)` — opens the WS,
    spawns a reader goroutine, returns a frame channel. Frames pass
    through verbatim. The TUI projects frames into the existing
    `paddockv1alpha1.PaddockEvent` shape for rendering — same code
    path the existing event tail uses.

#### Bubble Tea integration

New `tea.Cmd`s (mirroring the existing one-cmd-per-event idiom that
prevents goroutine leaks):

- `submitInteractivePromptCmd(client, ns, run, text)` →
  `interactivePromptSubmittedMsg{Seq}` or `errMsg`.
- `interruptInteractiveCmd(client, ns, run)` →
  `interactiveInterruptedMsg` or `errMsg`.
- `endInteractiveCmd(client, ns, run, reason)` →
  `interactiveEndedMsg` or `errMsg`.
- `openInteractiveStreamCmd(ctx, client, ns, run)` →
  `interactiveStreamOpenedMsg{Ch}` followed by
  `nextInteractiveFrameCmd(ch)` reading one frame at a time, just
  like `nextRunEventCmd` and `nextSessionEventCmd` do today.

#### Reconnect policy

WS stream drops (network blip, broker pod restart, etc.):

- Exponential backoff: 1s → 2s → 4s → 8s, max 5 attempts.
- On successful reconnect, refresh the `HarnessRun` once and fold
  `Status.RecentEvents` into the local buffer with the existing
  dedupe — no event loss for events captured in the ring.
- After 5 failed attempts, surface a banner: "interactive stream
  disconnected; type `reattach` in the palette to retry". User
  decides whether to retry or `:end`.

### `HOME`-from-PVC controller change

#### Behaviour

The controller's pod-spec generator (`internal/controller/pod_spec.go`,
or the equivalent file in the current layout) gets one new
responsibility:

- The agent container's env gains `HOME=<workspaceMountPath>/.home`
  (default `/workspace/.home`). The default workspace mount path is
  unchanged.
- A new init container `paddock-home-init` is added unconditionally
  to every run's pod spec. Image: `busybox:latest` or a minimal
  Paddock-shipped helper. Command:

  ```sh
  mkdir -p ${HOME_DIR}
  chown ${TARGET_UID}:${TARGET_GID} ${HOME_DIR}
  chmod 0700 ${HOME_DIR}
  ```

  with `HOME_DIR`, `TARGET_UID`, `TARGET_GID` populated from the
  agent container's `securityContext.runAsUser/runAsGroup` and the
  computed HOME path. The init container itself runs as root (the
  only slot that needs `chown`); idempotent on re-runs with the same
  uid; re-chowns on uid change (the documented footgun).

- `HOME` override is applied to the **agent container only**. The
  adapter, collector, and other sidecars are unaffected. Their HOME
  remains whatever the image bakes in.

#### Pre-1.0 evolves in place

No new template field, no new workspace field, no opt-in flag. Per
CLAUDE.md and `feedback_pre_v1_evolve_in_place.md`, this is a default
behaviour change in `v1alpha1` and is documented in release notes.
Existing harness images that depend on a different `HOME` either
continue to work (because they don't write to HOME) or need to be
updated.

#### Reference adapter audit

As part of this work the reference adapters (`paddock-claude-code`,
`paddock-echo`) are audited:

- If they bake config under their original HOME, the build either
  moves it to `/etc/<adapter>/skel` (copied into HOME at runtime by
  the adapter's own startup) or relocates it.
- The end-to-end smoke test for each adapter must continue to pass.

#### Workspace seed Job interaction

The existing seed Job (`Workspace` controller) clones repos at
workspace creation time. It does not touch `.home/`. No change
required — the per-run init container handles `.home/` setup at run
time, which is also when the agent uid is known.

#### Known footguns (documented, not fixed)

- **Cross-uid HOME stomping.** Two harnesses sharing a workspace but
  running under different uids (e.g. `claude-code` uid 1000,
  `python-agent` uid 1001) will cross-chown each other's HOME on
  every run. Symptoms range from "tool can't read its own config" to
  "credentials silently exposed to wrong uid". For MVP this is
  documented in a new guide page (`docs/guides/workspaces-and-home.md`)
  and a known-limitation entry in the release notes. Realistic
  follow-up mitigations: shared gid + 0770 perms; or per-uid HOME
  subdirs (re-introducing the partition we explicitly rejected for
  the single-harness case). Out of scope here.
- **Image-baked HOME content.** Images that bake config under their
  original HOME at build time will lose visibility of those files
  once HOME is overridden. Mitigation is to seed at runtime instead.
  Documented in release notes; reference-adapter audit catches the
  ones we ship.

## Component breakdown

### Files added

```
internal/paddocktui/
  broker/
    client.go               # HTTP + WS client with port-forward + CA pin
    prompt.go               # Submit, Interrupt, End
    stream.go               # Open + frame channel
    client_test.go
    prompt_test.go
    stream_test.go
  app/
    palette.go              # Palette state, parsing, dispatch
    palette_test.go
  ui/
    palette.go              # Palette overlay rendering
    palette_test.go

docs/guides/
  workspaces-and-home.md    # operator guide for the HOME-from-PVC default
                            # and the cross-uid footgun
```

### Files modified

```
internal/paddocktui/
  app/
    model.go                # add InteractiveBinding, PendingPrompt fields
    update.go               # palette dispatch; bound-run detection;
                            # interactive submit/interrupt/end handling;
                            # WS stream message handling
    slash.go                # remove the in-prompt slash parser; the
                            # palette parser replaces it
    types.go                # SessionMode, InteractiveBinding shapes
  ui/
    mainpane.go             # render bound interactive run as one
                            # growing run instead of separate boxes;
                            # show pending-prompt buffer in status bar
    view.go                 # palette overlay layering
  cmd/
    tui.go                  # wire up the broker client lifecycle

internal/controller/
  pod_spec.go               # HOME env var + paddock-home-init init slot
  harnessrun_controller_test.go
  pod_spec_test.go

api/v1alpha1/
  (no changes — explicitly no new fields)

docs/
  superpowers/specs/        # this file
  superpowers/plans/        # plan written next
  guides/
    claude-code-tui-quickstart.md  # update: OAuth login is now one-time
    interactive-harnessruns.md     # cross-link to the TUI flow
  reference/                # CLI flag docs for --broker-* additions
```

## Threat model deltas

### TUI ↔ broker

The TUI gains an outbound TLS+WSS connection to the broker. Pinning
the cert-manager CA from the cluster Secret (rather than trusting the
system pool or `--insecure-skip-verify`-ing) prevents MITM. The
ServiceAccount token is audience-pinned to `paddock-broker` so a
leaked token from this code path is useless against any other
audience.

The TUI does not store the SA token to disk. It is held in process
memory only and refreshed via `TokenRequest` when needed.

### `HOME`-from-PVC

Three new exposures to consider:

1. **Tokens at rest.** OAuth tokens that live in `~/.claude/` now
   persist in the workspace PVC. Anyone with PVC read access (i.e.
   anyone who can mount the workspace as a different `HarnessRun`)
   sees them. This is the same trust boundary as the workspace
   itself — workspaces are already shared mutable state across runs;
   `~/.claude/` joining that pool is a deliberate continuity choice.
   Documented in `docs/security/secret-lifecycle.md` (or the closest
   existing page).
2. **Cross-uid leak.** As above. Documented limitation.
3. **Seed-job → HOME injection.** A malicious Workspace Seed could
   seed `.home/` with content that compromises the next agent. The
   seed Job already runs with user-supplied content (clone URLs,
   credentials) — the threat surface doesn't grow materially. The
   seed Job's existing scope-restriction (it runs in the workspace's
   namespace, with the user's chosen Secrets) still applies.

### TUI command palette

The palette runs in-process with the same privileges as the rest of
the TUI. No new privilege boundary. Commands that hit the broker
(`cancel`, `end`) require the SA grant they always required — no
change.

## Migration / release notes

Two user-visible changes land together:

1. **HOME from PVC.** Existing workspaces grow a `.home/` subdirectory
   the first time a run executes after upgrade. No data migration
   needed; tools that wrote to the old HOME (typically ephemeral)
   simply start writing to the new HOME. Release notes call out the
   change and the cross-uid footgun.
2. **Slash commands moved to palette.** The release notes document
   that `:cancel`, `:end`, `:template`, `:status`, `:edit`, `:help`,
   `:interactive` no longer parse from the prompt input directly.
   `:` at the start of an empty prompt opens the palette, preserving
   muscle memory.

## Testing strategy

### Unit tests

- `internal/paddocktui/app/palette_test.go` — palette state machine,
  command parsing, dispatch.
- `internal/paddocktui/broker/*_test.go` — round-trip HTTP client
  tests against a fake broker (`httptest.Server`); WS reconnect
  behaviour with synthetic close-and-reopen.
- `internal/paddocktui/app/update_test.go` — interactive lifecycle
  reducer: arm → submit → POST /prompts → frame received → render;
  cancel during turn → POST /interrupt; end → POST /end; reattach
  detection from a list of runs.
- `internal/controller/pod_spec_test.go` — HOME env var present;
  init container present with right command, securityContext, image;
  Batch and Interactive both get it.

### envtest (controller integration)

- HOME-from-PVC: existing `harnessrun_controller_test.go` cases
  updated to expect the new init container slot. Add one new case:
  Workspace + first HarnessRun under uid 1000 → second HarnessRun
  under uid 1000 → assert `.home/` ownership unchanged.

### e2e

One new spec under `test/e2e/`:

- `interactive_tui_e2e_test.go` (or extension to existing TUI e2e):
  create a workspace, kick off an Interactive HarnessRun,
  POST /prompts, verify the WS stream frame, POST /end, assert the
  run terminates with `reason: explicit`.
- `home_persistence_e2e_test.go`: create a workspace, run Batch
  HarnessRun A that writes `~/.foo`, run Batch HarnessRun B that
  reads it and verifies content. Locks the persistence guarantee.

### Manual test plan

For both the developer running locally and the next reviewer:

1. **Happy path.** Open `paddock-tui`; create a session; palette →
   `interactive`; type prompt; observe interactive run is created and
   stream renders frames inline. Submit second prompt while the first
   is still in flight; observe buffered prompt indicator; observe
   second prompt fires after first completes.
2. **Cancel turn.** Mid-stream, palette → `cancel`. Observe turn
   stops; run remains alive; submit a new prompt and observe it
   succeeds.
3. **End run.** Palette → `end`. Run terminates; session returns to
   Batch.
4. **Disconnect / reattach.** While interactive run is active, Ctrl-C
   the TUI. Wait under `detachTimeout`. Relaunch; observe focus on
   the same session re-attaches; events resume; turn-in-progress
   continues to render.
5. **Run died while disconnected.** While interactive run is active,
   Ctrl-C; wait long enough for `detachTimeout` to fire (or manually
   delete the run). Relaunch; observe banner "Last interactive run
   ended (`detach`)"; next prompt creates a new Batch run.
6. **HOME persistence.** In a Batch session, run prompt that writes
   `~/.foo`. Run another prompt that reads it. Observe the file
   persists across runs.
7. **HOME OAuth payoff.** In a fresh workspace, run an interactive
   `claude` session, complete `claude /login` once. Quit the TUI.
   Relaunch and start a new (Batch or Interactive) run; observe the
   OAuth token persists; no re-login needed.

## Open questions resolved during writing

None — all four brainstorming questions were answered before the
spec started.

## Out of scope (recap)

- Run detail dialog (Enter on focused run row)
- TUI-driven shell attach
- Multi-prompt local queue
- Cross-uid HOME safety
- `Phase=Idle` controller transition (tracked by #89)
- Reattach confirmation prompt

These all earn their own follow-up units when the demand surfaces.
