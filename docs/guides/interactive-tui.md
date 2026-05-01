# Driving Interactive HarnessRuns from `paddock-tui`

How to use the TUI to start, stream, cancel, end, and reattach Interactive
HarnessRuns. Pairs with [interactive-harnessruns.md](./interactive-harnessruns.md),
which covers the same flow at the broker-endpoint level (curl/wscat).

## When to use this guide

- You already have an Interactive-mode HarnessTemplate and a matching
  policy granting `runs.interact` (see
  [interactive-harnessruns.md](./interactive-harnessruns.md) for the
  cluster-side setup).
- You want to drive that template from the TUI rather than calling the
  broker directly.

If you're still onboarding with Claude Code, read
[claude-code-tui-quickstart.md](./claude-code-tui-quickstart.md) first —
it gets you to a working session against the OAuth-backed template, then
come back here for the interactive flow.

## Mental model

A TUI session is in one of three states:

- **Batch** (default). Each prompt creates a one-shot `HarnessRun`. The
  pod terminates when the prompt is answered.
- **Armed**. The user has run the `interactive` palette command. The next
  submitted prompt becomes the kick-off for an Interactive run.
- **Bound**. The session is attached to a live Interactive `HarnessRun`.
  Subsequent prompts route to the broker's `/prompts` endpoint instead
  of creating new HarnessRuns. Disconnecting the TUI does not end the
  run.

`end` (palette) terminates a Bound session and returns it to Batch.

## Prerequisites

- A namespace with a `HarnessTemplate` where `spec.interactive.mode` is
  set (`per-prompt-process` or `persistent-process`).
- A `BrokerPolicy` granting `runs.interact: true` for that template.
- The `paddock-tui` binary on your PATH.
- Reachability to the broker. By default `paddock-tui` opens a
  programmatic port-forward to the `paddock-broker` Service in
  `paddock-system`; flags below override this if your install is
  non-standard.
  - `--broker-service` (default `paddock-broker`)
  - `--broker-namespace` (default `paddock-system`)
  - `--broker-port` (default `8443`)
  - `--broker-sa` (default `default`) — the ServiceAccount whose token
    authenticates to the broker; needs `runs.interact` granted via a
    `BrokerPolicy` matching the template.
  - `--broker-ca-secret` (default `paddock-broker-tls`) — Secret in
    `--broker-namespace` holding the broker's serving CA under key
    `ca.crt`.

If the broker can't be reached at startup, the TUI exits with a non-zero
code and a clear error before the screen draws.

## The flow

### 1. Open or create a session

Launch the TUI: `paddock-tui` (it picks up your current kubeconfig
context and namespace).

In the sidebar, either pick an existing session row with `↑`/`↓` and
`Enter`, or press `n` to open the new-session modal. Pick a template
that supports interactive mode.

### 2. Open the command palette

Two ways:

- Press `:` while the prompt input is empty. (`:` inside a non-empty
  prompt is a literal character — type it as part of your sentence and
  it stays.)
- Press `Ctrl-K` from anywhere in the TUI.

A small overlay appears at the bottom listing matching commands. Type
to filter; `Tab` autocompletes when there's a unique prefix; `Esc`
dismisses without executing.

### 3. Arm the session

Type `interactive` into the palette and press `Enter`. The palette
closes; the session is now **armed**. Nothing else changes visibly —
the next prompt you submit becomes the kickoff.

If the session is already bound to a live interactive run, `interactive`
errors with "session already bound to an interactive run". Run `end`
first.

### 4. Submit the kickoff prompt

Type the kickoff prompt into the prompt input as usual. Press `Enter`.
The TUI:

1. Creates a `HarnessRun` with `mode: Interactive` and your text as
   `spec.prompt`.
2. Marks the session **bound** to that run.
3. Opens the broker's `/stream` WebSocket against it.
4. Folds the existing `Status.RecentEvents` ring into the local event
   buffer (with dedupe) so any frames already emitted before the WS
   opens are visible.

The run renders in the main pane with a distinct heavy header so you
can tell at a glance which row is the live interactive dialog:

```
╭═ <run-name> · 14:22:11 ═════════
│ > Read the workspace, list…
│ • read /workspace
│ • read /workspace/README.md
│ I see three top-level files…
```

(Bound runs use `╭═ ═` markers; unbound Batch runs use `╭─ ─`.)

### 5. Send subsequent prompts

While bound, every prompt you submit routes to the broker's
`POST /v1/runs/{ns}/{run}/prompts` endpoint instead of creating a new
HarnessRun. Frames stream back over the WebSocket and append to the
same run's body.

**Type-ahead while a turn is in flight.** The broker rejects a second
`/prompts` call with 409 while the current turn is processing. The TUI
handles this transparently with a single-slot buffer:

- Submit a prompt while a turn is in flight → it goes into a `pending`
  buffer, surfaced as `queued: <preview>` in the status footer at the
  bottom of the screen.
- Submit a third while one is pending → the buffered prompt is
  **replaced**, not appended. Only one prompt sits in the buffer at any
  time.
