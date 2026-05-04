# Interactive adapter as proxy — design

**Status:** proposed (2026-05-02)
**Branch:** `feature/interactive-adapter-as-proxy`
**Supersedes (in part):** §2.3 of `2026-04-29-interactive-harnessrun-design.md`
**Related:** F19 in `docs/superpowers/plans/2026-04-28-quickstart-walkthrough-findings.md`

## §1. Summary

Today's Interactive `HarnessRun` runtime is structurally non-functional for any harness whose CLI is not pre-installed in the adapter sidecar image. The adapter container is built `FROM gcr.io/distroless/static:nonroot`, has no shell and no `claude` binary, and does not mount the workspace PVC — yet `cmd/adapter-claude-code/{persistent,per_prompt}.go` both call `exec.Command("claude", ...)` directly. The agent container's `run.sh` independently performs a one-shot batch invocation regardless of `PADDOCK_INTERACTIVE_MODE`, terminating the pod before the adapter can drive subsequent prompts. The compound effect (F19 a + b) is that interactive runs report `Phase: Succeeded` with an empty event timeline.

This spec inverts the assumption that the adapter owns the harness CLI. The harness CLI subprocess moves into the agent container — which already installs it on the workspace PVC — and the adapter sidecar becomes a transparent stream-json frame proxy between the broker WebSocket and a Unix-domain-socket pair on the shared `/paddock` emptyDir volume. A new harness-agnostic Go binary `paddock-harness-supervisor` runs in the agent container and bridges the UDS pair to the harness CLI's stdio.

The redesign also produces a documented harness-author contract (`docs/contributing/harness-authoring.md`): the small set of requirements an arbitrary CLI must satisfy to participate in batch and interactive modes, and the small set of build-time changes a harness image must apply. Validated empirically against Claude Code, Codex CLI, Gemini CLI, Aider, simonw/llm, ollama, Continue CLI, and Cursor — see §9.

## §2. Resolved design questions

### Q1. Mode scope: both modes, single supervisor

`per-prompt-process` and `persistent-process` are both fixed. Empirically (§9) per-prompt-process is the mode 7 of 8 surveyed CLIs can implement; only Claude Code natively supports persistent-stdin streaming today. Both modes are implemented in one supervisor binary; mode only changes the loop shape.

### Q2. IPC mechanism: Unix-domain socket on `/paddock`

UDS chosen over FIFO pair (close-and-reopen reblock cliff complicates adapter restart) and TCP loopback (port-allocation contract for no functional gain over a filesystem path the existing volume already provides). Filesystem permissions on `/paddock/<sock>` compose with the existing fsGroup=65532 pattern on the shared volume.

The cross-pod broker→adapter hop stays TCP/HTTP on `:8431` — UDS doesn't cross pods. The two transports answer different trust questions and coexist cleanly.

### Q3. Wire format: two sockets, separated concerns

- `/paddock/agent-data.sock` — opaque bidirectional byte stream. Adapter writes broker-WS payloads in; supervisor `io.Copy`s to the harness CLI's stdin. Harness CLI stdout `io.Copy`s back out the same socket. Nothing on this socket is interpreted by Paddock.
- `/paddock/agent-ctl.sock` — newline-delimited JSON, Paddock-defined messages: `{"action":"interrupt"}`, `{"action":"end","reason":"..."}`, `{"action":"begin-prompt","seq":N}`, `{"action":"end-prompt"}`.

Keeps the harness-author surface trivial (~30 lines: bidirectional copy + small JSON-line reader) and isolates failures: a control-protocol bug doesn't break the data path. The `begin-prompt`/`end-prompt` messages are the per-prompt-process boundary delimitation; they are not used in persistent-process.

### Q4. Supervisor: shared Go binary, not shell or library

`paddock-harness-supervisor` lives at `cmd/harness-supervisor/`, ships in a tiny scratch image, and is `COPY`-ed into each interactive-capable harness Dockerfile. `run.sh` `exec`s it after the install step on `PADDOCK_INTERACTIVE_MODE` being non-empty. Authors don't write Go, don't write socat, don't write JSON-line parsers — they pick which modes their CLI supports and supply env-var-shaped configuration.

## §3. Architecture

