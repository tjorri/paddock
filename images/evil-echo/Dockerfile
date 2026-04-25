# evil-echo — hostile harness for adversarial Paddock E2E.
#
# DO NOT USE AS A REAL HARNESS. This image deliberately attempts
# security-relevant operations (raw TCP egress, secret-file reads,
# broker probes) to validate Paddock's defences. Test-only.
#
# Multi-stage: Go binary built in stage 1, copied into a minimal
# alpine for runtime.

FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
COPY images/evil-echo/ ./images/evil-echo/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /workspace/bin/evil-echo ./images/evil-echo/

FROM alpine:3.22

RUN adduser -D -u 65532 paddock
COPY --from=builder /workspace/bin/evil-echo /usr/local/bin/evil-echo

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/evil-echo"]
