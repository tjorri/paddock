# paddock-runtime-echo — per-run runtime sidecar for the echo
# harness. Owns input recording (PromptSubmitted into events.jsonl),
# output translation (echo's tiny JSONL shape -> PaddockEvent),
# transcript persistence on the workspace PVC, ConfigMap projection,
# stdout passthrough, and (interactive-only) the broker HTTP+WS
# surface.
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
COPY cmd/runtime-echo/ cmd/runtime-echo/
COPY internal/runtime/ internal/runtime/
COPY internal/broker/api/ internal/broker/api/
COPY internal/brokerclient/ internal/brokerclient/
COPY internal/auditing/ internal/auditing/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o runtime-echo ./cmd/runtime-echo

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/runtime-echo .
USER 65532:65532

# Echo is a deterministic batch fixture today; interactive support is
# left for follow-up work (would require a harness-supervisor in the
# echo image to bridge UDS data + ctl sockets). The label is
# intentionally empty so the controller doesn't admit interactive
# Mode runs against this runtime.
LABEL paddock.dev/runtime-interactive-modes=""

ENTRYPOINT ["/runtime-echo"]
