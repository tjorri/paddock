#!/bin/sh
# paddock-echo — deterministic harness for Paddock CI/e2e.
#
# Emits a fixed sequence of raw JSONL events to PADDOCK_RAW_PATH based on
# the prompt at PADDOCK_PROMPT_PATH, writes a result.json to
# PADDOCK_RESULT_PATH if set, and exits 0.

set -eu

: "${PADDOCK_PROMPT_PATH:=/paddock/prompt/prompt.txt}"
: "${PADDOCK_RAW_PATH:=/paddock/raw/out}"
: "${PADDOCK_RUN_NAME:=echo-run}"

mkdir -p "$(dirname "$PADDOCK_RAW_PATH")"
: >"$PADDOCK_RAW_PATH"

prompt=""
if [ -f "$PADDOCK_PROMPT_PATH" ]; then
  # Collapse newlines so each event is a single JSON line; cap to keep
  # fixture output bounded.
  prompt=$(tr '\n' ' ' <"$PADDOCK_PROMPT_PATH" | head -c 512)
fi
esc_prompt=$(printf '%s' "$prompt" | sed 's/\\/\\\\/g; s/"/\\"/g')
n=${#prompt}

{
  printf '%s\n' '{"kind":"message","text":"echo harness starting"}'
  printf '{"kind":"tool","tool":"read","path":"%s"}\n' "$PADDOCK_PROMPT_PATH"
  printf '{"kind":"message","text":"prompt: %s"}\n' "$esc_prompt"
  printf '{"kind":"result","summary":"echoed %d bytes","filesChanged":0}\n' "$n"
} >>"$PADDOCK_RAW_PATH"

if [ -n "${PADDOCK_RESULT_PATH:-}" ]; then
  mkdir -p "$(dirname "$PADDOCK_RESULT_PATH")"
  cat >"$PADDOCK_RESULT_PATH" <<EOF
{
  "pullRequests": [],
  "filesChanged": 0,
  "summary": "echoed $n bytes",
  "artifacts": []
}
EOF
fi

# Interactive mode: stay alive so the controller observes Phase=Running.
# The adapter sidecar handles broker prompts via its loopback server.
# Exits on SIGTERM (kubelet pod-shutdown).
if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
  exec sleep infinity
fi
