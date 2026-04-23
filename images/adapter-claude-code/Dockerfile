# paddock-adapter-claude-code — event adapter sidecar for the
# claude-code harness. Tails claude's stream-json, translates each
# line into zero-or-more PaddockEvents, and appends them to the
# shared /paddock/events/events.jsonl for the collector to publish.
# Built from the repo root so the Go module context is available.
#
# See the top-level Dockerfile for the rationale on --platform=
# $BUILDPLATFORM + cache mounts — same pattern applies here.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum

COPY api/ api/
COPY cmd/adapter-claude-code/ cmd/adapter-claude-code/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o adapter-claude-code ./cmd/adapter-claude-code

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/adapter-claude-code .
USER 65532:65532

ENTRYPOINT ["/adapter-claude-code"]