- The buffered prompt fires the moment the broker stops 409'ing (i.e.
  when the previous turn's `currentTurnSeq` clears).

This is intentionally not a multi-prompt queue — interactive sessions
are dialogues, not batch jobs.

### 6. Cancel a turn

In the palette, run `cancel`. The TUI calls `POST /interrupt`, which
signals the adapter to stop the in-flight turn. The run stays alive;
the session remains bound. Submit another prompt to continue.

`cancel` with no turn in flight surfaces a status banner: "nothing to
interrupt".

### 7. End the run

In the palette, run `end`. The TUI calls `POST /end`; the controller
terminates the run cleanly; the session unbinds. The next prompt is a
fresh Batch run again.

`end` outside a bound session surfaces an error banner.

### 8. Disconnect

Press `Ctrl-C` (or `q` outside the prompt input) to quit the TUI. The
HarnessRun keeps running on the cluster as long as a client could
reasonably come back — by default the watchdog tolerates 5 minutes
without an attached client (`detachTimeout` on the template). Idle
clients beyond `idleTimeout` (default 30m) get cut off; absolute
`maxLifetime` (default 24h) caps a runaway dialog.

See
[interactive-harnessruns.md §Lifecycle and timeouts](./interactive-harnessruns.md#lifecycle-and-timeouts)
for the full state machine.

### 9. Reattach

Re-launch `paddock-tui` from any terminal. On startup, for each session
in the sidebar the TUI lists `HarnessRun`s in the workspace's namespace
and looks for one that is `mode: Interactive` and in `Pending`,
`Running`, or `Idle`. If found, the session is automatically marked
bound; on focus the WebSocket reopens.

Manual retrigger: run `reattach` in the palette. Useful if the
WebSocket dropped after the 5-attempt exponential-backoff reconnect
(1s/2s/4s/8s) gave up — surface a banner, retry from the palette.

### 10. The run terminated while you were away

If the watchdog terminated the run while the TUI was disconnected —
`idle`, `detach`, or `max-lifetime` — the next launch shows the run in
the run list with its terminal phase and surfaces a non-error banner:

```
interactive run ended (detach) — next prompt creates a Batch run
```

The session is back in Batch mode. Any prompt buffered before
disconnect is cleared.

## Run navigation

`Tab` cycles focus across three areas: prompt input → sidebar → main
pane.

With the main pane focused, `↑`/`↓` move a row cursor through the run
history. `Enter` on a focused row is reserved for a future run-detail
dialog (no-op today).

## Multi-session

Each TUI session has its own bound state. You can have one session
running an Interactive Claude run while another runs a Batch echo —
they don't share workspaces, palette state, or buffers. Switch with
the sidebar; everything stays where you left it.

## Troubleshooting

**The TUI says "interactive stream disconnected; type `reattach` in the
palette to retry".** The WebSocket lost connection (broker pod restart,
network blip, etc.) and the 5-attempt backoff couldn't reconnect. Run
`reattach` to retry. If it keeps failing, check the broker pod and the
per-run NetworkPolicy.

**The TUI exits at startup with `connect to broker: …`.** The
port-forward couldn't reach the broker. Check `--broker-service` /
`--broker-namespace` resolve to a running paddock-broker Pod, and that
the SA in `--broker-sa` exists and has `runs.interact` granted via a
policy that matches the template.

**The kickoff prompt errors with "no policy granting runs.interact".**
The template supports interactive mode (`spec.interactive.mode` is
set), but no `BrokerPolicy` in the namespace grants
`runs.interact: true` for it. Add the grant — see
[interactive-harnessruns.md §Grant `runs.interact`](./interactive-harnessruns.md#grant-runsinteract).

**`interactive` errors with "session already bound to an interactive
run".** The session has an unterminated bound run. Run `end` first; the
session returns to Batch and the next `interactive` re-arms cleanly.

## What's not in this guide

- **The `runs.shell` capability.** The broker exposes `/shell`, but the
  TUI does not wire it in this release. Use `wscat` per
  [interactive-harnessruns.md §Open a shell](./interactive-harnessruns.md#open-a-shell)
  for now.
- **Run-detail dialog.** `Enter` on a focused run row is registered as
  the future entry point; today it is a no-op.
- **Template setup.** Covered in
  [interactive-harnessruns.md](./interactive-harnessruns.md) and
  [claude-code-tui-quickstart.md](./claude-code-tui-quickstart.md).

## Related reading

- [interactive-harnessruns.md](./interactive-harnessruns.md) — the
  broker-side flow (prompt/stream/interrupt/end/shell endpoints,
  lifecycle and timeouts, audit events).
- [claude-code-tui-quickstart.md](./claude-code-tui-quickstart.md) —
  first-time-user walkthrough that gets a Claude Code template + policy
  live, then drops into the TUI.
- [workspaces-and-home.md](./workspaces-and-home.md) — the
  HOME-from-PVC default that lets `claude /login` survive across runs.
- [`../superpowers/specs/2026-04-30-paddock-tui-interactive-design.md`](../superpowers/specs/2026-04-30-paddock-tui-interactive-design.md)
  — the architectural decisions behind the palette and the lifecycle
  integration.
