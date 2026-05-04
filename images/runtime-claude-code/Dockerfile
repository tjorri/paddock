# paddock-runtime-claude-code — per-run runtime sidecar for the
# claude-code harness. Owns input recording (PromptSubmitted into
# events.jsonl), output translation (claude stream-json ->
# PaddockEvent), transcript persistence on the workspace PVC,
# ConfigMap projection, stdout passthrough, and the broker HTTP+WS
# surface (interactive only).
#
# Built from the repo root so the Go module context is available.
# See the top-level Dockerfile for the rationale on
# --platform=$BUILDPLATFORM + cache mounts — same pattern applies
# here.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum

COPY api/ api/
COPY cmd/runtime-claude-code/ cmd/runtime-claude-code/
COPY internal/runtime/ internal/runtime/
COPY internal/broker/api/ internal/broker/api/
COPY internal/brokerclient/ internal/brokerclient/
COPY internal/auditing/ internal/auditing/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o runtime-claude-code ./cmd/runtime-claude-code

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/runtime-claude-code .
USER 65532:65532

LABEL paddock.dev/runtime-interactive-modes="per-prompt-process,persistent-process"

ENTRYPOINT ["/runtime-claude-code"]
