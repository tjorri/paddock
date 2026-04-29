#!/usr/bin/env bash
#
# cilium-probe-iptables-redirect.sh — verify whether iptables nat OUTPUT
# REDIRECT in pod netns fires under a given Cilium config.
#
# Used by Phase 1 of the cilium-compat work (Issue #79). Each invocation
# applies one Cilium config variant via `helm upgrade`, deploys a
# fixture pod that runs the production iptables-init then probes
# whether a curl from the pod actually lands on a netcat sink at
# :15001 (impersonating the proxy). PASS = sink saw the bytes; FAIL =
# the redirect was bypassed.
#
# Usage:
#   PROBE_VARIANT=<variant> ./hack/cilium-probe-iptables-redirect.sh
#
# Variants:
#   baseline-kpr-on        — control case (reproduces the bug)
#   socketLB-disabled      — --set socketLB.enabled=false
#   socketLB-hostns-only   — --set socketLB.hostNamespaceOnly=true
#   bpf-tproxy             — --set bpf.tproxy=true
#   kpr-off                — --set kubeProxyReplacement=false (sanity)
#
# NOTE (2026-04-28): Phase 1 of Issue #79 ran this script across all
# variants and got FAIL on every one. That outcome is a SINK-SIDE
# ARTIFACT, not a real iptables-bypass result. Busybox netcat's
# `nc -lk -p 15001 -e cat` exits after one connection, so subsequent
# curls land on a closed port. With a robust sink (Python http.server
# on :15001, no NetworkPolicy applied), iptables nat OUTPUT REDIRECT
# under Cilium-with-KPR works end-to-end. Issue #79's real root cause
# was the per-run NetworkPolicy missing a loopback allow rule. See
# docs/superpowers/plans/2026-04-28-cilium-compat-findings.md.
#
# This script is kept in-tree as scaffolding for future Cilium-config-
# variant probing; replace the busybox sink with a python http.server
# or ncat-based listener if you re-use it.

set -euo pipefail

command -v helm >/dev/null || { echo "helm not installed; install from https://helm.sh" >&2; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl not installed" >&2; exit 1; }
command -v docker >/dev/null || { echo "docker not installed" >&2; exit 1; }

CLUSTER_NAME="${CLUSTER_NAME:-paddock-dev}"
CILIUM_VERSION="${CILIUM_VERSION:-1.16.5}"
PROBE_VARIANT="${PROBE_VARIANT:-baseline-kpr-on}"
NS="${NS:-cilium-probe}"
POD_NAME="cilium-probe-$(date +%s)"
PROXY_PORT=15001
PROXY_UID=1337
TIMEOUT_SEC=10

usage() {
  sed -n '2,/^$/p' "$0" | sed 's/^# //; s/^#//'
  exit 2
}
case "${1:-}" in -h|--help) usage ;; esac

helm_args=(
  --version "${CILIUM_VERSION}"
  --namespace kube-system
  --set image.pullPolicy=IfNotPresent
  --set ipam.mode=kubernetes
)
case "${PROBE_VARIANT}" in
  baseline-kpr-on)
    helm_args+=(--set kubeProxyReplacement=true)
    ;;
  socketLB-disabled)
    helm_args+=(--set kubeProxyReplacement=true --set socketLB.enabled=false)
    ;;
  socketLB-hostns-only)
    helm_args+=(--set kubeProxyReplacement=true --set socketLB.hostNamespaceOnly=true)
    ;;
  bpf-tproxy)
    helm_args+=(--set kubeProxyReplacement=true --set bpf.tproxy=true)
    ;;
  kpr-off)
    helm_args+=(--set kubeProxyReplacement=false)
    ;;
  *)
    echo "unknown PROBE_VARIANT=${PROBE_VARIANT}" >&2
    usage
    ;;
esac

control_plane_node="${CLUSTER_NAME}-control-plane"
control_plane_ip=$(docker inspect "${control_plane_node}" \
  --format '{{ .NetworkSettings.Networks.kind.IPAddress }}' 2>/dev/null || true)
