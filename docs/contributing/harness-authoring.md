# Authoring a Paddock-compatible harness

A Paddock harness ships as **two images** that pair together:

1. **Agent image** (`paddock-<harness>:<tag>`) — runs the harness CLI itself in the run pod's main container. For interactive runs it also ships `paddock-harness-supervisor` to bridge the CLI's stdio onto a UDS pair.
2. **Runtime image** (`paddock-runtime-<harness>:<tag>`) — runs as the per-run runtime sidecar. Owns the harness-side data plane (input recording, output translation, transcript persistence, ConfigMap publishing, stdout passthrough, broker HTTP+WS surface for interactive modes).

The pod composition the controller materialises is `agent + runtime + (proxy)`, plus the `iptables-init` init container in transparent mode. Two containers per harness, one author contract per container.

This guide covers both contracts.

## The runtime contract

A harness's runtime image is a Go binary that imports `internal/runtime` for the generic plumbing and supplies one or two harness-specific functions (a `Converter` always, a `PromptFormatter` if the harness supports interactive mode).

### Six requirements

1. **One image per harness.** The image name convention is `paddock-runtime-<harness>` (e.g. `paddock-runtime-claude-code`, `paddock-runtime-echo`). Reference it from `HarnessTemplate.spec.runtime.image`.
2. **Import `internal/runtime` for plumbing.** The library exposes `transcript` (events.jsonl writer + tail-broadcast), `archive` (`/workspace/.paddock/runs/<run>/` layout + metadata.json), `publish` (debounced ConfigMap projection with bounded recent-events ring), `stdout` (JSONL passthrough so `kubectl logs` and the transcript stay byte-identical), and `proxy` (the broker HTTP+WS surface for interactive mode). Don't reimplement these — call into them.
3. **Supply a `Converter`.** The function signature is:
   ```go
   func(line string) ([]paddockv1alpha1.PaddockEvent, error)
   ```
   It maps one line of the harness CLI's native output (whatever `PADDOCK_RAW_PATH` accumulates) into zero or more `PaddockEvent`s. A single line can produce multiple events when the harness emits mixed-block messages (e.g. one assistant message containing a text block plus N tool_use blocks). Malformed lines should return `(nil, err)` — the runtime logs and skips, never crashing the run.
