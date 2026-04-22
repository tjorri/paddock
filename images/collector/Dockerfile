# paddock-collector — generic collector sidecar. Tails raw + events
# JSONL, persists both to the workspace PVC, and publishes a ring
# buffer + result.json to an owned ConfigMap (ADR-0005, ADR-0007).
# Built from the repo root so the Go module context is available.
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY api/ api/
COPY cmd/collector/ cmd/collector/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o collector ./cmd/collector

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/collector .
USER 65532:65532

ENTRYPOINT ["/collector"]