if [ -z "${control_plane_ip}" ]; then
  echo "could not find control-plane IP for ${control_plane_node}; is the cluster up? try 'make kind-up'" >&2
  exit 1
fi
helm_args+=(--set k8sServiceHost="${control_plane_ip}" --set k8sServicePort=6443)

helm repo add cilium https://helm.cilium.io >/dev/null 2>&1 || true
helm repo update cilium >/dev/null

echo ">>> applying cilium variant: ${PROBE_VARIANT}"
helm upgrade --install cilium cilium/cilium "${helm_args[@]}" --wait --timeout=10m

kubectl -n kube-system rollout restart daemonset cilium >/dev/null 2>&1 || true
kubectl -n kube-system rollout status daemonset cilium --timeout=5m

kubectl get ns "${NS}" >/dev/null 2>&1 || kubectl create ns "${NS}"

# The fixture pod has three containers:
#   - init "iptables-init": runs the production binary as the main
#     production flag set, in particular --bypass-uids matches what
#     the manager passes (1337,1338,1339).
#   - "sink": netcat listening on 15001 as the proxy UID (1337) so the
#     PADDOCK_OUTPUT chain's RETURN-by-uid rule lets it bind.
#   - "client": curl 127.0.0.1:80 → should be REDIRECT'd to 15001 →
#     should land on the sink.
cat <<POD | kubectl apply -n "${NS}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
  labels:
    app: cilium-probe
spec:
  restartPolicy: Never
  initContainers:
    - name: iptables-init
      image: paddock-iptables-init:dev
      imagePullPolicy: IfNotPresent
      args:
        - --bypass-uids=1337,1338,1339
        - --proxy-port=${PROXY_PORT}
        - --ports=80,443
      securityContext:
        capabilities:
          add: ["NET_ADMIN"]
  containers:
    - name: sink
      image: busybox:1.36
      command: ["sh", "-c", "nc -lk -p ${PROXY_PORT} -e cat > /tmp/sink.log 2>&1; sleep 30"]
      securityContext:
        runAsUser: ${PROXY_UID}
    - name: client
      image: curlimages/curl:8.5.0
      command:
        - sh
        - -c
        - |
          sleep 3
          curl -s -m 5 -o /dev/null -w '%{http_code}\n' http://example.com/ || true
          sleep 3
POD

echo ">>> waiting for pod"
if ! kubectl -n "${NS}" wait pod/${POD_NAME} --for=condition=Ready --timeout=60s; then
  echo "pod ${POD_NAME} did not become Ready within 60s; dumping diagnostics" >&2
  kubectl -n "${NS}" get pod "${POD_NAME}" -o wide >&2 || true
  kubectl -n "${NS}" describe pod "${POD_NAME}" >&2 || true
  echo "common cause: 'paddock-iptables-init:dev' not loaded into the kind cluster — run 'make docker-build && kind load docker-image paddock-iptables-init:dev'" >&2
  exit 1
fi

# Give the curl + sink time to interact; then assert sink saw bytes.
sleep "${TIMEOUT_SEC}"
RESULT="FAIL"
if kubectl -n "${NS}" exec "${POD_NAME}" -c sink -- cat /tmp/sink.log 2>/dev/null | grep -qiE 'GET|HTTP'; then
  RESULT="PASS"
fi

echo "----- iptables-init logs -----"
kubectl -n "${NS}" logs "${POD_NAME}" -c iptables-init || true
echo "----- client logs -----"
kubectl -n "${NS}" logs "${POD_NAME}" -c client || true
echo "----- sink dump -----"
kubectl -n "${NS}" exec "${POD_NAME}" -c sink -- cat /tmp/sink.log 2>/dev/null | head -20 || true

echo
echo "RESULT (${PROBE_VARIANT}): ${RESULT}"

kubectl -n "${NS}" delete pod "${POD_NAME}" --wait=false || true
