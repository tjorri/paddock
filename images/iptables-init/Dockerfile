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

# Alpine 3.22, digest-pinned. Alpine 3.20 reached EOL on 2025-04-01
# and trivy reports "vulnerability detection may be insufficient" for
# EOL distributions. 3.22 matches the version used by the other
# alpine-based images in the repo (evil-echo, harness-e2e-egress,
# harness-echo). Digest pinning matches the security-critical posture
# set by defaultSeedImage in workspace_seed_helpers.go (the seed
# init container's alpine/git base is also digest-pinned).
# Captured 2026-04-27 from `docker buildx imagetools inspect alpine:3.22`.
# F-56.
FROM alpine@sha256:310c62b5e7ca5b08167e4384c68db0fd2905dd9c7493756d356e893909057601
RUN apk add --no-cache iptables
COPY --from=builder /workspace/iptables-init /usr/local/bin/iptables-init
# Runs as root because it needs CAP_NET_ADMIN to modify iptables rules;
# the Pod's securityContext drops every capability except NET_ADMIN at
# container runtime.
ENTRYPOINT ["/usr/local/bin/iptables-init"]
