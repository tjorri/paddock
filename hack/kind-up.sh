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
  if [ -n "${KIND_NO_CNI:-}" ]; then
    echo "creating kind cluster '${CLUSTER_NAME}' with default CNI (kindnet) — KIND_NO_CNI is set"
    kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_IMAGE}" --wait 60s
  else
    echo "creating kind cluster '${CLUSTER_NAME}' with Cilium CNI"
    kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_IMAGE}" \
      --config hack/kind-with-cilium.yaml --wait 60s
    hack/install-cilium.sh
  fi
fi

kubectl config use-context "kind-${CLUSTER_NAME}" >/dev/null

echo "installing cert-manager ${CERT_MANAGER_VERSION}"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"

echo "waiting for cert-manager to become ready"
kubectl -n cert-manager wait --for=condition=Available --timeout=180s \
  deployment/cert-manager deployment/cert-manager-webhook deployment/cert-manager-cainjector

# Phase 2f / F-18: the per-run intermediate ClusterIssuer (kind: CA)
# references the paddock-proxy-ca Secret in paddock-system. cert-manager
# defaults --cluster-resource-namespace to its own ns; patch it to
# paddock-system so the ClusterIssuer can read its source Secret.
echo "patching cert-manager --cluster-resource-namespace=paddock-system (F-18 / Phase 2f)"
kubectl -n cert-manager patch deployment cert-manager --type=json \
  --patch='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--cluster-resource-namespace=paddock-system"}]'
kubectl -n cert-manager rollout status deployment/cert-manager --timeout=120s

echo "kind cluster '${CLUSTER_NAME}' ready"
echo "next: tilt up"
