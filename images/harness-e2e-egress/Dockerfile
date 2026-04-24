# paddock-e2e-egress — e2e-only harness image.
#
# Ships just enough tooling (curl + ca-certificates) to drive the v0.3
# proxy scenarios: loop HTTPS requests through HTTPS_PROXY so the M12
# Kind suite can verify egress-allow / egress-block AuditEvents and the
# BrokerPolicy-delete-mid-run transition. Not a real harness — the
# run.sh emits echo-compatible events so paddock-adapter-echo can
# translate them unchanged.
FROM alpine:3.22

RUN apk add --no-cache curl ca-certificates \
    && adduser -D -u 65532 paddock

COPY run.sh /usr/local/bin/paddock-e2e-egress
RUN chmod +x /usr/local/bin/paddock-e2e-egress

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/paddock-e2e-egress"]