```
┌───────────────────┐  HTTP+WS    ┌──────────────────────────────────────────┐
│ paddock-broker    │  on :8431   │ Run pod                                  │
│ (other namespace) │ ──────────► │                                          │
└───────────────────┘             │  ┌─ adapter container ─────────────┐    │
                                  │  │ (paddock-adapter-<harness>)      │    │
                                  │  │ HTTP /prompts /interrupt /end    │    │
                                  │  │ WS   /stream                     │    │
                                  │  │  - validates, frames, audits     │    │
                                  │  │  - dials two UDS                 │    │
                                  │  │  - tails data UDS for events.jsonl
                                  │  └────┬──────────────────────┬──────┘    │
                                  │       │                      │           │
                                  │  /paddock (shared emptyDir) volume       │
                                  │       │                      │           │
                                  │  agent-data.sock      agent-ctl.sock     │
                                  │       │                      │           │
                                  │  ┌────▼──────────────────────▼──────┐    │
                                  │  │ agent container                  │    │
                                  │  │ (paddock-<harness>)              │    │
                                  │  │  run.sh: install + exec /pks    │    │
                                  │  │  /pks = paddock-harness-supervisor   │
                                  │  │   - listens on both UDS          │    │
                                  │  │   - spawns harness CLI per mode  │    │
                                  │  │   - bridges stdio ↔ data UDS     │    │
                                  │  │   - JSON-line ctl reader → SIG   │    │
                                  │  └──────────────────────────────────┘    │
                                  │                                          │
                                  │  ┌─ collector container ───────────┐    │
                                  │  │ - reads events.jsonl + raw      │    │
                                  │  │ - mirrors to workspace PVC      │    │
                                  │  └──────────────────────────────────┘    │
                                  └──────────────────────────────────────────┘
```

Three containers, three trust tiers, three transports:

| Hop | Transport | Trust boundary |
|-----|-----------|---|
| broker → adapter | TCP/HTTP+WS on pod-IP:8431 | NetworkPolicy ingress restricts to broker pod label set |
| adapter → agent (data) | UDS at `/paddock/agent-data.sock` | filesystem permissions + same-pod trust domain |
| adapter → agent (ctl) | UDS at `/paddock/agent-ctl.sock` | as above |

The adapter is a **thin policy/auth/framing layer**. It validates request shapes, performs audit emission, frames stream-json with `_paddock_seq`, and runs the events-jsonl translation. It does not run any harness-specific code beyond a per-harness `convert.go` line-translator (which is the only file that needs to be replaced when adding a new harness).

The agent is the **untrusted computation tier**. The harness CLI runs here, with workspace mounted, with `HOME` on the workspace PVC, with proxy-mediated egress, with whatever credentials the broker has injected via `extraEnv`. The supervisor running in the same container is small, well-tested, and does no privileged work.

## §4. Components

### 4.1 Adapter (refactored)

`cmd/adapter-claude-code/main.go` becomes a thin shim. The drivers `per_prompt.go` and `persistent.go` are deleted. A new harness-agnostic package `internal/adapter/proxy/` holds the proxy logic; `cmd/adapter-claude-code/main.go` instantiates it with the claude-specific `convert.go` line-translator.

The HTTP server preserves the broker's existing contract — same routes, same status codes, same body shapes:

| Route | Method | Adapter behavior |
|-------|--------|------------------|
| `/prompts` | POST | Persistent: write prompt JSON line to data UDS. Per-prompt: write `begin-prompt` ctl, then prompt bytes to data, then `end-prompt` ctl. |
| `/interrupt` | POST | Write `{"action":"interrupt"}` to ctl. |
| `/end` | POST | Write `{"action":"end","reason":"..."}` to ctl, then close data UDS write half. |
| `/stream` | GET WS | Bidirectional byte pipe between client WS and data UDS. |

The proxy package gains a backoff dial loop on startup (50ms × 5 → 1.6s × 5, capped at ~10s total) — the agent container may still be installing the harness CLI when the adapter sidecar starts. While the dial is retrying, `/prompts` / `/interrupt` / `/end` return `503 Service Unavailable`; `/stream` returns `502 Bad Gateway` if dialed during this window.

`convert.go` keeps writing PaddockEvents to `$PADDOCK_EVENTS_PATH` (today: `/paddock/events/events.jsonl`). The events go through a small `tee` so they hit both (a) the broker WS fanout, (b) the events.jsonl. The collector continues to mirror events.jsonl to the workspace as it does today — no changes to the collector.

**Why events stay adapter-side, not in the supervisor.** The supervisor's contract is "byte relay + lifecycle." Pushing per-event JSON parsing into it bloats the harness-author surface (they'd have to think about events.jsonl too) and duplicates the existing batch-mode pattern in the codebase. The adapter has the path-canonical view of `/paddock/events/events.jsonl` and is the right place for translation.