4. **Supply a `PromptFormatter`** if the harness supports interactive mode. The signature is:
   ```go
   func(text string, seq int32) ([]byte, error)
   ```
   It wraps the user's prompt + broker-assigned sequence number into the harness CLI's native stdin shape (e.g. claude's stream-json `{"type":"user","message":{...}}`). Batch-only runtimes can omit this. When `nil`, the proxy writes the request body to the data UDS verbatim.
5. **Declare supported interactive modes** via the image label:
   ```dockerfile
   LABEL paddock.dev/runtime-interactive-modes="per-prompt-process,persistent-process"
   ```
   Comma-separated. Empty (or absent) means the runtime is batch-only — the controller refuses to admit interactive-mode runs against it.
6. **Follow the canonical Dockerfile shape.** See `images/runtime-claude-code/Dockerfile` for the reference. The build context is the repo root (the runtime needs Go module access); the final image is `gcr.io/distroless/static:nonroot`.

### Canonical Dockerfile

```dockerfile
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum

COPY api/ api/
COPY cmd/runtime-<name>/ cmd/runtime-<name>/
COPY internal/runtime/ internal/runtime/
COPY internal/broker/api/ internal/broker/api/
COPY internal/brokerclient/ internal/brokerclient/
COPY internal/auditing/ internal/auditing/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o runtime-<name> ./cmd/runtime-<name>

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/runtime-<name> .
USER 65532:65532

LABEL paddock.dev/runtime-interactive-modes="per-prompt-process,persistent-process"

ENTRYPOINT ["/runtime-<name>"]
```

Replace `<name>` with the harness slug (e.g. `claude-code`, `echo`). For batch-only harnesses, set the label to the empty string (`paddock.dev/runtime-interactive-modes=""`) — the value is parsed at admission to gate interactive mode admission.

### Environment the runtime expects

The controller injects these env vars into the runtime container; the runtime reads them via `loadEnvOnly` (or your own equivalent if you don't want to reuse the reference shape). The reference implementations under `cmd/runtime-claude-code/` and `cmd/runtime-echo/` are good starting points to copy-and-adapt.

| Env var | Meaning |
|---------|---------|
| `PADDOCK_MODE` | `Batch` or `Interactive` |
| `PADDOCK_INTERACTIVE_MODE` | Set in interactive mode: `per-prompt-process` or `persistent-process` |
| `PADDOCK_RAW_PATH` | File path the agent writes its native output to (the runtime tails this in batch mode) |
| `PADDOCK_PROMPT_FILE` | Batch-only: file containing the prompt text — the runtime writes a single `PromptSubmitted` event from it at startup |
| `PADDOCK_AGENT_DATA_SOCKET` | Interactive-only: UDS the supervisor exposes for stream-json frames |
| `PADDOCK_AGENT_CTL_SOCKET` | Interactive-only: UDS the supervisor exposes for control frames (interrupt, end) |
| `PADDOCK_BROKER_URL` | Broker base URL (used for the per-turn `/turn-complete` callback) |
| `PADDOCK_BROKER_TOKEN_PATH` | Projected SA-token path for broker authn |
| `PADDOCK_BROKER_CA_PATH` | Broker serving-CA bundle path |
| `PADDOCK_RUN_NAME`, `PADDOCK_RUN_NAMESPACE` | Run identity for archive metadata + ConfigMap targeting |
| `PADDOCK_WORKSPACE_NAME`, `PADDOCK_TEMPLATE_NAME` | Workspace/template provenance for `metadata.json` |
| `PADDOCK_HARNESS_IMAGE` | The agent image reference (provenance for `metadata.json`) |
| `PADDOCK_TRANSCRIPT_DIR` | Override for the transcript root (default `/workspace/.paddock/runs/<run-name>/`) |
| `PADDOCK_OUTPUT_CONFIGMAP` | Override for the per-run output ConfigMap (default `<run-name>-output`) |

### Transcript layout the runtime writes

Per-run state lives at `/workspace/.paddock/runs/<run-name>/`:

| Path | Producer | Shape |
|---|---|---|
| `metadata.json` | `archive.WriteStartMetadata` at startup, `UpdateCompletion` at exit | `{schemaVersion, run:{name,namespace}, workspace, template, mode, harness:{image}, startedAt, completedAt, status, reason}` |
| `events.jsonl` | `transcript.Append` | One PaddockEvent per line, append-only |
| `raw.jsonl` | The agent (via `PADDOCK_RAW_PATH`) — the runtime only consumes it | Whatever shape the harness CLI emits natively |
| `result.json` | The agent (via `PADDOCK_RESULT_PATH`) | Optional structured result; surfaced into `Status.Outputs` |

The runtime also mirrors every transcript line to its own stdout (via `internal/runtime/stdout`) so `kubectl logs <pod> -c runtime` is byte-identical to the on-disk `events.jsonl`. Log aggregators (Loki, Promtail, fluent-bit) can scrape from container stdout — the run pod carries the `paddock.dev/{run,namespace,workspace,template,harness,mode}` labels so cross-run filtering is a label-selector query.

### PaddockEvent types the runtime emits

The canonical built-ins (in `api/v1alpha1/harnessrun_types.go`) are:

- `Message` — model-emitted prose (or system events: session-init, etc.).
- `ToolUse` — one tool invocation. Fields typically include `tool`, `id`, `input`.
- `Result` — a successful turn-terminal event. Fields may include `sessionId`, `turns`, `durationMs`, `costUsd`.
- `Error` — a failed turn-terminal event.
- `PromptSubmitted` — input record, written by the runtime itself (not the converter). Carries `Fields["text"]` (full prompt), `Fields["length"]`, `Fields["hash"]` (sha256), and optionally `Fields["seq"]`/`Fields["submitter"]` in interactive mode. The ConfigMap projection drops `Fields["text"]` so the prompt body never reaches `Status.RecentEvents`.

A converter may emit additional types — consumers tolerate unknown values per the godoc on `PaddockEvent.Type`.

### Reference runtimes

- [`cmd/runtime-echo/`](../../cmd/runtime-echo/) — deterministic batch fixture. Tiny converter; no `PromptFormatter`. Good template for a copy-paste-and-adapt of a new batch-only harness.
- [`cmd/runtime-claude-code/`](../../cmd/runtime-claude-code/) — full both-modes implementation. Converter handles claude's `--output-format stream-json` shape; `PromptFormatter` wraps user prompts in claude's `--input-format stream-json` user-message shape.

## The agent contract

The agent image is the run pod's main container — it executes the harness CLI itself. It mounts the workspace PVC and (in transparent mode) is fronted by the proxy sidecar via iptables redirect.

Batch mode is straightforward: the agent's `run.sh` invokes the CLI with the prompt mounted via `PADDOCK_PROMPT_PATH`, writes the CLI's native output to `PADDOCK_RAW_PATH`, and (optionally) writes a structured `PADDOCK_RESULT_PATH`. Exit code 0 = success.

| Env var | Meaning |
|---------|---------|
| `PADDOCK_PROMPT_PATH` | File containing the prompt text |
| `PADDOCK_RAW_PATH` | File path the harness writes raw output to |
| `PADDOCK_RESULT_PATH` | Optional file path for structured result JSON |
| `PADDOCK_WORKSPACE` | Workspace mount path |
| `HOME` | Set to `$PADDOCK_WORKSPACE/.home`; tools persist across runs |

The runtime sidecar reads `events.jsonl` (translated by its converter from `raw`) and `result.json` and reports them via the controller.

### Interactive mode (agent side)

For interactive runs the agent container additionally ships `paddock-harness-supervisor`, a harness-agnostic binary that listens on a UDS pair (`/paddock/agent-data.sock` and `/paddock/agent-ctl.sock`) and bridges them to the harness CLI's stdio. The runtime sidecar dials those sockets from the other side and acts as the broker-facing endpoint.

To make a harness interactive-capable:

1. **Include the supervisor binary** at `/usr/local/bin/paddock-harness-supervisor`:
   ```dockerfile
   COPY --from=ghcr.io/tjorri/paddock/harness-supervisor:<version> \
        /supervisor /usr/local/bin/paddock-harness-supervisor
   ```

2. **Set the harness invocation env vars** in the Dockerfile:
   ```dockerfile
   ENV PADDOCK_HARNESS_BIN=/path/to/cli
   ENV PADDOCK_HARNESS_ARGS_PERSISTENT="..."   # if persistent-process supported
   ENV PADDOCK_HARNESS_ARGS_PER_PROMPT="..."   # if per-prompt-process supported
   ```

3. **Branch `run.sh`** to exec the supervisor when in interactive mode:
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

4. **Declare supported modes** on the runtime image (not the agent image) via the `paddock.dev/runtime-interactive-modes` label — see [The runtime contract](#the-runtime-contract) above.

### Harness CLI requirements

The CLI must satisfy these requirements to participate in interactive runs:

- **Read prompts from stdin** in any line-delimited stream the supervisor can pipe byte-for-byte. (Stream-json, NDJSON, plain text — the supervisor doesn't interpret. The broker must be sending what the CLI expects, which is the harness-image author's contract with the broker.)

- **Write responses to stdout**, byte-relayable. **Stderr is for diagnostics; do not put protocol bytes on stdout.**

- **Exit cleanly on stdin EOF** when in persistent-process mode. (CLIs that need an explicit "exit" command don't fit persistent-process; they should use per-prompt-process only.)

- **Treat SIGINT as "abort current turn"** in persistent-process mode. The process must remain alive after SIGINT to accept the next prompt. (CLIs that exit on SIGINT can use per-prompt-process only.)

### Stdout filtering for noisy CLIs

If a CLI emits non-protocol bytes on stdout (init banner, control characters, etc.), wrap the CLI with a shell shim in the agent image:

```sh
#!/bin/sh
# /usr/local/bin/clean-cli
exec /real/cli "$@" 2>&1 | sed '/^Banner line/d'
```

Then point `PADDOCK_HARNESS_BIN` at the shim. The supervisor stays harness-agnostic and transparent; filtering happens at image-build time.

### Mode selection guide

Use this flowchart to determine which modes your CLI can support:

- Does the CLI accept stdin EOF as "exit"? **No** → per-prompt-process only.
- Does the CLI tolerate SIGINT mid-turn without exiting? **No** → per-prompt-process only.
- Does the CLI support multi-turn over a single stdin stream (e.g., `--input-format stream-json`)? **No** → per-prompt-process only.
- All three yes → both modes available; choose based on conversation-state requirements.

## Reference implementations

| Component | Echo (CI fixture) | Claude Code (real CLI) |
|---|---|---|
| Agent image | `paddock-echo:dev` (`images/echo/Dockerfile`) | `paddock-claude-code:dev` (`images/claude-code/Dockerfile`) |
| Runtime image | `paddock-runtime-echo:dev` (`images/runtime-echo/Dockerfile`) | `paddock-runtime-claude-code:dev` (`images/runtime-claude-code/Dockerfile`) |
| Modes | Batch only | Batch + persistent-process + per-prompt-process |

## Validation

Run `hack/validate-harness.sh <agent-image>` against your candidate **agent** image:

```bash
hack/validate-harness.sh paddock-claude-code:dev
```

The script invokes the supervisor in a docker container against a synthetic test fixture and reports:

1. Supervisor binary present and executable
2. `PADDOCK_HARNESS_BIN` declared in image env
3. Supervisor fails fast on missing required environment variables
4. (TODO) Live round-trip against fake supervisor socket pair

Expected output: 3 OK, 1 SKIP.

The runtime image has its own unit-test surface under `cmd/runtime-<name>/*_test.go` (converter cases, prompt-formatter round-trips, env parsing). The end-to-end check is the per-harness e2e test in `test/e2e/`.

## Empirical compatibility

The interactive contract has been validated against the following CLIs (per `docs/superpowers/specs/2026-05-02-interactive-adapter-as-proxy-design.md` §9):

| CLI | Native I/O shape | Per-prompt fit | Persistent fit | Notes |
|---|---|---|---|---|
| Claude Code | NDJSON in/out via `--input-format stream-json --output-format stream-json` | yes | **yes** | Reference implementation. Stdout flush bug ([anthropics/claude-code#25670](https://github.com/anthropics/claude-code/issues/25670)) — supervisor must not buffer. |
| OpenAI Codex CLI | Plain text or JSONL via `codex exec --json`; `codex exec -` for stdin | yes | no | Per-prompt only. No streamed-input multi-turn surface. |
| Gemini CLI | Text or JSON via `gemini -p … --output-format json` | yes | no | Stdout init-banner leak ([google-gemini/gemini-cli#21433](https://github.com/google-gemini/gemini-cli/issues/21433)) — needs filtering shim. |
| Aider | Plain text + Rich TUI; `--message` for one-shot, REPL via prompt-toolkit | yes | no | REPL hard-requires PTY; persistent mode unreachable without a PTY contract. |
| simonw `llm` | Plain text token stream; `llm -c` to continue | yes | no | Multi-turn = repeated `llm -c` invocations. Pipe-friendly. |
| ollama | Plain text or `--format json` | yes | no | Multi-prompt-stdin removed ([ollama/ollama#7823](https://github.com/ollama/ollama/issues/7823)). HTTP API for persistent (out of scope for stdin contract). |
| Continue CLI | `cn -p` final answer; `--format json` for structured events | yes | no | Per-prompt fit. |
| Cursor CLI | NDJSON via `cursor-agent -p --output-format stream-json` | yes | no | Closest analog to Claude on output side; missing `--input-format stream-json` for symmetric persistent. |

**Verdict:** `per-prompt-process` covers 7/8 CLIs; `persistent-process` is Claude-Code-native today, with Cursor the most likely next entrant. Aider's persistent-mode unfit is a "won't support, route to per-prompt" decision, not a contract flaw.
