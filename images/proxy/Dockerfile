# paddock-proxy — per-run egress proxy sidecar.
# Cooperative-mode L7 HTTPS MITM. Forges leaf certs against the run's
# MITM CA (projected via Secret), validates each CONNECT against the
# allow-list (M4) / broker (M7), and emits AuditEvents on denials.
# See ADR-0013.
#
# Built from the repo root (Go module context). Same buildx pattern as
# the other images.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum

COPY api/ api/
COPY cmd/proxy/ cmd/proxy/
COPY internal/proxy/ internal/proxy/
# internal/proxy/broker_client.go imports the broker's wire types
# (paddock.dev/paddock/internal/broker/api) to share request/response
# schemas with the broker binary without a separate contract package.
# It also imports providers.SubstituteResult / BasicAuth / Substituter
# for the substitution rewrite pipeline.
COPY internal/broker/api/ internal/broker/api/
COPY internal/broker/providers/ internal/broker/providers/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o proxy ./cmd/proxy

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/proxy .
USER 65532:65532

ENTRYPOINT ["/proxy"]
