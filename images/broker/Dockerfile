# paddock-broker — issues short-lived, scoped credentials to runs.
# Holds upstream secrets (API keys, PAT pools); authenticates callers
# via TokenReview with audience=paddock-broker. See ADR-0012.
#
# Built from the repo root (Go module context). Same buildx pattern
# as the other images — BUILDPLATFORM for the toolchain, TARGETOS/
# TARGETARCH for cross-compile, cached go mod + build directories.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum

COPY api/ api/
COPY cmd/broker/ cmd/broker/
COPY internal/auditing/ internal/auditing/
COPY internal/broker/ internal/broker/
COPY internal/policy/ internal/policy/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o broker ./cmd/broker

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/broker .
USER 65532:65532

ENTRYPOINT ["/broker"]
