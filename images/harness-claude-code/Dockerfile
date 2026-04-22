# paddock-claude-code — demo harness image wrapping Anthropic's
# upstream Claude Code CLI. Emits stream-json to $PADDOCK_RAW_PATH
# verbatim; the matching adapter (paddock-adapter-claude-code) turns
# that stream into PaddockEvents.
#
# Pins the Claude CLI version because the stream-json schema is not
# stable across majors (see ADR-0001). Bump CLAUDE_VERSION + rerun
# the adapter's fixture tests whenever this pin changes.

ARG NODE_VERSION=22-alpine
ARG CLAUDE_VERSION=2.1.117

FROM node:${NODE_VERSION}

# Baseline tooling:
#   bash — the run.sh wrapper uses bashisms ([[ ]], arrays, pipefail)
#   git  — agents routinely inspect / commit inside the workspace
#   jq   — extract final result.json from the stream-json tail
RUN apk add --no-cache bash git jq

ARG CLAUDE_VERSION
RUN npm install -g --omit=dev @anthropic-ai/claude-code@${CLAUDE_VERSION} \
 && npm cache clean --force

RUN adduser -D -u 65532 paddock
ENV HOME=/home/paddock

COPY run.sh /usr/local/bin/paddock-claude-code
RUN chmod +x /usr/local/bin/paddock-claude-code

USER 65532:65532
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/paddock-claude-code"]
