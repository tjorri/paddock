# A minimal harness that mimics claude's stream-json contract for e2e
# tests. Reads NDJSON from stdin, echoes a synthetic "assistant" event
# per input on stdout, exits on EOF. Does not call any external API
# and does not install anything at run time, so the e2e suite can
# exercise the broker -> adapter -> UDS -> supervisor -> CLI -> UDS
# -> adapter -> broker WS path without external network or API budget.
#
# Build context is the repo root (not images/harness-claude-code-fake)
# so the supervisor binary can be built inline from
# cmd/harness-supervisor — see harness-echo/Dockerfile for the
# rationale.

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

# Supervisor binary for interactive mode.
COPY --from=supervisor-builder /supervisor /usr/local/bin/paddock-harness-supervisor

COPY images/harness-claude-code-fake/run.sh /usr/local/bin/paddock-claude-code-fake
RUN chmod +x /usr/local/bin/paddock-claude-code-fake

ENV PADDOCK_HARNESS_BIN=/usr/local/bin/fake-claude
ENV PADDOCK_HARNESS_ARGS_PERSISTENT=""
ENV PADDOCK_HARNESS_ARGS_PER_PROMPT=""

# Inline fake harness CLI: emits a well-formed stream-json
# "assistant" event followed by a turn-terminating "result" event per
# input line so the runtime's convertLine parser
# (cmd/runtime-claude-code/convert.go) translates them to Message +
# Result PaddockEvents. The fake's output schema must match the real
# claude --output-format stream-json shape: assistant.message is an
# object with .role and .content[] where each content block has .type
# ("text" | "tool_use" | ...) and a type-specific body; result frames
# carry .subtype and .num_turns so the runtime's OnTurnComplete fires.
# The trap on SIGINT lets the supervisor's per-prompt-process mode
# exercise its interrupt path without the fake CLI dying outright.
RUN cat <<'EOF' > /usr/local/bin/fake-claude && chmod +x /usr/local/bin/fake-claude
#!/bin/sh
trap 'printf "%s\n" "{\"type\":\"interrupted\"}"; INTERRUPTED=1' INT
while IFS= read -r line; do
  if [ -n "${INTERRUPTED:-}" ]; then
    INTERRUPTED=
    continue
  fi
  printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"fake reply"}]}}\n'
  printf '{"type":"result","subtype":"success","num_turns":1,"is_error":false}\n'
done
EOF

ENTRYPOINT ["/usr/local/bin/paddock-claude-code-fake"]
