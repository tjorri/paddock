#!/usr/bin/env bash
# Test fixture: behaves like a stream-json harness CLI. Echoes each
# stdin line back as an "assistant" event on stdout. Handles SIGINT
# by emitting an "interrupted" event but stays alive (matches the
# persistent-process contract). Exits 0 on stdin EOF.
#
# Emits a "ready" event on startup, after installing the SIGINT
# trap, so tests can wait for the trap to be live before sending
# an interrupt (avoids a race where SIGINT arrives before bash has
# parsed the `trap` line and bash dies with the default action).
trap 'printf "{\"type\":\"interrupted\"}\n"' INT
printf '{"type":"ready"}\n'
while IFS= read -r line; do
  printf '{"type":"assistant","message":%s}\n' "$line"
done
