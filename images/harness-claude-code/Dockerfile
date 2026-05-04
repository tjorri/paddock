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

# Build context is the repo root (not images/harness-claude-code) so
# the supervisor binary can be built inline from cmd/harness-supervisor.
# Inlining avoids a cross-image COPY --from that would require a
# preexisting :dev tag in the local docker daemon — which is fine for
# `make image-claude-code` but breaks buildx jobs in CI that have no
# such tag.

ARG ALPINE_VERSION=3.22

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

COPY images/harness-claude-code/run.sh /usr/local/bin/paddock-claude-code
RUN chmod +x /usr/local/bin/paddock-claude-code

# Supervisor binary for interactive mode (per spec §7.2).
COPY --from=supervisor-builder /supervisor /usr/local/bin/paddock-harness-supervisor

# Harness invocation contract for the supervisor (per spec §4.3).
ENV PADDOCK_HARNESS_BIN=/home/paddock/.local/bin/claude
ENV PADDOCK_HARNESS_ARGS_PERSISTENT="--input-format stream-json --output-format stream-json --verbose"
# --verbose is REQUIRED by claude when --print is combined with
# --output-format=stream-json. Without it claude exits with
# "Error: When using --print, --output-format=stream-json requires --verbose"
# before any prompt is read. Verified against claude-code 2.1.x.
ENV PADDOCK_HARNESS_ARGS_PER_PROMPT="--print --input-format stream-json --output-format stream-json --verbose"

USER 65532:65532
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/paddock-claude-code"]
