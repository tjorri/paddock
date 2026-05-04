#!/usr/bin/env bash
# Test fixture: emits a ready event, reads ONE stdin line, then exits 1.
# Used to exercise per-prompt-process's prompt-crashed event.
printf '{"type":"ready"}\n'
IFS= read -r line
exit 1
