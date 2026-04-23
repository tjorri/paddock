# Build the manager binary.
#
# --platform=$BUILDPLATFORM pins the builder stage to the build host's
# native arch (amd64 on GitHub runners) so we never QEMU-emulate the
# Go toolchain. Go's in-tree cross-compiler produces the $TARGETARCH
# binary natively via GOOS/GOARCH, which is ~10x faster than running
# `go build` under QEMU. The final distroless stage is left without
# --platform so buildx pulls the correct per-arch base image.
FROM --platform=$BUILDPLATFORM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Copy the Go Modules manifests first so dependency downloads layer
# separately from source-change invalidations.
COPY go.mod go.mod
COPY go.sum go.sum

# Copy the Go source (relies on .dockerignore to filter).
COPY . .

# Build with persistent BuildKit cache mounts for the Go module cache
# and the Go build cache. Combined with the workflow's GHA layer cache,
# warm-cache rebuilds are dominated by `go build` on actually-changed
# packages only. `-a` is deliberately dropped: it forces rebuilding
# every standard-library package on each invocation, which defeats
# the build cache.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o manager cmd/main.go

# Distroless base matches $TARGETPLATFORM automatically. The binary
# copied from the builder already matches the requested arch because
# we cross-compiled above.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
