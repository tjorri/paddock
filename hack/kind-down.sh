#!/usr/bin/env bash
#
# kind-down.sh — tear down the local Paddock Kind cluster.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-paddock-dev}"

if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  echo "deleting kind cluster '${CLUSTER_NAME}'"
  kind delete cluster --name "${CLUSTER_NAME}"
else
  echo "kind cluster '${CLUSTER_NAME}' does not exist — nothing to do"
fi
