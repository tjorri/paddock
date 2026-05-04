# A minimal harness that mimics claude's stream-json contract for e2e
# tests. Reads NDJSON from stdin, echoes a synthetic "assistant" event
# per input on stdout, exits on EOF. Does not call any external API
# and does not install anything at run time, so the e2e suite can
# exercise the broker -> adapter -> UDS -> supervisor -> CLI -> UDS
# -> adapter -> broker WS path without external network or API budget.
FROM alpine:3.22

# Supervisor binary for interactive mode.
COPY --from=paddock-harness-supervisor:dev /supervisor /usr/local/bin/paddock-harness-supervisor

COPY run.sh /usr/local/bin/paddock-claude-code-fake
RUN chmod +x /usr/local/bin/paddock-claude-code-fake

ENV PADDOCK_HARNESS_BIN=/usr/local/bin/fake-claude
ENV PADDOCK_HARNESS_ARGS_PERSISTENT=""
ENV PADDOCK_HARNESS_ARGS_PER_PROMPT=""

# Inline fake harness CLI: emits a well-formed stream-json
# "assistant" event per input line so the adapter's convertLine
# parser (cmd/adapter-claude-code/convert.go) translates it to a
# Message PaddockEvent. The fake's output schema must match the
# real claude --output-format stream-json shape: assistant.message
# is an object with .role and .content[] where each content block
# has .type ("text" | "tool_use" | ...) and a type-specific body.
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
done
EOF

ENTRYPOINT ["/usr/local/bin/paddock-claude-code-fake"]
