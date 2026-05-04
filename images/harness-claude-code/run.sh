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
: "${PADDOCK_CLAUDE_CODE_VERSION:=latest}"

mkdir -p "$(dirname "$PADDOCK_RAW_PATH")"

# Combine the Alpine system CA bundle with the paddock proxy CA
# into a single file, then point all standard trust-store env vars
# at it. The controller mounts the paddock proxy CA (the cluster
# root paddock-proxy-ca, self-signed) at $SSL_CERT_FILE and exports
# SSL_CERT_FILE + NODE_EXTRA_CA_CERTS + REQUESTS_CA_BUNDLE +
# GIT_SSL_CAINFO. Tools that read a SINGLE bundle file (rather than
# the cert directory) need both the public roots (for talking to
# any non-paddock service) AND the paddock root (for talking to the
# proxy's MITM cert) in one file. The concat below produces
# /tmp/paddock-trust.pem and re-exports the variables; downstream
# tools pick up the combined bundle without changes.
if [[ -n "${SSL_CERT_FILE:-}" \
   && "${SSL_CERT_FILE}" != "/etc/ssl/certs/ca-certificates.crt" \
   && -r "${SSL_CERT_FILE}" \
   && -r /etc/ssl/certs/ca-certificates.crt ]]; then
  cat /etc/ssl/certs/ca-certificates.crt "${SSL_CERT_FILE}" > /tmp/paddock-trust.pem
  export SSL_CERT_FILE=/tmp/paddock-trust.pem
  export NODE_EXTRA_CA_CERTS=/tmp/paddock-trust.pem
  export REQUESTS_CA_BUNDLE=/tmp/paddock-trust.pem
  export CURL_CA_BUNDLE=/tmp/paddock-trust.pem
  export GIT_SSL_CAINFO=/tmp/paddock-trust.pem
fi

# Install the Claude Code CLI at run time so operators can pick the
# version via PADDOCK_CLAUDE_CODE_VERSION without rebuilding the image.
# The harness pod's egress is locked down by iptables-init before this
# script runs, so probe Anthropic's downloads CDN first — otherwise an
# opaque connect timeout is the only signal when the host is missing
# from the egress allowlist.
if ! curl -fsS --connect-timeout 5 --max-time 10 -o /dev/null \
     https://downloads.claude.ai/claude-code-releases/latest; then
  cat >&2 <<'ERR'
paddock-claude-code: cannot reach https://downloads.claude.ai/

The Claude Code CLI is installed at run time, so the harness pod must
be allowed to reach Anthropic's downloads host.

Most likely cause: downloads.claude.ai:443 is missing from the harness
template's requires.egress allowlist (and/or the BrokerPolicy's
grants.egress). Add it in both places and re-apply, e.g.:

  # ClusterHarnessTemplate
  spec:
    requires:
      egress:
        - host: api.anthropic.com
          ports: [443]
        - host: downloads.claude.ai
          ports: [443]

  # BrokerPolicy
  spec:
    grants:
      egress:
        - host: api.anthropic.com
          ports: [443]
        - host: downloads.claude.ai
          ports: [443]

See config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code.yaml
and config/samples/paddock_v1alpha1_brokerpolicy.yaml.
ERR
  exit 1
fi

echo "paddock-claude-code: installing Claude Code @ ${PADDOCK_CLAUDE_CODE_VERSION}" >&2
# bootstrap.sh sha256-verifies the downloaded binary against the manifest
# fetched from the same CDN, so we get integrity-against-corruption for
# free. End-to-end attestation against Anthropic's GPG-signed
# manifest.json.sig would be a defense-in-depth follow-up.
curl -fsSL https://downloads.claude.ai/claude-code-releases/bootstrap.sh \
  | bash -s "${PADDOCK_CLAUDE_CODE_VERSION}"

# bootstrap.sh's native installer drops the binary at $HOME/.local/bin/claude
# and only prints a warning when that directory isn't in PATH (no auto-export).
# Prepend it ourselves so the `claude` invocation below resolves regardless of
# what the upstream installer does to the rest of the environment.
export PATH="$HOME/.local/bin:$PATH"

# Interactive mode (per spec §4.3): the supervisor takes over the
# stdin/stdout contract. run.sh has already done install + PATH +
# CA-bundle setup, so the supervisor just needs the harness binary
# location and per-mode argv.
if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
  case "${PADDOCK_INTERACTIVE_MODE}" in
    persistent-process)
      export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PERSISTENT:?PADDOCK_HARNESS_ARGS_PERSISTENT not set in image}"
      ;;
    per-prompt-process)
      export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PER_PROMPT:?PADDOCK_HARNESS_ARGS_PER_PROMPT not set in image}"
      ;;
    *)
      echo "paddock-claude-code: unknown PADDOCK_INTERACTIVE_MODE: $PADDOCK_INTERACTIVE_MODE" >&2
      exit 1
      ;;
  esac
  # PADDOCK_HARNESS_BIN is declared in the Dockerfile as a fallback;
  # if the runtime install puts claude elsewhere (e.g. via custom
  # PADDOCK_CLAUDE_CODE_VERSION), prefer the resolved $(command -v
  # claude) when present.
  if command -v claude >/dev/null 2>&1; then
    export PADDOCK_HARNESS_BIN="$(command -v claude)"
  fi
  exec paddock-harness-supervisor
fi

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
