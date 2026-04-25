#!/usr/bin/env bash
#
# install-cilium.sh — install Cilium CNI into a Kind cluster.
#
# Pinned to 1.16.x for reproducibility. Replaces kindnet + kube-proxy.
# Idempotent: re-running on an installed cluster is a no-op.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-paddock-dev}"
CILIUM_VERSION="${CILIUM_VERSION:-1.16.5}"

command -v helm >/dev/null || { echo "helm not installed; install from https://helm.sh"; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl not installed"; exit 1; }

kubectl config use-context "kind-${CLUSTER_NAME}" >/dev/null

if helm -n kube-system status cilium >/dev/null 2>&1; then
  echo "cilium already installed in kind-${CLUSTER_NAME} — skipping"
  exit 0
fi

echo "installing cilium ${CILIUM_VERSION} into kind-${CLUSTER_NAME}"

helm repo add cilium https://helm.cilium.io >/dev/null 2>&1 || true
helm repo update cilium >/dev/null

# Cilium needs to know the Kind node IP range to route correctly. The
# values below are Kind defaults; adjust if your Kind config diverges.
helm upgrade --install cilium cilium/cilium \
  --version "${CILIUM_VERSION}" \
  --namespace kube-system \
  --set image.pullPolicy=IfNotPresent \
  --set ipam.mode=kubernetes \
  --set kubeProxyReplacement=true \
  --set k8sServiceHost=kind-control-plane \
  --set k8sServicePort=6443 \
  --wait

echo "waiting for cilium pods to become Ready"
kubectl -n kube-system wait --for=condition=Ready --timeout=180s \
  -l k8s-app=cilium pods

echo "cilium ${CILIUM_VERSION} installed in kind-${CLUSTER_NAME}"
