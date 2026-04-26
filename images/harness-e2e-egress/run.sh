#!/bin/sh
# paddock-e2e-egress — deterministic harness for Paddock v0.3 Kind e2e.
#
# Drives one or more HTTPS probes via curl so the proxy sidecar records
# AuditEvents. Optionally loops while holding the Pod alive so the
# BrokerPolicy-delete-mid-run scenario can observe the transition from
# egress-allow to egress-block.
#
# Env inputs (all optional):
#   E2E_EGRESS_TARGETS  Comma-separated URLs. Probed once at start.
#   E2E_EGRESS_LOOP     Single URL probed every E2E_LOOP_SECONDS
#                               until E2E_HOLD_SECONDS elapses.
#   E2E_LOOP_SECONDS    Loop interval (default 3).
#   E2E_HOLD_SECONDS    Total hold duration (default 0 = no hold).
#
# Emits echo-compatible JSONL events (kind ∈ {message,tool,result}) so
# paddock-adapter-echo can translate them unchanged.

set -eu

: "${PADDOCK_PROMPT_PATH:=/paddock/prompt/prompt.txt}"
: "${PADDOCK_RAW_PATH:=/paddock/raw/out}"
: "${PADDOCK_RUN_NAME:=e2e-egress}"
: "${E2E_EGRESS_TARGETS:=}"
: "${E2E_EGRESS_LOOP:=}"
: "${E2E_LOOP_SECONDS:=3}"
: "${E2E_HOLD_SECONDS:=0}"

mkdir -p "$(dirname "$PADDOCK_RAW_PATH")"
: >"$PADDOCK_RAW_PATH"

emit_msg() {
  # $1 is already plain text — escape backslashes + double quotes for JSON.
  esc=$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf '{"kind":"message","text":"%s"}\n' "$esc" >>"$PADDOCK_RAW_PATH"
}

emit_tool() {
  esc=$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf '{"kind":"tool","tool":"curl","path":"%s"}\n' "$esc" >>"$PADDOCK_RAW_PATH"
}

emit_result() {
  esc=$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf '{"kind":"result","summary":"%s","filesChanged":0}\n' "$esc" >>"$PADDOCK_RAW_PATH"
}

probe_once() {
  target="$1"
  emit_tool "$target"
  # -sS: silent + show-errors; -m: bounded per-probe timeout so a slow
  # block response doesn't stall the loop.
  if curl -sS -m 5 -o /dev/null -w "" "$target" 2>/tmp/curl.err; then
    emit_msg "probe $target: reachable"
  else
    msg=$(head -c 200 /tmp/curl.err 2>/dev/null || true)
    emit_msg "probe $target: blocked/error (${msg})"
  fi
}

probed=0
if [ -n "$E2E_EGRESS_TARGETS" ]; then
  emit_msg "starting egress probes"
  # POSIX shell word-splitting on commas. No filenames involved, so the
  # usual IFS disclaimer doesn't apply.
  old_ifs="$IFS"
  IFS=,
  for t in $E2E_EGRESS_TARGETS; do
    probe_once "$t"
    probed=$((probed + 1))
  done
  IFS="$old_ifs"
fi

if [ -n "$E2E_EGRESS_LOOP" ] && [ "$E2E_HOLD_SECONDS" -gt 0 ]; then
  emit_msg "loop-probing $E2E_EGRESS_LOOP for ${E2E_HOLD_SECONDS}s"
  end=$(( $(date +%s) + E2E_HOLD_SECONDS ))
  while [ "$(date +%s)" -lt "$end" ]; do
    probe_once "$E2E_EGRESS_LOOP"
    probed=$((probed + 1))
    sleep "$E2E_LOOP_SECONDS"
  done
elif [ "$E2E_HOLD_SECONDS" -gt 0 ]; then
  emit_msg "holding for ${E2E_HOLD_SECONDS}s"
  sleep "$E2E_HOLD_SECONDS"
fi

emit_result "egress probe complete: $probed target(s)"

if [ -n "${PADDOCK_RESULT_PATH:-}" ]; then
  mkdir -p "$(dirname "$PADDOCK_RESULT_PATH")"
  cat >"$PADDOCK_RESULT_PATH" <<EOF
{
  "pullRequests": [],
  "filesChanged": 0,
  "summary": "egress probe complete: $probed target(s)",
  "artifacts": []
}
EOF
fi
