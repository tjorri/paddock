# paddock-iptables-init — NET_ADMIN init container that installs the
# per-pod REDIRECT chain for transparent-mode egress interception.
# See ADR-0013 §7.2.
#
# Uses alpine as the base (not distroless) because we need the iptables
# userspace binary at runtime. Alpine's iptables is ~1 MB and drops no
# privileges we don't explicitly ask for.

FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
COPY cmd/iptables-init/ cmd/iptables-init/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o iptables-init ./cmd/iptables-init

FROM alpine:3.20
RUN apk add --no-cache iptables
COPY --from=builder /workspace/iptables-init /usr/local/bin/iptables-init
# Runs as root because it needs CAP_NET_ADMIN to modify iptables rules;
# the Pod's securityContext drops every capability except NET_ADMIN at
# container runtime.
ENTRYPOINT ["/usr/local/bin/iptables-init"]
