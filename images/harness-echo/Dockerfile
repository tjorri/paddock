# paddock-echo — deterministic harness image used by CI and e2e tests.
# Not an LLM. In batch mode reads PADDOCK_PROMPT_PATH, emits a fixed
# sequence of raw JSONL events to PADDOCK_RAW_PATH, writes a result.json,
# exits 0. In interactive mode dispatches to paddock-harness-supervisor,
# which spawns the inline echo-interactive harness binary and bridges
# /prompts ↔ harness stdio over the agent UDS.
#
# Build context is the repo root (not images/harness-echo) so the
# supervisor binary can be built inline from cmd/harness-supervisor.
# Inlining avoids a cross-image COPY --from that would require a
# preexisting :dev tag in the local docker daemon — which is fine for
# `make image-echo` but breaks buildx jobs in CI that have no such tag.

FROM --platform=$BUILDPLATFORM golang:1.26 AS supervisor-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /workspace
COPY go.mod go.sum ./
COPY cmd/harness-supervisor/ cmd/harness-supervisor/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o /supervisor ./cmd/harness-supervisor

FROM alpine:3.22

RUN adduser -D -u 65532 paddock

COPY --from=supervisor-builder /supervisor /usr/local/bin/paddock-harness-supervisor

COPY images/harness-echo/run.sh /usr/local/bin/paddock-echo
RUN chmod +x /usr/local/bin/paddock-echo

ENV PADDOCK_HARNESS_BIN=/usr/local/bin/echo-interactive
ENV PADDOCK_HARNESS_ARGS_PERSISTENT=""
ENV PADDOCK_HARNESS_ARGS_PER_PROMPT=""

# Inline interactive harness CLI: reads NDJSON prompts from stdin (the
# runtime forwards POST /prompts bodies verbatim — paddock-echo has no
# PromptFormatter), and emits one assistant message + one
# turn-terminating result frame per input line. The result frame is
# what triggers the runtime's OnTurnComplete hook. The trap on SIGINT
# lets the supervisor's per-prompt-process mode exercise its interrupt
# path without the harness dying outright.
#
# Each line carries both a `type` field (consumed by paddockbroker's
# StreamFrame decoder on /v1/runs/.../stream — empty Type makes the
# TUI client's smoke assertion fail) and a `kind` field (consumed by
# cmd/runtime-echo's convertLine, which maps kind → PaddockEvent.Type
# for the transcript). Mirrors the dual-purpose role of claude-code's
# `type` field and keeps the existing convertLine schema unchanged.
RUN cat <<'EOF' > /usr/local/bin/echo-interactive && chmod +x /usr/local/bin/echo-interactive
#!/bin/sh
trap 'INTERRUPTED=1' INT
while IFS= read -r line; do
  if [ -n "${INTERRUPTED:-}" ]; then
    INTERRUPTED=
    continue
  fi
  esc=$(printf '%s' "$line" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf '{"type":"message","kind":"message","text":"echo: %s"}\n' "$esc"
  printf '{"type":"result","kind":"result","summary":"echoed %d bytes","filesChanged":0}\n' "${#line}"
done
EOF

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/paddock-echo"]
