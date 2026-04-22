# paddock-echo — deterministic harness image used by CI and e2e tests.
# Not an LLM. Reads PADDOCK_PROMPT_PATH, emits a fixed sequence of raw
# JSONL events to PADDOCK_RAW_PATH, writes a result.json, exits 0.
FROM alpine:3.20

RUN adduser -D -u 65532 paddock

COPY run.sh /usr/local/bin/paddock-echo
RUN chmod +x /usr/local/bin/paddock-echo

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/paddock-echo"]
