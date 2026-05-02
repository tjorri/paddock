# Authoring a Paddock-compatible harness image

This guide walks an image author through the contract a harness container must implement to participate in Paddock's batch and interactive runtime. The supervisor binary handles the IPC plumbing; the image author supplies a CLI that meets the (small) contract below.

## Batch mode

| Env var | Meaning |
|---------|---------|
| `PADDOCK_PROMPT_PATH` | File containing the prompt text |
| `PADDOCK_RAW_PATH` | File path the harness writes raw output to |
| `PADDOCK_RESULT_PATH` | Optional file path for structured result JSON |
| `PADDOCK_WORKSPACE` | Workspace mount path |
| `HOME` | Set to `$PADDOCK_WORKSPACE/.home`; tools persist across runs |

Exit semantics: the CLI should exit with code 0 on success, or non-zero on failure. The collector reads `events.jsonl` (translated by the adapter from `raw`) and `result.json` and reports them via the controller.

## Interactive mode

To make a harness interactive-capable, the image must:

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

4. **Declare supported modes** in the HarnessTemplate's `spec.interactive.mode` enum and add the recommended annotation:
   ```yaml
   metadata:
     annotations:
       paddock.dev/adapter-interactive-modes: "per-prompt-process,persistent-process"
   ```

## Harness CLI requirements

The CLI must satisfy these requirements to participate in interactive runs:

- **Read prompts from stdin** in any line-delimited stream the supervisor can pipe byte-for-byte. (Stream-json, NDJSON, plain text — the supervisor doesn't interpret. The broker must be sending what the CLI expects, which is the harness-image author's contract with the broker.)

- **Write responses to stdout**, byte-relayable. **Stderr is for diagnostics; do not put protocol bytes on stdout.**

- **Exit cleanly on stdin EOF** when in persistent-process mode. (CLIs that need an explicit "exit" command don't fit persistent-process; they should use per-prompt-process only.)

- **Treat SIGINT as "abort current turn"** in persistent-process mode. The process must remain alive after SIGINT to accept the next prompt. (CLIs that exit on SIGINT can use per-prompt-process only.)

## Stdout filtering for noisy CLIs

If a CLI emits non-protocol bytes on stdout (init banner, control characters, etc.), wrap the CLI with a shell shim in the harness image:

```sh
#!/bin/sh
# /usr/local/bin/clean-cli
exec /real/cli "$@" 2>&1 | sed '/^Banner line/d'
```

Then point `PADDOCK_HARNESS_BIN` at the shim. The supervisor stays harness-agnostic and transparent; filtering happens at image-build time.

## Mode selection guide

Use this flowchart to determine which modes your CLI can support:

- Does the CLI accept stdin EOF as "exit"? **No** → per-prompt-process only.
- Does the CLI tolerate SIGINT mid-turn without exiting? **No** → per-prompt-process only.
- Does the CLI support multi-turn over a single stdin stream (e.g., `--input-format stream-json`)? **No** → per-prompt-process only.
- All three yes → both modes available; choose based on conversation-state requirements.

## Reference implementations

- `harness-echo` — synthetic, persistent-process only (no real CLI; validates the supervisor's contract against a fake harness).
- `harness-claude-code` — real CLI, both modes (reference implementation for production use).

## Validation

Run `hack/validate-harness.sh <image>` against your candidate image:

```bash
hack/validate-harness.sh paddock-claude-code:dev
```

The script invokes the supervisor in a docker container against a synthetic test fixture and reports:
1. Supervisor binary present and executable
2. `PADDOCK_HARNESS_BIN` declared in image env
3. Supervisor fails fast on missing required environment variables
4. (TODO) Live round-trip against fake adapter socket pair

Expected output: 3 OK, 1 SKIP.

## Empirical compatibility

The contract has been validated against the following CLIs (per `docs/superpowers/specs/2026-05-02-interactive-adapter-as-proxy-design.md` §9):

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
