#!/usr/bin/env bash
# Computes a content hash of the source tree backing a paddock image.
# Used by Makefile image targets to skip docker build when nothing
# has changed since the last successful build.
#
# Args: image name (e.g. "broker", "manager", "echo")
# Output: 12-char hex hash

set -euo pipefail

image="${1:-}"
if [[ -z "$image" ]]; then
  echo "usage: image-hash.sh <image-name>" >&2
  exit 2
fi

# Per-image dependency lists. Update when an image starts importing
# a new internal/ package or moving its Dockerfile's COPY-set.
case "$image" in
  broker)
    deps="api cmd/broker internal/auditing internal/broker internal/policy images/broker go.mod go.sum" ;;
  proxy)
    deps="api cmd/proxy internal/auditing internal/proxy internal/broker/api internal/brokerclient images/proxy go.mod go.sum" ;;
  iptables-init)
    deps="cmd/iptables-init images/iptables-init go.mod go.sum" ;;
  echo)
    deps="images/harness-echo" ;;
  harness-supervisor)
    deps="cmd/harness-supervisor images/harness-supervisor go.mod go.sum" ;;
  evil-echo)
    deps="images/evil-echo internal/brokerclient go.mod go.sum" ;;
  runtime-claude-code)
    deps="api cmd/runtime-claude-code internal/runtime internal/broker/api internal/brokerclient internal/auditing images/runtime-claude-code go.mod go.sum" ;;
  runtime-echo)
    deps="api cmd/runtime-echo internal/runtime internal/broker/api internal/brokerclient internal/auditing images/runtime-echo go.mod go.sum" ;;
  claude-code)
    deps="images/harness-claude-code" ;;
  claude-code-fake)
    deps="images/harness-claude-code-fake" ;;
  e2e-egress)
    deps="images/harness-e2e-egress" ;;
  *)
    echo "unknown image: $image" >&2
    exit 2 ;;
esac

# Hash inputs: every file under each dep dir (or the dep file itself).
# Sorted for deterministic ordering.
{
  for d in $deps; do
    if [[ -d "$d" ]]; then
      find "$d" -type f -print0 | sort -z | xargs -0 shasum -a 256
    elif [[ -f "$d" ]]; then
      shasum -a 256 "$d"
    fi
  done
} | shasum -a 256 | head -c 12
