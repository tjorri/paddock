#!/bin/sh
# Fake claude-code harness for e2e — same shape as the real run.sh but
# without the bootstrap install or API call. The supervisor and the
# inline fake CLI cover the rest of the contract.
set -eu

if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
  case "${PADDOCK_INTERACTIVE_MODE}" in
    persistent-process)  export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PERSISTENT}" ;;
    per-prompt-process)  export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PER_PROMPT}" ;;
    *) echo "fake: unknown PADDOCK_INTERACTIVE_MODE: $PADDOCK_INTERACTIVE_MODE" >&2; exit 1 ;;
  esac
  exec paddock-harness-supervisor
fi

# Batch mode for completeness.
prompt=$(cat "${PADDOCK_PROMPT_PATH}" 2>/dev/null || echo "")
mkdir -p "$(dirname "${PADDOCK_RAW_PATH}")"
printf '{"type":"assistant","message":"echo: %s"}\n' "$prompt" > "${PADDOCK_RAW_PATH}"