### 4.2 Supervisor (`cmd/harness-supervisor/`, new)

Single Go binary, harness-agnostic. Built into a scratch image `paddock-harness-supervisor:dev`; each harness Dockerfile does:

```dockerfile
COPY --from=ghcr.io/tjorri/paddock/harness-supervisor:<version> \
     /supervisor /usr/local/bin/paddock-harness-supervisor
```

Reads env at startup:

| Env var | Required when | Purpose |
|---------|---------------|---------|
| `PADDOCK_INTERACTIVE_MODE` | always | `per-prompt-process` \| `persistent-process` |
| `PADDOCK_AGENT_DATA_SOCKET` | always | UDS path for the data channel |
| `PADDOCK_AGENT_CTL_SOCKET` | always | UDS path for the control channel |
| `PADDOCK_HARNESS_BIN` | always | Absolute path to the harness CLI binary |
| `PADDOCK_HARNESS_ARGS` | always | Argv tail for the CLI (parsed by `shellwords` rules) |
| `PADDOCK_HARNESS_WORKDIR` | optional | Defaults to `$PADDOCK_WORKSPACE`; the CLI's `cmd.Dir` |

Behavior, per mode:

**persistent-process:**
1. `Listen` on both UDS paths.
2. `Accept` once on each (the adapter's single connection).
3. `exec.Command(PADDOCK_HARNESS_BIN, PADDOCK_HARNESS_ARGS...)` with stdin connected to the data UDS read half, stdout to data UDS write half, stderr to the supervisor's stderr (which lands in `kubectl logs`).
4. Spawn a goroutine reading newline-delimited JSON from the ctl UDS. On `interrupt`: `syscall.Kill(-pgid, SIGINT)`. On `end`: close the CLI's stdin, write `{"event":"ended"}` to ctl, wait for CLI exit, then exit the supervisor.
5. If the CLI exits unexpectedly: write `{"event":"crashed","exit_code":N}` to ctl, close data UDS, exit non-zero. Adapter sees the data-UDS EOF, closes the broker WS with status 1011 (internal error), the controller observes the failure and marks the run Failed.

**per-prompt-process:**
1. `Listen`/`Accept` as above.
2. Loop on the ctl UDS:
   - On `begin-prompt`: spawn a fresh CLI as in persistent step 3. Pipe data UDS bytes into its stdin until `end-prompt` arrives on ctl.
   - On `end-prompt`: close the CLI's stdin. Continue reading the CLI's stdout into the data UDS until the CLI exits. Loop back to await the next `begin-prompt`.
   - On `interrupt`: SIGINT to the active CLI's process group, if any.
   - On `end`: break the loop, close UDS listeners, exit 0.

**Data-UDS reader synchronization (per-prompt-process detail).** The supervisor's data-UDS read goroutine is a single persistent loop that pipes incoming bytes into the *current* CLI's stdin pipe. Between prompts (after `end-prompt` and before the next `begin-prompt`), there is no current CLI; the reader waits on a `sync.Cond` rather than dropping bytes. The adapter's UDS write blocks behind the reader if the kernel buffer fills; the broker observes this as a slow `POST /prompts` and times out via the existing `adapterForwardTimeout` (30s). Concurrent prompts on the same run are prevented upstream by the broker's `CurrentTurnSeq` guard, so the reader-vs-CLI-lifecycle mutex is one-prompt-at-a-time.

Both modes also handle SIGTERM (kubelet pod-shutdown) by closing UDS listeners gracefully, sending SIGTERM to the active CLI process, and waiting up to `terminationGracePeriodSeconds` for it to exit.

**Implementation note: stdout filtering.** The validation matrix (§9) flagged that some CLIs (Gemini's init banner, ollama's control characters) emit non-protocol bytes on stdout. The supervisor does not implement filtering — it stays a verbatim relay. Harness authors whose CLI needs filtering wrap the CLI in a shell shim:
```sh
# in $PADDOCK_HARNESS_BIN's image-built location:
#!/bin/sh
exec /usr/local/bin/gemini "$@" 2>&1 | sed '/^Welcome to Gemini/d'
```
…and point `PADDOCK_HARNESS_BIN` at the shim. Documented in `docs/contributing/harness-authoring.md` §3.4.

### 4.3 Agent container (`run.sh`)

`images/harness-claude-code/run.sh` ends with the same pattern echo already uses:

```sh
if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
  exec paddock-harness-supervisor
fi
```

Above the branch: the existing batch flow — read prompt, run CLI, write result.json, exit. The interactive branch resolves the mode-specific args before exec:

```sh
if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
  case "${PADDOCK_INTERACTIVE_MODE}" in
    persistent-process) export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PERSISTENT:?}" ;;
    per-prompt-process) export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PER_PROMPT:?}" ;;
    *) echo "unknown PADDOCK_INTERACTIVE_MODE: $PADDOCK_INTERACTIVE_MODE" >&2; exit 1 ;;
  esac
  exec paddock-harness-supervisor
fi
```

The Dockerfile gains a `COPY --from` of the supervisor binary, and ENV declarations:
```dockerfile
ENV PADDOCK_HARNESS_BIN=/root/.local/bin/claude
ENV PADDOCK_HARNESS_ARGS_PERSISTENT="--input-format stream-json --output-format stream-json --verbose"
ENV PADDOCK_HARNESS_ARGS_PER_PROMPT="--print --input-format stream-json --output-format stream-json --verbose"
```

The image declares both per-mode argv strings; `run.sh` selects one based on `PADDOCK_INTERACTIVE_MODE` and exposes it as `PADDOCK_HARNESS_ARGS` for the supervisor to consume. The controller is not involved in mode-vs-argv resolution — it just sets `PADDOCK_INTERACTIVE_MODE`.

### 4.4 Controller (`internal/controller/pod_spec.go`)

Three changes:

1. **`buildAdapterContainer`** gains two new env vars:
   ```go
   {Name: "PADDOCK_AGENT_DATA_SOCKET", Value: "/paddock/agent-data.sock"},
   {Name: "PADDOCK_AGENT_CTL_SOCKET", Value: "/paddock/agent-ctl.sock"},
   ```
   No new volume mount. **The adapter does not get the workspace PVC** — preserves the `pod_spec_test.go:184` invariant ("adapter must not see workspace") that the codebase mapper flagged.

2. **`buildEnv`** (agent container) gets the same two `PADDOCK_AGENT_*_SOCKET` env vars so the supervisor knows where to listen. The per-mode argv resolution is **`run.sh`'s job, not the controller's** (§4.3) — the controller sets `PADDOCK_INTERACTIVE_MODE`, the image declares `PADDOCK_HARNESS_ARGS_{PERSISTENT,PER_PROMPT}` at build time, and `run.sh` resolves to `PADDOCK_HARNESS_ARGS` before exec'ing the supervisor. This keeps the controller harness-agnostic.

3. **No changes to `NetworkPolicy`** — the broker still dials `:8431` on the run pod's IP; nothing about the run-pod-side network footprint changes.

### 4.5 Webhook tightening (discovered gap, opportunistic)

`internal/webhook/v1alpha1/harnessrun_webhook.go:176-181` currently only checks that `template.Spec.Interactive.Mode != ""` when `run.Spec.Mode == Interactive`. The codebase mapper flagged that this does not validate the value against the adapter image's `paddock.dev/interactive-modes` annotation, even though the original 2026-04-29 design §2.3 called for it.

This spec proposes fixing it via **template-side declaration**, not image inspection (which is hairy in admission webhooks: network, latency, registry auth). Operators who declare an interactive template add a parallel annotation:
```yaml
metadata:
  annotations:
    paddock.dev/adapter-interactive-modes: "per-prompt-process,persistent-process"
```
The webhook validates that `template.Spec.Interactive.Mode` is in this annotation's set when present; if absent, it's a soft warning (won't reject pre-existing templates). The annotation is operator-asserted, not Paddock-verified — the runtime `paddock-harness-supervisor` is the actual enforcement (it reads `PADDOCK_INTERACTIVE_MODE` and refuses to start in an unsupported mode).

## §5. Data flow

### 5.1 Persistent-process prompt path

```
TUI                  broker             adapter            data UDS         supervisor       claude
 │                     │                   │                   │                │              │
 │ WS: {"type":"user", │                   │                   │                │              │
 │  ..., _paddock_seq} │                   │                   │                │              │
 ├────────────────────►│                   │                   │                │              │
 │                     │ POST /prompts     │                   │                │              │
 │                     ├──────────────────►│                   │                │              │
 │                     │                   │ write line        │                │              │
 │                     │                   ├──────────────────►│                │              │
 │                     │                   │                   │ io.Copy        │              │
 │                     │                   │                   ├───────────────►│ stdin write  │
 │                     │                   │                   │                ├─────────────►│
 │                     │                   │                   │                │              │ ... processes ...
 │                     │                   │                   │                │ stdout read  │
 │                     │                   │                   │ io.Copy       ◄┼──────────────┤
 │                     │                   │                  ◄┤                │              │
 │                     │                   │ tee:             ◄┤                │              │
 │                     │                   │  - WS fanout     ◄┤                │              │
 │                     │                   │  - events.jsonl   │                │              │
 │                     │ WS frame relay    │                   │                │              │
 │ WS: {"type":"asst…  │◄──────────────────┤                   │                │              │
 │◄────────────────────┤                   │                   │                │              │
```

### 5.2 Per-prompt-process prompt path

Same as persistent up to the adapter, then:

```
adapter           ctl UDS         supervisor           claude (fresh process)
 │                   │                │                       │
 │ {"action":"begin- │                │                       │
 │  prompt","seq":N} │                │                       │
 ├──────────────────►│ JSON line read │                       │
 │                   ├───────────────►│                       │
 │                   │                │ exec.Command(claude,  │
 │                   │                │  --print --input-fmt  │
 │                   │                │  stream-json …)       │
 │                   │                ├──────────────────────►│
 │ prompt bytes →    │                │                       │
 │  data UDS         │                │                       │
 │ (separate path)   │                │ stdin pipe ─────────► │
 │                   │                │                       │
 │ {"action":"end-   │                │                       │
 │  prompt"}         │                │                       │
 ├──────────────────►│                │                       │
 │                   ├───────────────►│ close stdin           │
 │                   │                ├──────────────────────►│
 │                   │                │                       │ ... processes,
 │                   │                │ stdout drain          │     emits stream-json,
 │                   │                │◄──────────────────────┤     exits 0
 │ data UDS reply ←  │                │                       │
 │  bytes            │                │                       │
 │                   │                │ wait, then loop       │
 │                   │                │ awaiting next         │
 │                   │                │ begin-prompt          │
```

### 5.3 Interrupt path

```
TUI: cancel  →  broker POST /interrupt  →  adapter writes {"action":"interrupt"} to ctl UDS
                                          →  supervisor reads ctl line
                                          →  supervisor: syscall.Kill(-pgid, SIGINT)
                                          →  CLI receives SIGINT, aborts current turn
                                          (persistent-process: CLI continues running, ready for next prompt)
                                          (per-prompt-process: CLI exits, supervisor loops)
```

The broker, adapter, supervisor, and CLI all stay alive; only the in-flight turn is canceled. Matches the existing 2026-04-29 design's interrupt semantics.

### 5.4 End path

```
TUI: end    →  broker POST /end  →  adapter writes {"action":"end","reason":...} to ctl UDS
                                  →  adapter closes write half of data UDS
                                  →  supervisor reads ctl, closes CLI's stdin
                                  →  CLI drains, exits 0 (persistent) or current prompt finishes (per-prompt)
                                  →  supervisor closes UDS listeners, exits 0
                                  →  agent container exits → pod terminates
                                  →  controller observes pod termination, marks run Succeeded
                                  →  audit event records the reason
```

## §6. Lifecycle & error handling

### 6.1 Startup ordering

K8s native sidecars (the adapter has `restartPolicy=Always`, making it a native sidecar) start **before** the main agent container. So the adapter is up before the supervisor exists. The proxy package handles this with a backoff dial loop (50ms / 100 / 200 / 400 / 800 / 1.6s; capped at 5 tries × 1.6s ≈ 5s total before the adapter returns startup failure).

The adapter's HTTP listener on `:8431` opens immediately. Broker requests during the startup window get:
- `/prompts`, `/interrupt`, `/end`: `503 Service Unavailable`
- `/stream`: `502 Bad Gateway`

These are retryable on the broker side; the broker already has reconnect logic for the WS path. POST retries are the broker's concern (existing design).

### 6.2 Crash semantics

| Failure | Detection | Effect |
|---------|-----------|--------|
| Harness CLI crashes mid-prompt (persistent) | supervisor reads stdout EOF before `end` ctl | supervisor writes `{"event":"crashed"}` to ctl, closes data UDS, exits non-zero. Adapter sees data-UDS EOF, closes broker WS (status 1011). Run marked Failed. |
| Harness CLI crashes mid-prompt (per-prompt) | supervisor's CLI subprocess exits non-zero | that prompt fails; supervisor loops awaiting next `begin-prompt`. Run continues. |
| Adapter crashes | kubelet restarts the sidecar (`restartPolicy=Always`) | on restart, re-dial UDS; supervisor's `Accept` re-accepts. Broker's WS reconnect path picks up. |
| Supervisor crashes | agent container exits (no restart — main container, not sidecar) | pod terminates. Run marked Failed. Supervisor is small, well-tested, low crash risk. |
| Pod evicted / SIGTERM | supervisor's signal handler | gracefully close UDS, SIGTERM to CLI, wait for `terminationGracePeriodSeconds` (controller already configures this longer for interactive runs). |

### 6.3 Reconnect semantics

The supervisor's `Listen` + `Accept` loop on each UDS handles repeated connections, so adapter restart is non-disruptive. The control protocol is request-only (no in-flight state to recover); the data path is the harness CLI's natural stdin/stdout, which doesn't care about adapter restarts.

The TUI's broker WS reconnect is unchanged from the existing 2026-04-30-paddock-tui-interactive design.

## §7. Harness author contract

The deliverable: `docs/contributing/harness-authoring.md`. Outline:

### §7.1 Batch mode (existing, codified)

| Env var | Meaning |
|---------|---------|
| `PADDOCK_PROMPT_PATH` | File containing the prompt text |
| `PADDOCK_RAW_PATH` | File path the harness writes raw output to |
| `PADDOCK_RESULT_PATH` | Optional file path for structured result JSON |
| `PADDOCK_WORKSPACE` | Workspace mount path |
| `HOME` | Set to `$PADDOCK_WORKSPACE/.home`; tools persist across runs |

Exit semantics: 0 on success, non-zero on failure. The collector reads `events.jsonl` (translated by the adapter from `raw`) and `result.json` and reports them via the controller.

### §7.2 Interactive mode (new)

To make a harness interactive-capable, the image:

1. **Includes the supervisor binary** at `/usr/local/bin/paddock-harness-supervisor`:
   ```dockerfile
   COPY --from=ghcr.io/tjorri/paddock/harness-supervisor:<version> \
        /supervisor /usr/local/bin/paddock-harness-supervisor
   ```
2. **Sets the harness invocation env vars** in the Dockerfile:
   ```dockerfile
   ENV PADDOCK_HARNESS_BIN=/path/to/cli
   ENV PADDOCK_HARNESS_ARGS_PERSISTENT="..."   # if persistent-process supported
   ENV PADDOCK_HARNESS_ARGS_PER_PROMPT="..."   # if per-prompt-process supported
   ```
3. **Branches `run.sh`** at the end:
   ```sh
   if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
     exec paddock-harness-supervisor
   fi
   ```
4. **Declares supported modes** in the template's `spec.interactive.mode` enum and the recommended `paddock.dev/adapter-interactive-modes` annotation (§4.5).

### §7.3 Harness CLI requirements

The CLI must:

- **Read prompts from stdin** in any line-delimited stream the supervisor can pipe byte-for-byte. (Stream-json, NDJSON, plain text — the supervisor doesn't interpret. The broker must be sending what the CLI expects, which is the harness-image author's contract with the broker.)
- **Write responses to stdout**, byte-relayable. **Stderr is for diagnostics; do not put protocol bytes there.**
- **Exit cleanly on stdin EOF** in persistent-process mode. (CLIs that need an explicit "exit" command don't fit persistent-process; they should use per-prompt-process only.)
- **Treat SIGINT as "abort current turn"** in persistent-process mode. Process must remain alive after SIGINT for the next prompt. (CLIs that exit on SIGINT can use per-prompt-process only.)

### §7.4 Stdout filtering for noisy CLIs

If a CLI emits non-protocol bytes on stdout (init banner, control characters), the harness image wraps the CLI with a shim:
```sh
#!/bin/sh
# /usr/local/bin/clean-cli
exec /real/cli "$@" 2>&1 | sed '/^Banner line/d'
```
…and points `PADDOCK_HARNESS_BIN` at the shim. The supervisor stays harness-agnostic.

### §7.5 Mode selection guide

A short flowchart for template authors:

- Does the CLI accept stdin EOF as "exit"? **No** → per-prompt-process only.
- Does the CLI tolerate SIGINT mid-turn without exiting? **No** → per-prompt-process only.
- Does the CLI support multi-turn over a single stdin stream (e.g., `--input-format stream-json`)? **No** → per-prompt-process only.
- All three yes → persistent-process is available; choose based on conversation-state requirements.

### §7.6 Reference implementations & validation

- `harness-echo` — synthetic, persistent-process only (no real CLI; the supervisor's contract works against a fake harness).
- `harness-claude-code` — real CLI, both modes.
- A `hack/validate-harness.sh` script: starts the supervisor against a candidate image with a test fixture, runs a smoke prompt, verifies the contract.

## §8. Testing strategy

Three layers, mirroring the existing pattern.

**1. Adapter unit tests** (`internal/adapter/proxy/...`, plus `cmd/adapter-claude-code/...` shim tests).

- The existing six tests in `cmd/adapter-claude-code/{per_prompt_test.go,persistent_test.go}` (enumerated by the codebase mapper) are deleted — they assert subprocess behavior the adapter no longer performs.
- Replace with a fake-supervisor UDS server. Tests assert:
  - `POST /prompts` writes the right bytes to data UDS (persistent: prompt only; per-prompt: begin/prompt/end ctl framing).
  - `/interrupt` writes a well-formed JSON line to ctl.
  - `/end` writes JSON to ctl, closes data write half.
  - `/stream` mirrors UDS bytes to the WS verbatim.
  - Adapter restart redials and resumes operation.
  - Backoff dial loop succeeds after the agent's "install" delay.
- Estimated: ~6 new tests.

**2. Supervisor unit tests** (`cmd/harness-supervisor/...`, new).

- Fake harness binary: a small `bash -c` script written into a tempdir (same pattern as the existing `per_prompt_test.go` used). Tests assert:
  - persistent-process: stdin pipe carries prompt bytes; stdout pipe relays to data UDS; multiple prompts over one CLI run.
  - per-prompt-process: each `begin-prompt` spawns a fresh CLI; CLI exits per `end-prompt`; supervisor loops.
  - Interrupt forwards SIGINT to the right pgid.
  - End closes stdin and waits for CLI exit.
  - Crash detection: CLI exits unexpectedly → ctl `crashed` event, non-zero exit.
  - SIGTERM drains gracefully within `terminationGracePeriodSeconds`.
  - Missing required env vars → fast-fail with a clear error.
- Estimated: ~10 new tests.

**3. End-to-end** (`test/e2e/interactive_test.go`, extend existing).

- One new spec: full Interactive run through the TUI's broker client against a fake-claude harness image (a real harness image built with a fake CLI binary, so we don't burn API budget). Asserts:
  - Prompts arrive at the supervisor's CLI subprocess.
  - Stream events flow back through the broker WS.
  - Interrupt cancels mid-turn without ending the run.
  - End terminates cleanly with the run marked Succeeded.

The fake-claude harness image (`images/harness-claude-code-fake/`?) becomes a documented test fixture — useful both for our CI and for harness authors validating their own integrations.

## §9. Validation against other harnesses

Empirical compatibility matrix (results from background subagent, 2026-05-02):

| CLI | Native I/O shape | Per-prompt fit | Persistent fit | Notes |
|---|---|---|---|---|
| Claude Code | NDJSON in/out via `--input-format stream-json --output-format stream-json` | yes | **yes** | Reference implementation. Stdout flush bug ([anthropics/claude-code#25670](https://github.com/anthropics/claude-code/issues/25670)) — supervisor must not buffer. |
| OpenAI Codex CLI | Plain text or JSONL via `codex exec --json`; `codex exec -` for stdin | yes | no | Per-prompt only. No streamed-input multi-turn surface. |
| Gemini CLI | Text or JSON via `gemini -p … --output-format json` | yes | no | Stdout init-banner leak ([google-gemini/gemini-cli#21433](https://github.com/google-gemini/gemini-cli/issues/21433)) — needs §7.4 wrapper. |
| Aider | Plain text + Rich TUI; `--message` for one-shot, REPL via prompt-toolkit | yes | **no** | REPL hard-requires PTY; persistent mode unreachable without a PTY contract (rejected — see §10). |
| simonw `llm` | Plain text token stream; `llm -c` to continue | yes | no | Multi-turn = repeated `llm -c` invocations. Pipe-friendly. |
| ollama | Plain text or `--format json` | yes | no | Multi-prompt-stdin removed ([ollama/ollama#7823](https://github.com/ollama/ollama/issues/7823)). HTTP API for persistent (out of scope for stdin contract). |
| Continue CLI | `cn -p` final answer; `--format json` for structured events | yes | no | Per-prompt fit. |
| Cursor CLI | NDJSON via `cursor-agent -p --output-format stream-json` | yes | no | Closest analog to Claude on output side; missing `--input-format stream-json` for symmetric persistent. |

Verdict: **the contract generalizes**. `per-prompt-process` covers 7/8; `persistent-process` is Claude-Code-only today, with Cursor the most likely next entrant. Aider's persistent-mode unfit is a "won't support, route to per-prompt" decision, not a contract flaw.

## §10. Out of scope / follow-ups

- **Collector / adapter consolidation.** Considered: now that the adapter loses its subprocess-management logic, could we fold the collector into it? **Rejected for this design** on three grounds. (1) *Trust-boundary asymmetry:* the adapter is broker-facing (accepts traffic from outside the pod, gated only by NetworkPolicy); the collector has workspace PVC mount and K8s API write access (output ConfigMap). Merging would put workspace contents and ConfigMap-write privileges behind the broker-facing surface. The `pod_spec_test.go:184` invariant ("adapter must not see workspace") was flagged by the codebase mapper as load-bearing for exactly this reason. (2) *Lifecycle phase:* the adapter is hot-path during the run (every prompt traverses it); the collector primarily finalizes post-run. Separating lets each component have its own restart/crash semantics. (3) *Scope:* F19 is already substantial; consolidation doesn't collapse any shared state in the new design (the components don't even read the same files). Worth revisiting alongside a follow-up that moves `events.jsonl` translation from the adapter into the supervisor — at that point, the collector's mirror-to-workspace job shrinks and consolidation could reopen with different trade-offs.

- **PTY contract for REPL-only CLIs (Aider, ollama interactive).** Adding PTY support would require a third UDS path (or a control-message extension) and a more complex supervisor. Deferred to a separate spec; per-prompt-process is the workaround.
- **Webhook image-inspection enforcement** of `paddock.dev/adapter-interactive-modes`. The annotation-based template-side enforcement (§4.5) is the v1; image-introspection is a future hardening pass.
- **Adapter-echo migration**. The existing `cmd/adapter-echo/main.go:68-82` synthetic interactive path is unaffected by this design but inconsistent with it (echo doesn't use the supervisor). Optional follow-up to bring echo onto the same plane; out of scope here unless it's trivial in implementation.
- **Audit-event schema for interactive turns.** The existing 2026-04-29 design specified per-prompt audit events; this spec doesn't change the schema. Verify in the implementation plan that the events still carry the right correlation IDs in the new flow.
- **Multi-tenant single-pod runs.** Out of scope; one harness per pod is the model.

## §11. Migration

Single-PR change, no flag-flipping. The current interactive paths are non-functional (F19), so there is no production behavior to preserve.

**Delete:**
- `cmd/adapter-claude-code/per_prompt.go`, `cmd/adapter-claude-code/per_prompt_test.go`
- `cmd/adapter-claude-code/persistent.go`, `cmd/adapter-claude-code/persistent_test.go`

**Add:**
- `internal/adapter/proxy/` (proxy package, harness-agnostic)
- `cmd/harness-supervisor/` (the new binary)
- `images/harness-supervisor/Dockerfile` (scratch image carrying the supervisor)
- `Makefile` target `image-harness-supervisor`
- `docs/contributing/harness-authoring.md`
- `hack/validate-harness.sh`

**Modify:**
- `cmd/adapter-claude-code/main.go` — thin shim around `internal/adapter/proxy/`
- `cmd/adapter-claude-code/server.go` — refactor for new Driver shape
- `images/harness-claude-code/Dockerfile` — `COPY --from` of supervisor; new ENV vars
- `images/harness-claude-code/run.sh` — `exec paddock-harness-supervisor` branch
- `internal/controller/pod_spec.go` — UDS env vars on adapter + agent; `PADDOCK_HARNESS_ARGS` selection
- `internal/controller/pod_spec_test.go` — assert the new env vars; the line:184 invariant stays
- `internal/webhook/v1alpha1/harnessrun_webhook.go` — annotation-based mode enforcement (§4.5)

**Append (in a separate "supersedes" footnote):**
- `docs/superpowers/specs/2026-04-29-interactive-harnessrun-design.md` — note that §2.3's "adapter spawns claude" model is superseded by this design; details remain accurate for the broker/CRD/lifecycle layers.

CRD types are unchanged. Per project CLAUDE.md ("Pre-1.0 evolves in place"), no v1alpha2, no flag aliasing.
