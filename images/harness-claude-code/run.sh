#!/usr/bin/env bash
# paddock-claude-code — entrypoint for the Claude Code demo harness.
#
# Reads the prompt from PADDOCK_PROMPT_PATH, runs the pinned `claude`
# CLI with stream-json output, tees raw bytes to PADDOCK_RAW_PATH for
# the adapter + collector to pick up, then synthesises a result.json
# from the final "result" line so HarnessRun.status.outputs gets
# populated without waiting for a harness-side writer. Exits non-zero
# when the final result line has is_error=true (e.g. API billing
# errors) so the Job reports Failed rather than Succeeded — the
# `claude` CLI itself exits 0 on API errors, which would otherwise
# mask the failure.

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

# Extract the final "result" line once; used for both result.json
# synthesis and the is_error exit-code propagation below.
last_result=$(grep '"type":"result"' "$PADDOCK_RAW_PATH" | tail -n 1 || true)

# Best-effort result.json fallback. The adapter can't write here (it
# doesn't mount the workspace PVC), so the harness itself produces a
# minimal outputs document. If the result line is missing (claude
# crashed mid-stream), we skip silently — status.outputs stays empty.
if [[ -n "${PADDOCK_RESULT_PATH:-}" && -n "$last_result" ]]; then
  mkdir -p "$(dirname "$PADDOCK_RESULT_PATH")"
  summary=$(printf '%s' "$last_result" | jq -r '(.result // .error // "claude-code run completed")' | head -c 1024)
  cost=$(printf '%s' "$last_result" | jq -r '.total_cost_usd // .cost_usd // 0')
  jq -n --arg s "$summary" --arg c "$cost" \
    '{pullRequests: [], filesChanged: 0, summary: $s, artifacts: ["cost_usd=" + $c]}' \
    >"$PADDOCK_RESULT_PATH"
fi

# Exit non-zero when claude reports is_error=true. Without this the
# Job would transition to Succeeded despite the stream carrying an
# API error (billing, rate limit, auth), making failures invisible
# at the HarnessRun.status.phase level.
if [[ -n "$last_result" ]]; then
  is_error=$(printf '%s' "$last_result" | jq -r '.is_error // false')
  if [[ "$is_error" == "true" ]]; then
    echo "paddock-claude-code: claude reported is_error=true; exiting 1 to mark run Failed" >&2
    exit 1
  fi
fi
