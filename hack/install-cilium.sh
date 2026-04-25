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
command -v docker >/dev/null || { echo "docker not installed"; exit 1; }

kubectl config use-context "kind-${CLUSTER_NAME}" >/dev/null

if helm -n kube-system status cilium >/dev/null 2>&1; then
  echo "cilium already installed in kind-${CLUSTER_NAME} — skipping"
  exit 0
fi

# Cilium pods can't resolve the Kind control-plane container name via
# Docker DNS, so look up its IP on the Kind network and pass that as
# k8sServiceHost. The control-plane container is named
# `<cluster>-control-plane`; its IP lives in the `kind` Docker network.
CONTROL_PLANE_NODE="${CLUSTER_NAME}-control-plane"
CONTROL_PLANE_IP=$(docker inspect "${CONTROL_PLANE_NODE}" \
  --format '{{ .NetworkSettings.Networks.kind.IPAddress }}' 2>/dev/null || true)
if [ -z "${CONTROL_PLANE_IP}" ]; then
  echo "could not find control-plane IP for ${CONTROL_PLANE_NODE}; is the cluster up?"
  exit 1
fi

echo "installing cilium ${CILIUM_VERSION} into kind-${CLUSTER_NAME} (k8sServiceHost=${CONTROL_PLANE_IP})"

helm repo add cilium https://helm.cilium.io >/dev/null 2>&1 || true
helm repo update cilium >/dev/null

# Bump --timeout above the default 5m: arm64/Cilium-on-Kind can take
# 6-8m before all pods are Ready (image pulls + IPAM setup).
helm upgrade --install cilium cilium/cilium \
  --version "${CILIUM_VERSION}" \
  --namespace kube-system \
  --set image.pullPolicy=IfNotPresent \
  --set ipam.mode=kubernetes \
  --set kubeProxyReplacement=true \
  --set k8sServiceHost="${CONTROL_PLANE_IP}" \
  --set k8sServicePort=6443 \
  --wait --timeout=10m

echo "waiting for cilium pods to become Ready"
kubectl -n kube-system wait --for=condition=Ready --timeout=300s \
  -l k8s-app=cilium pods

echo "cilium ${CILIUM_VERSION} installed in kind-${CLUSTER_NAME}"
