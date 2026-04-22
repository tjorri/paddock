#!/usr/bin/env bash
#
# kind-up.sh — provision a local Kind cluster for Paddock development.
#
# Idempotent: re-running with an existing cluster is a no-op (plus cert-manager
# reconciliation). Intended to be the first command a contributor runs.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-paddock-dev}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.2}"
KIND_IMAGE="${KIND_IMAGE:-kindest/node:v1.31.2}"

command -v kind >/dev/null || { echo "kind not installed"; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl not installed"; exit 1; }

if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  echo "kind cluster '${CLUSTER_NAME}' already exists — skipping creation"
else
  echo "creating kind cluster '${CLUSTER_NAME}' (${KIND_IMAGE})"
  kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_IMAGE}" --wait 60s
fi

kubectl config use-context "kind-${CLUSTER_NAME}" >/dev/null

echo "installing cert-manager ${CERT_MANAGER_VERSION}"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"

echo "waiting for cert-manager to become ready"
kubectl -n cert-manager wait --for=condition=Available --timeout=180s \
  deployment/cert-manager deployment/cert-manager-webhook deployment/cert-manager-cainjector

echo "kind cluster '${CLUSTER_NAME}' ready"
echo "next: tilt up"
