# paddock-claude-code — demo harness image wrapping Anthropic's
# upstream Claude Code CLI. Emits stream-json to $PADDOCK_RAW_PATH
# verbatim; the matching adapter (paddock-adapter-claude-code) turns
# that stream into PaddockEvents.
#
# The Claude Code CLI is installed at run time from Anthropic's
# downloads CDN (downloads.claude.ai), not at image build time.
# Operators pick the version via PADDOCK_CLAUDE_CODE_VERSION (accepts
# `latest` (default), `stable`, or an explicit X.Y.Z), so the same
# image works against any upstream release without a rebuild. The
# matching adapter has fixture tests pinned to a specific stream-json
# shape, though, so a major upstream bump may break parsing — pin
# PADDOCK_CLAUDE_CODE_VERSION to a known-good version in production.

ARG ALPINE_VERSION=3.22

FROM alpine:${ALPINE_VERSION}

# Baseline tooling:
#   bash              — run.sh uses bashisms ([[ ]], arrays, pipefail)
#   git               — agents routinely inspect / commit inside the workspace
#   jq                — extract final result.json from the stream-json tail;
#                       also lets the upstream bootstrap parse the manifest
#                       without falling back to a bash regex
#   curl              — fetch the Claude Code installer at run time
#   ca-certificates   — TLS roots for curl
#   libgcc, libstdc++ — runtime deps for the Claude Code binary on
#                       musl-based distros (per Anthropic's setup docs)
#   ripgrep           — Claude Code's bundled ripgrep is glibc-linked, so
#                       Alpine needs the system package + USE_BUILTIN_RIPGREP=0
RUN apk add --no-cache \
        bash git jq curl ca-certificates \
        libgcc libstdc++ ripgrep

RUN adduser -D -u 65532 paddock
ENV HOME=/home/paddock
ENV PATH=/home/paddock/.local/bin:$PATH
# Required by Claude Code on musl/Alpine; the bundled ripgrep is glibc-linked.
ENV USE_BUILTIN_RIPGREP=0
# Harness pods are ephemeral and we install fresh on every start —
# the in-process auto-updater would just be wasted work.
ENV DISABLE_AUTOUPDATER=1

COPY run.sh /usr/local/bin/paddock-claude-code
RUN chmod +x /usr/local/bin/paddock-claude-code

# Supervisor binary for interactive mode (per spec §7.2).
COPY --from=paddock-harness-supervisor:dev /supervisor /usr/local/bin/paddock-harness-supervisor

# Harness invocation contract for the supervisor (per spec §4.3).
ENV PADDOCK_HARNESS_BIN=/root/.local/bin/claude
ENV PADDOCK_HARNESS_ARGS_PERSISTENT="--input-format stream-json --output-format stream-json"
ENV PADDOCK_HARNESS_ARGS_PER_PROMPT="--print --input-format stream-json --output-format stream-json"

USER 65532:65532
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/paddock-claude-code"]
