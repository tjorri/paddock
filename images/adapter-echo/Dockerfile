# paddock-adapter-echo — adapter sidecar for the echo harness. Tails
# PADDOCK_RAW_PATH, converts each line to a PaddockEvent, and appends
# to PADDOCK_EVENTS_PATH. Built from the repo root so the Go module
# context is available.
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY api/ api/
COPY cmd/adapter-echo/ cmd/adapter-echo/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o adapter-echo ./cmd/adapter-echo

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/adapter-echo .
USER 65532:65532

ENTRYPOINT ["/adapter-echo"]
