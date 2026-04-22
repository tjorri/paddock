#!/usr/bin/env bash
# paddock-claude-code — entrypoint for the Claude Code demo harness.
#
# Reads the prompt from PADDOCK_PROMPT_PATH, runs the pinned `claude`
# CLI with stream-json output, tees raw bytes to PADDOCK_RAW_PATH for
# the adapter + collector to pick up, then synthesises a result.json
# from the final "result" line so HarnessRun.status.outputs gets
# populated without waiting for a harness-side writer.

set -euo pipefail

: "${PADDOCK_PROMPT_PATH:?PADDOCK_PROMPT_PATH is required}"
: "${PADDOCK_RAW_PATH:=/paddock/raw/out}"
: "${PADDOCK_RUN_NAME:=claude-code}"

mkdir -p "$(dirname "$PADDOCK_RAW_PATH")"

prompt=$(cat "$PADDOCK_PROMPT_PATH")

# Build argv: -p puts claude in print-mode; --verbose is required to
# get streaming output; --output-format stream-json gives us one JSON
# object per line on stdout.
args=(-p --output-format stream-json --verbose)
if [[ -n "${PADDOCK_MODEL:-}" ]]; then
  args+=(--model "$PADDOCK_MODEL")
fi

# Tee raw stream-json to PADDOCK_RAW_PATH. `pipefail` is on, so a
# non-zero claude exit propagates even though tee always succeeds.
claude "${args[@]}" -- "$prompt" | tee "$PADDOCK_RAW_PATH"

# Best-effort result.json fallback. The adapter can't write here
# (it doesn't mount the workspace PVC), so the harness itself
# synthesises a minimal outputs document from the last "result"
# line. If that line is missing (claude crashed mid-stream), we
# skip silently — status.outputs simply stays empty.
if [[ -n "${PADDOCK_RESULT_PATH:-}" ]]; then
  mkdir -p "$(dirname "$PADDOCK_RESULT_PATH")"
  last=$(grep '"type":"result"' "$PADDOCK_RAW_PATH" | tail -n 1 || true)
  if [[ -n "$last" ]]; then
    summary=$(printf '%s' "$last" | jq -r '(.result // .error // "claude-code run completed")' | head -c 1024)
    cost=$(printf '%s' "$last" | jq -r '.total_cost_usd // .cost_usd // 0')
    jq -n --arg s "$summary" --arg c "$cost" \
      '{pullRequests: [], filesChanged: 0, summary: $s, artifacts: ["cost_usd=" + $c]}' \
      >"$PADDOCK_RESULT_PATH"
  fi
fi
