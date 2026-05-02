# images/harness-supervisor/Dockerfile
# paddock-harness-supervisor — bridges two Unix-domain sockets to a
# harness CLI's stdio. Built scratch (binary copy only) so harness
# images can `COPY --from=` it without pulling shell or libc.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
COPY cmd/harness-supervisor/ cmd/harness-supervisor/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o supervisor ./cmd/harness-supervisor

FROM scratch
COPY --from=builder /workspace/supervisor /supervisor
LABEL paddock.dev/role="harness-supervisor"
