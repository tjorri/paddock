#!/usr/bin/env bash
# Test fixture: emits a "ready" event then sleeps forever, ignoring
# stdin EOF. Used to exercise the supervisor's bounded-shutdown path:
# closing this CLI's stdin will NOT cause it to exit, so the supervisor
# must escalate via SIGTERM/SIGKILL.
trap '' TERM  # ignore SIGTERM too — force the SIGKILL path
printf '{"type":"ready"}\n'
while true; do sleep 60; done
