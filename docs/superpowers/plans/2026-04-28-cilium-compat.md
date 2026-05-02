# Cilium Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix Issue #79 — Cilium-with-KPR compatibility — so HarnessRuns
with non-empty `requires` succeed on `make kind-up`. Resolves both the
kube-apiserver-classification bug (Issue A) and the iptables-REDIRECT
silent-bypass bug (Issue B).

**Architecture:** Two phases. Phase 1 runs probes against a real
Cilium-on-Kind cluster (load-bearing scripted, others prose) to pick
fix branches. Phase 2 implements the chosen branches plus the e2e
regression test, ADR-0013 update, and quickstart cleanup. The plan
below assumes the most-likely Phase 1 outcome — `A-FIX-toEntities` +
`B-FIX-cooperative-downgrade` — and explicitly flags branch points so
divergent outcomes redirect cleanly without rewriting the whole plan.

**Tech Stack:** Go 1.x, Kubebuilder/controller-runtime, Cilium
1.16.5, Kind, Ginkgo v2, helm, iptables, BPF (background only).

**Spec:** `docs/superpowers/specs/2026-04-28-cilium-compat-design.md`

**Branch:** `fix/cilium-compat` (already created, spec already
committed).

**Reference files:**
- `internal/controller/network_policy.go` — current per-run NP builder.
- `internal/controller/cni_probe.go` — current CNI presence probe (NP
  enforcement path); will be extended for cilium-config detection.
- `internal/controller/harnessrun_controller.go:1414+` —
  `resolveInterceptionMode` wrapper. CNI-incompat downgrade is layered
  here (not in `internal/policy/interception_mode.go`, which stays a
  pure policy resolver).
- `internal/policy/interception_mode.go` — pure-logic resolver returning
  `InterceptionDecision`. Extended with `MinInterceptionMode` handling
  in §5.2 territory.
- `internal/auditing/builders.go` + `internal/controller/audit.go` —
  builders + emit helpers for AuditKinds.
- `api/v1alpha1/auditevent_types.go` — `AuditKind` constants.
- `api/v1alpha1/brokerpolicy_types.go` — `InterceptionSpec`. Gains a
  `MinInterceptionMode` field.
- `cmd/main.go` — controller-manager startup wiring.
- `hack/install-cilium.sh` — helm install of Cilium for `make kind-up`.
- `docs/contributing/adr/0013-proxy-interception-modes.md` — ADR.
- `docs/getting-started/quickstart.md` — has the `KIND_NO_CNI=1`
  workaround note to remove.

**Branch-point glossary** (resolved in Phase 1):

- **A-FIX-toEntities** (most likely): emit a CNP with
  `toEntities: [kube-apiserver, remote-node]` when CNP CRDs are
  present. Tasks 8–13.
- **A-FIX-toCIDR**: emit a CNP with `toCIDR` for resolved control-plane
  node IPs. If selected, swap Tasks 10–11's CNP shape for the toCIDR
  variant (same plumbing).
- **A-FIX-cluster-config**: doc-only Cilium config tweak. If selected,
  delete Tasks 8–13 entirely; add a single doc-update task patching
  `hack/install-cilium.sh` and quickstart.
- **B-FIX-cooperative-downgrade** (most likely): admission detects
  KPR=true → resolves transparent → cooperative; emits AuditEvent;
  `MinInterceptionMode=transparent` rejects rather than downgrades.
  Tasks 14–22.
- **B-FIX-cilium-knob**: a Cilium config flag preserves transparent
  mode. If selected, swap Tasks 14–22 for a smaller set: update
  `install-cilium.sh`, helm chart NOTES, and an observability-only
  audit emit on incompatible config. Detailed branch design lives in
  the spec §5.2; the writing-plans skill should be re-invoked to
  flesh that branch out if Phase 1 selects it.

---

## Phase 1 — Investigation

> **Reminder:** Phase 1 is a 1–2 day box per spec §4.5. If a probe
> doesn't resolve in one work session, mark it Blocked in the findings
> doc and pick the next-most-preferable branch from spec §4.5.

### Task 1: Findings doc skeleton

**Files:**
- Create: `docs/superpowers/plans/2026-04-28-cilium-compat-findings.md`

- [ ] **Step 1: Create findings doc skeleton**

```bash
cat > docs/superpowers/plans/2026-04-28-cilium-compat-findings.md <<'EOF'
# Cilium Compatibility — Phase 1 Findings (Issue #79)

- Date: 2026-04-28
- Owner: @tjorri
- Spec: `docs/superpowers/specs/2026-04-28-cilium-compat-design.md`

## Cluster under test

- Cluster: `paddock-dev` (from `make kind-up`).
- Cilium: 1.16.5, `kubeProxyReplacement=true`, `routing-mode=tunnel`,
  `tunnel-protocol=vxlan`, `bpf-lb-sock=false`.
- Kind config: `hack/kind-with-cilium.yaml`.

## A-1 — kube-apiserver identity classification

- **Hypothesis:** the apiserver is not classified as Cilium identity
  `kube-apiserver` in this config.
- **Procedure:** TBD (filled at run time)
- **Result:** TBD
- **Decides:** TBD

## A-2 — CNP toEntities: [kube-apiserver, remote-node]

- **Hypothesis:** including `remote-node` covers the host-network
  static apiserver pod where `kube-apiserver` alone does not.
- **Procedure:** TBD
- **Result:** TBD
- **Decides:** TBD

## A-3 — CNP toCIDR control-plane node IP

- **Hypothesis:** CNP `toCIDR` enforces against host-network targets
  even when standard NP `ipBlock` does not.
- **Procedure:** TBD
- **Result:** TBD
- **Decides:** TBD

## A-4 — Cluster-config: policy-cidr-match-mode + node label

- **Hypothesis:** flipping `policy-cidr-match-mode=cidr+nodes` plus
  labelling control-plane node makes the Phase 2d ipBlock rule fire.
- **Procedure:** TBD
- **Result:** TBD
- **Decides:** TBD

## B-1 — iptables REDIRECT under Cilium config variants

- **Hypothesis:** some Cilium config variant lets pod-netns iptables
  `nat OUTPUT` REDIRECT fire while keeping KPR=true.
- **Procedure:** `hack/cilium-probe-iptables-redirect.sh PROBE_VARIANT=<...>`
- **Result:** TBD per variant
- **Decides:** B-FIX-cilium-knob (any variant passes) vs.
  B-FIX-cooperative-downgrade (all KPR variants fail; baseline `kpr-off` passes).

## B-2 — Cooperative HTTPS_PROXY sanity under Cilium-with-KPR

- **Hypothesis:** cooperative-mode env-var path works on Cilium-with-KPR
  (cooperative is supposed to be CNI-agnostic).
- **Procedure:** TBD
- **Result:** TBD
- **Decides:** confirmation that B-FIX-cooperative-downgrade is viable
  if B-1 fails.

## Selected fix branches

- **Issue A:** TBD (one of A-FIX-toEntities, A-FIX-toCIDR, A-FIX-cluster-config)
- **Issue B:** TBD (one of B-FIX-cilium-knob, B-FIX-cooperative-downgrade)
- **Rationale:** TBD

## Decision: CNI mode (B4) deferred to v1.0

Captured here for the audit trail: even if B-FIX-cooperative-downgrade
ships, CNI mode remains the structural answer for hostile-tenant
posture under Cilium-with-KPR. Reasoning: TBD per probe results.
EOF
```

- [ ] **Step 2: Commit the skeleton**

```bash
git add docs/superpowers/plans/2026-04-28-cilium-compat-findings.md
git commit -m "docs(plan): cilium-compat phase-1 findings skeleton (#79)"
```

---

### Task 2: Probe script — `hack/cilium-probe-iptables-redirect.sh`

**Files:**
- Create: `hack/cilium-probe-iptables-redirect.sh`

The script applies a Cilium config variant via `helm upgrade`, deploys
a fixture pod that runs the production `iptables-init` binary plus a
netcat sink at `:15001`, and asserts traffic actually lands on the
sink. The fixture pod uses real images (`paddock-iptables-init:dev`)
already built locally for `make kind-up` / e2e flows.

- [ ] **Step 1: Write the script**

```bash
cat > hack/cilium-probe-iptables-redirect.sh <<'PROBE_EOF'
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

set -euo pipefail

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
  --format '{{ .NetworkSettings.Networks.kind.IPAddress }}')
helm_args+=(--set k8sServiceHost="${control_plane_ip}" --set k8sServicePort=6443)

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
kubectl -n "${NS}" wait pod/${POD_NAME} --for=condition=Ready --timeout=60s || true

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
PROBE_EOF
chmod +x hack/cilium-probe-iptables-redirect.sh
```

- [ ] **Step 2: Lint-pass — bash -n**

Run: `bash -n hack/cilium-probe-iptables-redirect.sh`
Expected: no output (parse OK).

- [ ] **Step 3: Smoke-test the script's argument parsing**

Run: `PROBE_VARIANT=bogus hack/cilium-probe-iptables-redirect.sh 2>&1 | head -3`
Expected: prints `unknown PROBE_VARIANT=bogus` and exits non-zero.

- [ ] **Step 4: Commit**

```bash
git add hack/cilium-probe-iptables-redirect.sh
git commit -m "build(hack): cilium iptables-redirect probe script (#79)"
```

---

### Task 3: Run Issue A probes

> Probes A-1 through A-4. Run each in sequence; stop at the first that
> resolves the question. Record the procedure (commands run) and result
> (verbatim outputs) in the findings doc as you go. Don't skip A-1
> even if you suspect A-2 will pass — the identity-list output is
> diagnostic context for whichever fix branch lands.

**Files:**
- Modify: `docs/superpowers/plans/2026-04-28-cilium-compat-findings.md` (fill A-1..A-4 sections)

- [ ] **Step 1: Confirm cluster is up with the baseline Cilium config**

```bash
make kind-up
kubectl -n kube-system get cm cilium-config -o jsonpath='{.data.kube-proxy-replacement}'; echo
```
Expected: `true`.

- [ ] **Step 2: A-1 — list Cilium identities for the apiserver host**

```bash
CILIUM_POD=$(kubectl -n kube-system get pod -l k8s-app=cilium -o name | head -1)
kubectl -n kube-system exec "${CILIUM_POD}" -- cilium-dbg identity list 2>&1 | tee /tmp/cilium-identity.txt | head -40
kubectl -n kube-system exec "${CILIUM_POD}" -- cilium-dbg endpoint list -o jsonpath='{range .[*]}{.id}{"\t"}{.identity.id}{"\t"}{.identity.labels}{"\n"}{end}' 2>&1 | tee /tmp/cilium-endpoints.txt | head -40
```

Record the output in the findings doc under A-1.
Expected: shows whether the apiserver / control-plane node has the
`reserved:kube-apiserver`, `reserved:remote-node`, or `reserved:host`
label.

- [ ] **Step 3: A-2 — apply CNP with toEntities, test apiserver reachability**

```bash
kubectl create ns cnp-probe || true
cat <<EOF | kubectl -n cnp-probe apply -f -
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: probe-allow-apiserver
spec:
  endpointSelector:
    matchLabels:
      probe: a2
  egress:
    - toEntities: [kube-apiserver, remote-node]
    - toPorts:
        - ports: [{port: "53", protocol: UDP}]
      toEndpoints:
        - matchLabels:
            "k8s:io.kubernetes.pod.namespace": kube-system
            "k8s:k8s-app": kube-dns
EOF

kubectl -n cnp-probe run probe-a2 --image=curlimages/curl:8.5.0 --restart=Never \
  --labels=probe=a2 -- sh -c 'sleep 3; curl -s -m 5 -o /dev/null -w "%{http_code} %{time_connect}\n" -k https://10.96.0.1:443/'
sleep 10
kubectl -n cnp-probe logs probe-a2
```

Record output in findings under A-2.
**Pass criterion:** any TCP-level response within 1s (e.g.
`401 0.012`). **Fail criterion:** `000` and timeout.

- [ ] **Step 4: A-3 — if A-2 failed, try CNP toCIDR with control-plane node IP**

If A-2 passed, skip this step. Otherwise:

```bash
NODE_IP=$(kubectl get node -l node-role.kubernetes.io/control-plane \
  -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
cat <<EOF | kubectl -n cnp-probe apply -f -
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: probe-allow-apiserver
spec:
  endpointSelector:
    matchLabels:
      probe: a3
  egress:
    - toCIDR: ["${NODE_IP}/32"]
    - toPorts:
        - ports: [{port: "53", protocol: UDP}]
      toEndpoints:
        - matchLabels:
            "k8s:io.kubernetes.pod.namespace": kube-system
            "k8s:k8s-app": kube-dns
EOF

kubectl -n cnp-probe delete pod probe-a2 --ignore-not-found
kubectl -n cnp-probe run probe-a3 --image=curlimages/curl:8.5.0 --restart=Never \
  --labels=probe=a3 -- sh -c 'sleep 3; curl -s -m 5 -o /dev/null -w "%{http_code} %{time_connect}\n" -k https://10.96.0.1:443/'
sleep 10
kubectl -n cnp-probe logs probe-a3
```

Record in findings under A-3.

- [ ] **Step 5: A-4 — if both A-2 and A-3 failed, try cluster-config tweak**

If either A-2 or A-3 passed, skip this step. Otherwise:

```bash
kubectl label node "${CLUSTER_NAME:-paddock-dev}-control-plane" \
  node.kubernetes.io/exclude-from-external-load-balancers=true --overwrite
kubectl -n kube-system patch cm cilium-config --type=merge \
  -p '{"data":{"policy-cidr-match-mode":"cidr+nodes"}}'
kubectl -n kube-system rollout restart ds cilium
kubectl -n kube-system rollout status ds cilium --timeout=5m

kubectl -n cnp-probe delete pod probe-a3 --ignore-not-found
kubectl -n cnp-probe delete cnp probe-allow-apiserver --ignore-not-found

cat <<EOF | kubectl -n cnp-probe apply -f -
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: probe-allow-apiserver
spec:
  podSelector:
    matchLabels: {probe: a4}
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock: {cidr: 10.96.0.1/32}
      ports:
        - {protocol: TCP, port: 443}
    - to:
        - namespaceSelector:
            matchLabels: {kubernetes.io/metadata.name: kube-system}
          podSelector:
            matchLabels: {k8s-app: kube-dns}
      ports:
        - {protocol: UDP, port: 53}
EOF
kubectl -n cnp-probe run probe-a4 --image=curlimages/curl:8.5.0 --restart=Never \
  --labels=probe=a4 -- sh -c 'sleep 3; curl -s -m 5 -o /dev/null -w "%{http_code} %{time_connect}\n" -k https://10.96.0.1:443/'
sleep 10
kubectl -n cnp-probe logs probe-a4
```

Record outputs and reset the cilium-config back to defaults afterward
(remove the `policy-cidr-match-mode` data key) so subsequent probes
are not contaminated.

- [ ] **Step 6: Clean up**

```bash
kubectl delete ns cnp-probe --wait=false
```

- [ ] **Step 7: Commit findings updates so far**

```bash
git add docs/superpowers/plans/2026-04-28-cilium-compat-findings.md
git commit -m "docs(plan): cilium-compat — issue A probe results (#79)"
```

---

### Task 4: Run Issue B probes (B-1 via the script)

**Files:**
- Modify: `docs/superpowers/plans/2026-04-28-cilium-compat-findings.md` (fill B-1 section)

- [ ] **Step 1: Run each probe variant in sequence; record outputs**

```bash
for v in baseline-kpr-on socketLB-disabled socketLB-hostns-only bpf-tproxy kpr-off; do
  echo "=== ${v} ==="
  PROBE_VARIANT="${v}" ./hack/cilium-probe-iptables-redirect.sh 2>&1 | tee "/tmp/probe-${v}.log"
  echo
done
```

- [ ] **Step 2: Tabulate results in findings**

Append to B-1 section in the findings doc:

```markdown
| Variant | Result | Notes |
| --- | --- | --- |
| `baseline-kpr-on` | (PASS/FAIL) | |
| `socketLB-disabled` | (PASS/FAIL) | |
| `socketLB-hostns-only` | (PASS/FAIL) | |
| `bpf-tproxy` | (PASS/FAIL) | |
| `kpr-off` | (PASS/FAIL) | |
```

Fill in actual values from the `/tmp/probe-*.log` outputs. The
`baseline-kpr-on` variant must FAIL to confirm the bug reproduces;
`kpr-off` must PASS to confirm the harness works at all.

- [ ] **Step 3: Reset Cilium to project default**

```bash
make cleanup-test-e2e || true
make kind-down && make kind-up
```

(The probe script left the cluster on whichever variant ran last;
re-up restores the project-standard install.)

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/plans/2026-04-28-cilium-compat-findings.md
git commit -m "docs(plan): cilium-compat — issue B probe results (#79)"
```

---

### Task 5: B-2 cooperative-mode HTTPS_PROXY sanity probe

**Files:**
- Modify: `docs/superpowers/plans/2026-04-28-cilium-compat-findings.md`

- [ ] **Step 1: Deploy a fixture pod exercising cooperative mode**

```bash
kubectl create ns coop-probe
cat <<EOF | kubectl -n coop-probe apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: coop-probe
spec:
  restartPolicy: Never
  containers:
    - name: proxy-sink
      image: busybox:1.36
      command: ["sh","-c","nc -lk -p 15001 -e cat > /tmp/sink.log 2>&1; sleep 30"]
      securityContext: {runAsUser: 1337}
    - name: client
      image: curlimages/curl:8.5.0
      env:
        - {name: HTTPS_PROXY, value: "http://localhost:15001"}
        - {name: HTTP_PROXY,  value: "http://localhost:15001"}
      command: ["sh","-c","sleep 3; curl -s -m 5 -o /dev/null -w '%{http_code}\\n' https://example.com/ || true; sleep 5"]
EOF
sleep 12
kubectl -n coop-probe logs coop-probe -c client
kubectl -n coop-probe exec coop-probe -c proxy-sink -- cat /tmp/sink.log | head -3
kubectl delete ns coop-probe --wait=false
```

- [ ] **Step 2: Record finding**

In the findings doc B-2 section, record whether the sink saw the
`CONNECT example.com:443` line. Expected: PASS — the cooperative
env-var path is CNI-agnostic.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/plans/2026-04-28-cilium-compat-findings.md
git commit -m "docs(plan): cilium-compat — cooperative-mode sanity (#79)"
```

---

### Task 6: Decide fix branches; write Selected branches section

**Files:**
- Modify: `docs/superpowers/plans/2026-04-28-cilium-compat-findings.md` (Selected fix branches section)

- [ ] **Step 1: Apply spec §4.5 preference order**

Per spec preference:
- Issue A: `A-FIX-toEntities > A-FIX-toCIDR > A-FIX-cluster-config`.
- Issue B: `B-FIX-cilium-knob > B-FIX-cooperative-downgrade`.

Pick the highest-preference branch that passed.

- [ ] **Step 2: Fill the Selected fix branches section**

Replace the TBDs with the chosen branches and a one-paragraph
rationale. If both Issue B knob variants and `kpr-off` passed, prefer
the knob with the smallest blast radius (fewest sub-features
disabled): `socketLB-disabled` < `socketLB-hostns-only` <
`bpf-tproxy`.

- [ ] **Step 3: Decide whether to continue with the rest of this plan**

If the selection is **A-FIX-toEntities + B-FIX-cooperative-downgrade**
(the most-likely outcome the rest of the plan was written for),
proceed to Task 7. Otherwise:
- Pause at this checkpoint.
- Note in the findings doc which branches were picked and which
  tasks need substitution. The branch-point glossary in the plan
  header (above) names the substitutions.
- Re-invoke the writing-plans skill with the findings + the chosen
  branches as input to flesh out the swapped tasks before continuing.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/plans/2026-04-28-cilium-compat-findings.md
git commit -m "docs(plan): cilium-compat — selected fix branches (#79)"
```

---

### Task 7: Phase 1 wrap

**Files:** none

- [ ] **Step 1: Confirm Phase 1 outputs in tree**

```bash
ls docs/superpowers/plans/2026-04-28-cilium-compat-findings.md
ls hack/cilium-probe-iptables-redirect.sh
git log --oneline fix/cilium-compat ^main | head -10
```

Expected: findings doc + script present; commits include skeleton +
script + 3 findings updates (Issue A, Issue B, cooperative sanity) +
selected branches.

- [ ] **Step 2: Open a draft PR for Phase 1 review (optional)**

If the user wants to land Phase 1 separately for review, open a draft
PR scoped to the spec + findings + script changes. Otherwise continue
to Phase 2 on the same branch.

---

## Phase 2 — Implementation

> **Default branches assumed below: A-FIX-toEntities + B-FIX-cooperative-downgrade.**
> If Phase 1 selected different branches, see the branch-point glossary
> in the plan header.

### Task 8: Add Cilium CNP CRD presence detector

Detection avoids importing the Cilium types directly. We use the
discovery API to ask the apiserver whether `cilium.io/v2` resources
exist; if `CiliumNetworkPolicy` is among them, CNPs can be created
with `unstructured.Unstructured`.

**Files:**
- Modify: `internal/controller/cni_probe.go` (add new function)
- Modify: `internal/controller/cni_probe_test.go` (add test)

- [ ] **Step 1: Write the failing test**

In `internal/controller/cni_probe_test.go`, append:

```go
func TestDetectCiliumCNP(t *testing.T) {
	cases := []struct {
		name      string
		resources []*metav1.APIResourceList
		want      bool
	}{
		{
			name: "cnp present",
			resources: []*metav1.APIResourceList{
				{
					GroupVersion: "cilium.io/v2",
					APIResources: []metav1.APIResource{
						{Name: "ciliumnetworkpolicies", Kind: "CiliumNetworkPolicy"},
					},
				},
			},
			want: true,
		},
		{
			name:      "no cilium group",
			resources: nil,
			want:      false,
		},
		{
			name: "cilium group present but no CNP kind",
			resources: []*metav1.APIResourceList{
				{
					GroupVersion: "cilium.io/v2",
					APIResources: []metav1.APIResource{
						{Name: "ciliumendpoints", Kind: "CiliumEndpoint"},
					},
				},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDiscovery{resources: tc.resources}
			got, err := DetectCiliumCNP(fake)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// fakeDiscovery implements just enough of discovery.DiscoveryInterface
// for DetectCiliumCNP. ServerResourcesForGroupVersion is the only
// method exercised; ServerVersion is required by the interface but
// returns a zero value here.
type fakeDiscovery struct {
	resources []*metav1.APIResourceList
}

func (f *fakeDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	for _, r := range f.resources {
		if r.GroupVersion == gv {
			return r, nil
		}
	}
	return nil, &errors.StatusError{ErrStatus: metav1.Status{
		Code:   http.StatusNotFound,
		Reason: metav1.StatusReasonNotFound,
	}}
}

// (Other discovery.DiscoveryInterface methods elided — only the one
// above is reachable from DetectCiliumCNP.)
```

Add imports needed to top of file: `"net/http"`, `"k8s.io/apimachinery/pkg/api/errors"`, `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestDetectCiliumCNP -v`
Expected: FAIL — `undefined: DetectCiliumCNP`.

- [ ] **Step 3: Implement DetectCiliumCNP**

Append to `internal/controller/cni_probe.go`:

```go
// CiliumNetworkPolicyDiscovery is the minimum subset of
// discovery.DiscoveryInterface this package uses. Defined locally so
// tests can supply a fake without dragging in client-go's full fake
// discovery client.
type CiliumNetworkPolicyDiscovery interface {
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
}

// CiliumGroupVersion is the API group/version that hosts
// CiliumNetworkPolicy. Stable across Cilium 1.x.
const CiliumGroupVersion = "cilium.io/v2"

// DetectCiliumCNP reports whether the cluster has the
// CiliumNetworkPolicy resource registered. Used at controller-manager
// startup; callers fall back to standard NetworkPolicy when this
// returns false.
//
// Treats group-not-found as "not Cilium" rather than an error: most
// non-Cilium clusters do not register cilium.io/v2 at all.
func DetectCiliumCNP(d CiliumNetworkPolicyDiscovery) (bool, error) {
	list, err := d.ServerResourcesForGroupVersion(CiliumGroupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("discovery for %s: %w", CiliumGroupVersion, err)
	}
	for _, r := range list.APIResources {
		if r.Kind == "CiliumNetworkPolicy" {
			return true, nil
		}
	}
	return false, nil
}
```

Add `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"` and `apierrors "k8s.io/apimachinery/pkg/api/errors"` imports if not already present.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestDetectCiliumCNP -v`
Expected: PASS, all three sub-tests.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/cni_probe.go internal/controller/cni_probe_test.go
git commit -m "feat(controller): detect CiliumNetworkPolicy CRD presence (#79)"
```

---

### Task 9: Wire CNP detection into controller-manager startup

**Files:**
- Modify: `cmd/main.go` (call DetectCiliumCNP at startup; pass to reconciler)
- Modify: `internal/controller/proxybroker_config.go` (add CiliumCNPAvailable field)
- Modify: `internal/controller/harnessrun_controller.go` (add field on reconciler)

- [ ] **Step 1: Add field to ProxyBrokerConfig**

Find the `ProxyBrokerConfig` struct in `internal/controller/proxybroker_config.go` and add a new field after `APIServerIPs`:

```go
	// CiliumCNPAvailable reports whether the cluster has the
	// CiliumNetworkPolicy CRD registered. Set at controller-manager
	// startup via DetectCiliumCNP. When true, ensureRunNetworkPolicy
	// emits a CiliumNetworkPolicy variant; when false, it emits a
	// standard NetworkPolicy. F-? / Issue #79.
	CiliumCNPAvailable bool
```

- [ ] **Step 2: Add corresponding field on HarnessRunReconciler**

In `internal/controller/harnessrun_controller.go`, find the `HarnessRunReconciler` struct and add:

```go
	// CiliumCNPAvailable: see ProxyBrokerConfig.CiliumCNPAvailable.
	CiliumCNPAvailable bool
```

If the reconciler is wired via the shared ProxyBrokerConfig (check
`cmd/main.go` for the assignment), thread the field through the same
way the existing `NetworkPolicyAutoEnabled` field is plumbed.

- [ ] **Step 3: Wire detection in cmd/main.go**

Locate the `DetectNetworkPolicyCNI` call site in `cmd/main.go`
(currently around line 324) and add CNP detection right after it:

```go
	cnpAvailable, err := controller.DetectCiliumCNP(
		discovery.NewDiscoveryClientForConfigOrDie(cfg),
	)
	if err != nil {
		setupLog.Error(err, "CNP discovery failed; falling back to standard NetworkPolicy")
		cnpAvailable = false
	}
	setupLog.Info("CiliumNetworkPolicy detection complete", "available", cnpAvailable)
```

Imports: add `"k8s.io/client-go/discovery"`.

Then add `CiliumCNPAvailable: cnpAvailable,` to the `proxyBrokerCfg`
literal where `APIServerIPs:` is set.

- [ ] **Step 4: Run unit tests**

Run: `go test ./internal/controller/ -count=1 -v ./...`
Expected: PASS (no behavior change yet beyond plumbing).

- [ ] **Step 5: Build the manager binary**

Run: `go build ./cmd/main.go`
Expected: clean build.

- [ ] **Step 6: Commit**

```bash
git add cmd/main.go internal/controller/proxybroker_config.go internal/controller/harnessrun_controller.go
git commit -m "feat(controller): plumb CNP availability from startup to reconciler (#79)"
```

---

### Task 10: Build CNP via unstructured.Unstructured

**Files:**
- Create: `internal/controller/cilium_network_policy.go`
- Create: `internal/controller/cilium_network_policy_test.go`

Build the CNP variant using `unstructured.Unstructured` so we don't
take a dependency on Cilium's Go types. The CNP shape mirrors the
standard NP egress rules plus the `toEntities` block.

- [ ] **Step 1: Write the failing test**

Create `internal/controller/cilium_network_policy_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildCiliumEgressPolicy_HasKubeApiserverAndRemoteNodeEntities(t *testing.T) {
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
		BrokerNamespace:    "paddock-system",
		BrokerPort:         8443,
	}
	cnp := buildCiliumEgressPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"paddock.dev/run": "demo"}},
		"demo-egress",
		"tenant",
		map[string]string{"app.kubernetes.io/name": "paddock"},
		cfg,
	)
	if cnp.GetAPIVersion() != "cilium.io/v2" || cnp.GetKind() != "CiliumNetworkPolicy" {
		t.Fatalf("apiVersion/kind: %s/%s", cnp.GetAPIVersion(), cnp.GetKind())
	}
	egress, _, err := unstructured.NestedSlice(cnp.Object, "spec", "egress")
	if err != nil {
		t.Fatalf("read egress: %v", err)
	}
	var foundEntities, foundDNS, foundBroker bool
	for _, raw := range egress {
		rule, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if ents, has, _ := unstructured.NestedStringSlice(rule, "toEntities"); has {
			seen := map[string]bool{}
			for _, e := range ents {
				seen[e] = true
			}
			if seen["kube-apiserver"] && seen["remote-node"] {
				foundEntities = true
			}
		}
		if eps, has, _ := unstructured.NestedSlice(rule, "toEndpoints"); has && len(eps) > 0 {
			ep0, _ := eps[0].(map[string]interface{})
			if ml, _, _ := unstructured.NestedStringMap(ep0, "matchLabels"); ml["k8s-app"] == "kube-dns" {
				foundDNS = true
			}
			if ml, _, _ := unstructured.NestedStringMap(ep0, "matchLabels"); ml["app.kubernetes.io/component"] == "broker" {
				foundBroker = true
			}
		}
	}
	if !foundEntities {
		t.Errorf("missing toEntities: [kube-apiserver, remote-node]")
	}
	if !foundDNS {
		t.Errorf("missing kube-dns rule")
	}
	if !foundBroker {
		t.Errorf("missing broker rule")
	}
}

func TestBuildCiliumEgressPolicy_NoBrokerRuleWhenNamespaceEmpty(t *testing.T) {
	cfg := networkPolicyConfig{}
	cnp := buildCiliumEgressPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"x": "y"}},
		"x", "y", nil, cfg,
	)
	egress, _, _ := unstructured.NestedSlice(cnp.Object, "spec", "egress")
	for _, raw := range egress {
		rule, _ := raw.(map[string]interface{})
		if eps, has, _ := unstructured.NestedSlice(rule, "toEndpoints"); has && len(eps) > 0 {
			ep0, _ := eps[0].(map[string]interface{})
			if ml, _, _ := unstructured.NestedStringMap(ep0, "matchLabels"); ml["app.kubernetes.io/component"] == "broker" {
				t.Fatalf("broker rule emitted with empty namespace")
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestBuildCiliumEgressPolicy -v`
Expected: FAIL — `undefined: buildCiliumEgressPolicy`.

- [ ] **Step 3: Implement buildCiliumEgressPolicy**

Create `internal/controller/cilium_network_policy.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// CiliumNetworkPolicyGVK is the GroupVersionKind for cilium.io/v2
// CiliumNetworkPolicy. Used to construct unstructured.Unstructured
// objects without taking a Go-type dependency on the Cilium API.
var CiliumNetworkPolicyGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2",
	Kind:    "CiliumNetworkPolicy",
}

// ciliumEgressRulesAdditional builds the Cilium-specific egress rules
// that go beyond the standard NetworkPolicy shape, namely the
// toEntities allow rule covering the kube-apiserver (regardless of
// whether it is reachable as a host-network static pod or via a
// dedicated apiserver deployment). See ADR-0013 §"Issue #79 update"
// and spec §5.1 A-FIX-toEntities.
func ciliumEgressRulesAdditional() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"toEntities": []interface{}{"kube-apiserver", "remote-node"},
		},
	}
}

// buildCiliumEgressPolicy mirrors buildEgressNetworkPolicy's rule set
// in CNP shape. The CNP differs from the standard NP in three places:
// (1) `egress` rule shape uses Cilium's matchers (toEntities,
// toEndpoints, toCIDR) instead of NetworkPolicyPeer; (2) the
// kube-apiserver rule uses `toEntities: [kube-apiserver, remote-node]`
// instead of an ipBlock allow-list; (3) selector key names mirror
// Cilium's k8s:* prefix in matchLabels for namespace and pod
// selectors.
//
// The function returns *unstructured.Unstructured so the controller
// does not take a Go-type dependency on cilium.io/v2.
func buildCiliumEgressPolicy(
	selector metav1.LabelSelector,
	name, namespace string,
	labels map[string]string,
	cfg networkPolicyConfig,
) *unstructured.Unstructured {
	rules := []interface{}{
		// DNS to kube-dns.
		map[string]interface{}{
			"toEndpoints": []interface{}{
				map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"k8s:io.kubernetes.pod.namespace": "kube-system",
						"k8s-app":                         "kube-dns",
					},
				},
			},
			"toPorts": []interface{}{
				map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{"port": "53", "protocol": "UDP"},
						map[string]interface{}{"port": "53", "protocol": "TCP"},
					},
				},
			},
		},
		// Public-internet 443/80 with cluster-CIDR exclusions encoded
		// as toCIDR + toCIDRSet.except. Mirrors buildExceptCIDRs().
		map[string]interface{}{
			"toCIDRSet": ciliumPublicCIDRSet(cfg),
			"toPorts": []interface{}{
				map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{"port": "443", "protocol": "TCP"},
						map[string]interface{}{"port": "80", "protocol": "TCP"},
					},
				},
			},
		},
	}
	// Broker (if configured).
	if cfg.BrokerNamespace != "" {
		port := cfg.BrokerPort
		if port == 0 {
			port = 8443
		}
		rules = append(rules, map[string]interface{}{
			"toEndpoints": []interface{}{
				map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"k8s:io.kubernetes.pod.namespace": cfg.BrokerNamespace,
						"app.kubernetes.io/component":     "broker",
						"app.kubernetes.io/name":          "paddock",
					},
				},
			},
			"toPorts": []interface{}{
				map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{
							"port":     fmt.Sprintf("%d", port),
							"protocol": "TCP",
						},
					},
				},
			},
		})
	}
	// kube-apiserver / remote-node entity rule (the heart of A-FIX-toEntities).
	rules = append(rules, ciliumEgressRulesAdditional()...)

	cnp := &unstructured.Unstructured{}
	cnp.SetGroupVersionKind(CiliumNetworkPolicyGVK)
	cnp.SetName(name)
	cnp.SetNamespace(namespace)
	cnp.SetLabels(labels)
	endpointSelector := map[string]interface{}{
		"matchLabels": stringMapToInterface(selector.MatchLabels),
	}
	_ = unstructured.SetNestedField(cnp.Object, endpointSelector, "spec", "endpointSelector")
	_ = unstructured.SetNestedSlice(cnp.Object, rules, "spec", "egress")
	return cnp
}

func stringMapToInterface(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ciliumPublicCIDRSet(cfg networkPolicyConfig) []interface{} {
	excepts := buildExceptCIDRs(cfg)
	exceptIface := make([]interface{}, 0, len(excepts))
	for _, e := range excepts {
		exceptIface = append(exceptIface, e)
	}
	return []interface{}{
		map[string]interface{}{
			"cidr":   "0.0.0.0/0",
			"except": exceptIface,
		},
	}
}

// buildRunCiliumNetworkPolicy renders the CNP variant of the per-run
// egress policy. Selector matches the run pod label
// (paddock.dev/run=<name>); rule list mirrors buildRunNetworkPolicy.
func buildRunCiliumNetworkPolicy(run *paddockv1alpha1.HarnessRun, cfg networkPolicyConfig) *unstructured.Unstructured {
	return buildCiliumEgressPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"paddock.dev/run": run.Name}},
		runNetworkPolicyName(run.Name),
		run.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "harnessrun-egress",
			"paddock.dev/run":             run.Name,
		},
		cfg,
	)
}

// buildSeedCiliumNetworkPolicy mirrors buildSeedNetworkPolicy.
func buildSeedCiliumNetworkPolicy(ws *paddockv1alpha1.Workspace, cfg networkPolicyConfig) *unstructured.Unstructured {
	return buildCiliumEgressPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"paddock.dev/workspace": ws.Name}},
		seedNetworkPolicyName(ws),
		ws.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "workspace-seed-egress",
			"paddock.dev/workspace":       ws.Name,
		},
		cfg,
	)
}
```

Add `"fmt"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/controller/ -run TestBuildCiliumEgressPolicy -v`
Expected: PASS (both sub-tests).

- [ ] **Step 5: Commit**

```bash
git add internal/controller/cilium_network_policy.go internal/controller/cilium_network_policy_test.go
git commit -m "feat(controller): build CiliumNetworkPolicy variant with toEntities (#79)"
```

---

### Task 11: Branch ensureRunNetworkPolicy on CiliumCNPAvailable

**Files:**
- Modify: `internal/controller/network_policy.go` (add CNP-aware path)
- Modify: `internal/controller/network_policy_test.go` (cover both branches)

- [ ] **Step 1: Write the failing test**

In `internal/controller/network_policy_test.go`, add a test that
constructs a HarnessRunReconciler with `CiliumCNPAvailable=true` and
asserts the resource emitted is a CNP, not a standard NP. Use
`controller-runtime/pkg/client/fake` builder; add the CNP GVK to the
scheme so the fake client can store it as unstructured.

```go
func TestEnsureRunNetworkPolicy_EmitsCNPWhenAvailable(t *testing.T) {
	scheme := newControllerTestScheme(t)

	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "tenant"},
		Status: paddockv1alpha1.HarnessRunStatus{
			NetworkPolicyEnforced: ptr.To(true),
			ObservedGeneration:    1,
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		Build()

	r := &HarnessRunReconciler{
		Client: cli,
		Scheme: scheme,
		Audit:  &ControllerAudit{Sink: nil}, // nil-sink is the no-op shape; see TestControllerAudit_NilSink_NoOp
		// network_policy_test.go already constructs reconcilers with
		// these fields; mirror them here for parity.
		ClusterPodCIDR:        "10.244.0.0/16",
		ClusterServiceCIDR:    "10.96.0.0/12",
		BrokerNamespace:       "paddock-system",
		BrokerPort:            8443,
		CiliumCNPAvailable:    true,
	}

	if err := r.ensureRunNetworkPolicy(context.Background(), run); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	cnpList := &unstructured.UnstructuredList{}
	cnpList.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicyList",
	})
	if err := cli.List(context.Background(), cnpList); err != nil {
		t.Fatalf("list cnp: %v", err)
	}
	if len(cnpList.Items) != 1 {
		t.Fatalf("expected 1 CNP, got %d", len(cnpList.Items))
	}

	npList := &networkingv1.NetworkPolicyList{}
	if err := cli.List(context.Background(), npList); err != nil {
		t.Fatalf("list np: %v", err)
	}
	if len(npList.Items) != 0 {
		t.Fatalf("expected 0 standard NetworkPolicy, got %d", len(npList.Items))
	}
}
```

(`ptr.To` is `k8s.io/utils/ptr.To`, widely used in this package. The
`Audit` field accepts a nil `Sink`; the existing test
`TestControllerAudit_NilSink_NoOp` confirms that's a no-op.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestEnsureRunNetworkPolicy_EmitsCNPWhenAvailable -v`
Expected: FAIL — current code only emits standard NetworkPolicy.

- [ ] **Step 3: Branch ensureRunNetworkPolicy**

In `internal/controller/network_policy.go`, modify `ensureRunNetworkPolicy`:

```go
func (r *HarnessRunReconciler) ensureRunNetworkPolicy(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	if run.Status.NetworkPolicyEnforced == nil || !*run.Status.NetworkPolicyEnforced {
		return nil
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     r.ClusterPodCIDR,
		ClusterServiceCIDR: r.ClusterServiceCIDR,
		BrokerNamespace:    r.BrokerNamespace,
		BrokerPort:         r.BrokerPort,
		APIServerIPs:       r.APIServerIPs,
	}
	if r.CiliumCNPAvailable {
		return r.ensureRunCiliumNetworkPolicy(ctx, run, cfg)
	}
	desired := buildRunNetworkPolicy(run, cfg)
	// (existing standard-NP body unchanged below this line)
	np := &networkingv1.NetworkPolicy{ ... }  // unchanged
	// ...
}
```

Add a new sibling function:

```go
// ensureRunCiliumNetworkPolicy is the CNP-emitting counterpart to
// ensureRunNetworkPolicy. CreateOrUpdate semantics mirror the standard-NP
// path, including the F-43 audit emit on a re-create.
func (r *HarnessRunReconciler) ensureRunCiliumNetworkPolicy(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	cfg networkPolicyConfig,
) error {
	desired := buildRunCiliumNetworkPolicy(run, cfg)
	cnp := &unstructured.Unstructured{}
	cnp.SetGroupVersionKind(CiliumNetworkPolicyGVK)
	cnp.SetName(desired.GetName())
	cnp.SetNamespace(desired.GetNamespace())

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, cnp, func() error {
		if err := controllerutil.SetControllerReference(run, cnp, r.Scheme); err != nil {
			return err
		}
		cnp.SetLabels(desired.GetLabels())
		// Replace spec with desired spec (whole-spec swap; CreateOrUpdate
		// does not deep-merge unstructured.Unstructured for us).
		spec, _, _ := unstructured.NestedMap(desired.Object, "spec")
		return unstructured.SetNestedMap(cnp.Object, spec, "spec")
	})
	if err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("upserting run CiliumNetworkPolicy: %w", err)
	}
	if op == controllerutil.OperationResultCreated && run.Status.ObservedGeneration > 0 {
		r.Audit.EmitNetworkPolicyEnforcementWithdrawn(ctx, run.Name, run.Namespace,
			fmt.Sprintf("per-run CiliumNetworkPolicy %s was missing on reconcile; re-created", desired.GetName()))
	}
	return nil
}
```

Add imports: `"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"`. (Note:
`SetNestedField`, `NestedMap` may need the `runtime` package's
`unstructured` — verify by running the tests.)

- [ ] **Step 4: Mirror the same branching in deleteRunNetworkPolicy**

The existing `deleteRunNetworkPolicy` only deletes standard NPs.
Extend it to also try deleting a CNP if `r.CiliumCNPAvailable` is
true. The empty-`requires` skip path (Phase 2d, removed in main but
still in some empty-template flows) calls this on cleanup:

```go
func (r *HarnessRunReconciler) deleteRunNetworkPolicy(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	key := client.ObjectKey{Namespace: run.Namespace, Name: runNetworkPolicyName(run.Name)}
	// Try standard NP.
	var np networkingv1.NetworkPolicy
	if err := r.Get(ctx, key, &np); err == nil {
		if delErr := r.Delete(ctx, &np); delErr != nil && !apierrors.IsNotFound(delErr) {
			return delErr
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	// Try CNP (idempotent: NotFound is fine).
	if r.CiliumCNPAvailable {
		cnp := &unstructured.Unstructured{}
		cnp.SetGroupVersionKind(CiliumNetworkPolicyGVK)
		if err := r.Get(ctx, key, cnp); err == nil {
			if delErr := r.Delete(ctx, cnp); delErr != nil && !apierrors.IsNotFound(delErr) {
				return delErr
			}
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Apply the same branching to ensureSeedNetworkPolicy**

Find `ensureSeedNetworkPolicy` (or the workspace-seed equivalent) and
mirror Steps 3 + 4 using `buildSeedCiliumNetworkPolicy`. If the seed
function doesn't exist as a sibling, look in
`internal/controller/workspace_*.go` for the equivalent.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/controller/ -count=1 -v`
Expected: all pre-existing tests still pass; new
`TestEnsureRunNetworkPolicy_EmitsCNPWhenAvailable` passes.

- [ ] **Step 7: Commit**

```bash
git add internal/controller/network_policy.go internal/controller/network_policy_test.go
git commit -m "feat(controller): emit CiliumNetworkPolicy when CNP CRDs available (#79)"
```

---

### Task 12: Extend Owns() watch to CiliumNetworkPolicy

When the controller emits a CNP, mid-run kubectl-delete of the CNP
must trigger reconcile and re-create (matches the F-41 NP behaviour).

**Files:**
- Modify: `internal/controller/harnessrun_controller.go` (SetupWithManager)
- Modify: `internal/controller/workspace_controller.go` (SetupWithManager) if seed CNPs are also emitted

- [ ] **Step 1: Write the failing test**

The existing `network_policy_test.go` (or `harnessrun_controller_test.go`)
likely has a "delete-out-from-under" envtest. Mirror it for CNP. If
no envtest exists for the existing NP path, skip Steps 1–2 and rely
on the e2e test in Task 23 to cover the watch behaviour.

- [ ] **Step 2: Add Owns(CNP) when CiliumCNPAvailable**

In `HarnessRunReconciler.SetupWithManager`, find the existing
`Owns(&networkingv1.NetworkPolicy{})` line (added in F-41 / Phase 2d)
and append:

```go
	if r.CiliumCNPAvailable {
		cnp := &unstructured.Unstructured{}
		cnp.SetGroupVersionKind(CiliumNetworkPolicyGVK)
		bldr = bldr.Owns(cnp)
	}
```

(Where `bldr` is the existing builder chain; adapt to the local
variable name.)

Mirror the same change in the workspace reconciler if it emits a seed
CNP.

- [ ] **Step 3: Build + run unit tests**

Run: `go build ./... && go test ./internal/controller/ -count=1 -v`
Expected: clean build; tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/harnessrun_controller.go internal/controller/workspace_controller.go
git commit -m "feat(controller): watch CiliumNetworkPolicy for re-converge (#79)"
```

---

### Task 13: Drop apiserver-IP allow rule when CNP path is used

The standard NP's apiserver-IP `ipBlock` rule (Phase 2d) is redundant
when the CNP `toEntities: [kube-apiserver, remote-node]` rule covers
the same destination. Drop it in the CNP builder to avoid two
overlapping rules.

The CNP builder already does NOT emit an ipBlock for apiserver IPs
(see Task 10). This task documents the decision and removes any
dead-code remnants.

**Files:**
- Modify: `internal/controller/cilium_network_policy.go` (no behaviour
  change; comment cleanup if needed)

- [ ] **Step 1: Verify the CNP builder ignores cfg.APIServerIPs**

Run: `grep -n "APIServerIPs" internal/controller/cilium_network_policy.go`
Expected: no matches. The CNP path should not consume APIServerIPs at
all; entities cover them.

If the field is used, remove its consumption. The intent: standard-NP
path keeps the ipBlock rule (operators on non-Cilium clusters benefit
from Phase 2d's discovery); CNP path uses entities.

- [ ] **Step 2: Add a doc comment to the CNP builder**

Append above `buildCiliumEgressPolicy`:

```go
// Note: the CNP path intentionally does NOT consume cfg.APIServerIPs.
// The toEntities rule covers the kube-apiserver regardless of how its
// IP set rotates. Standard-NP path retains the ipBlock allow-list as
// a defence-in-depth fallback.
```

- [ ] **Step 3: Commit**

```bash
git add internal/controller/cilium_network_policy.go
git commit -m "docs(controller): explain why CNP path skips apiserver IP allow-list (#79)"
```

---

### Task 14: Detect Cilium kube-proxy-replacement at admission

**Files:**
- Modify: `internal/controller/cni_probe.go` (new function)
- Modify: `internal/controller/cni_probe_test.go` (new test)

- [ ] **Step 1: Write the failing test**

Append to `cni_probe_test.go`:

```go
func TestDetectCiliumKubeProxyReplacement(t *testing.T) {
	cases := []struct {
		name string
		cm   *corev1.ConfigMap
		want bool
	}{
		{
			name: "absent → not cilium → false",
			cm:   nil,
			want: false,
		},
		{
			name: "present, kpr=true → true",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cilium-config", Namespace: "kube-system"},
				Data:       map[string]string{"kube-proxy-replacement": "true"},
			},
			want: true,
		},
		{
			name: "present, kpr=false → false",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cilium-config", Namespace: "kube-system"},
				Data:       map[string]string{"kube-proxy-replacement": "false"},
			},
			want: false,
		},
		{
			name: "present, kpr key missing → false",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cilium-config", Namespace: "kube-system"},
				Data:       map[string]string{"identity-allocation-mode": "crd"},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			builder := fake.NewClientBuilder()
			if tc.cm != nil {
				builder = builder.WithObjects(tc.cm)
			}
			cli := builder.Build()
			got, err := DetectCiliumKubeProxyReplacement(context.Background(), cli)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestDetectCiliumKubeProxyReplacement -v`
Expected: FAIL — `undefined: DetectCiliumKubeProxyReplacement`.

- [ ] **Step 3: Implement DetectCiliumKubeProxyReplacement**

Append to `cni_probe.go`:

```go
// CiliumConfigMapName is the standard ConfigMap name for Cilium's
// runtime configuration. We read this once at admission to decide
// whether transparent-mode iptables interception will work.
const CiliumConfigMapName = "cilium-config"

// CiliumKPRKey is the data key in cilium-config that records whether
// kube-proxy-replacement (KPR) is enabled. Cilium's BPF datapath
// intercepts pod-netns connect() before iptables nat OUTPUT runs when
// this is true; the controller treats that combination as
// transparent-incompatible.
const CiliumKPRKey = "kube-proxy-replacement"

// DetectCiliumKubeProxyReplacement reports whether the cluster is
// running Cilium with kube-proxy-replacement enabled. Reads
// kube-system/cilium-config; absence means "not Cilium" (returns
// false, no error). Other read errors propagate.
//
// Called from the controller's resolveInterceptionMode wrapper at
// admission. See spec §5.4.
func DetectCiliumKubeProxyReplacement(ctx context.Context, c client.Reader) (bool, error) {
	var cm corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: KubeSystemNamespace, Name: CiliumConfigMapName}, &cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s/%s: %w", KubeSystemNamespace, CiliumConfigMapName, err)
	}
	return cm.Data[CiliumKPRKey] == "true", nil
}
```

Imports: ensure `apierrors "k8s.io/apimachinery/pkg/api/errors"` is
present.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestDetectCiliumKubeProxyReplacement -v`
Expected: PASS, four sub-tests.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/cni_probe.go internal/controller/cni_probe_test.go
git commit -m "feat(controller): detect cilium kube-proxy-replacement (#79)"
```

---

### Task 15: Add new AuditKind + builder + emit helper

**Files:**
- Modify: `api/v1alpha1/auditevent_types.go` (add constant)
- Modify: `internal/auditing/builders.go` (add builder)
- Modify: `internal/auditing/builders_test.go` (test the builder)
- Modify: `internal/controller/audit.go` (add emit helper)

- [ ] **Step 1: Add the AuditKind constant**

In `api/v1alpha1/auditevent_types.go`, add after
`AuditKindInterceptionModeCooperativeAccepted`:

```go
	// AuditKindInterceptionModeCiliumIncompatibilityDowngrade is emitted
	// at admission for every HarnessRun whose transparent-mode request
	// is auto-downgraded to cooperative due to Cilium-with-KPR
	// incompatibility (Issue #79). Carries the detected cilium-config
	// keys for the audit trail.
	AuditKindInterceptionModeCiliumIncompatibilityDowngrade AuditKind = "interception-mode-cilium-incompatibility-downgrade"
```

- [ ] **Step 2: Run kubebuilder code-gen**

Run: `make manifests generate`
Expected: clean (no schema-affecting change since AuditKind is a
string alias).

- [ ] **Step 3: Write failing builder test**

In `internal/auditing/builders_test.go`, append:

```go
func TestNewInterceptionModeCiliumIncompatibilityDowngrade(t *testing.T) {
	ev := NewInterceptionModeCiliumIncompatibilityDowngrade(InterceptionModeCiliumIncompatibilityDowngradeInput{
		RunName:   "demo",
		Namespace: "tenant",
		Reason:    "kube-proxy-replacement=true",
	})
	if ev.Spec.Kind != paddockv1alpha1.AuditKindInterceptionModeCiliumIncompatibilityDowngrade {
		t.Errorf("kind: %s", ev.Spec.Kind)
	}
	if ev.Spec.Decision != paddockv1alpha1.AuditDecisionWarned {
		t.Errorf("decision: %s", ev.Spec.Decision)
	}
	if ev.Spec.RunRef == nil || ev.Spec.RunRef.Name != "demo" {
		t.Errorf("run ref: %+v", ev.Spec.RunRef)
	}
	if ev.Namespace != "tenant" {
		t.Errorf("namespace: %s", ev.Namespace)
	}
	if !strings.Contains(ev.Spec.Reason, "kube-proxy-replacement=true") {
		t.Errorf("reason: %s", ev.Spec.Reason)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/auditing/ -run TestNewInterceptionModeCiliumIncompatibilityDowngrade -v`
Expected: FAIL — undefined.

- [ ] **Step 5: Implement the builder**

In `internal/auditing/builders.go`, find an existing builder
(e.g. `NewNetworkPolicyEnforcementWithdrawn`) for the canonical shape,
then append:

```go
// InterceptionModeCiliumIncompatibilityDowngradeInput carries the
// fields the AuditEvent needs. RunName/Namespace identify the run;
// Reason describes the detected cilium-config state (e.g.
// "kube-proxy-replacement=true").
type InterceptionModeCiliumIncompatibilityDowngradeInput struct {
	RunName   string
	Namespace string
	Reason    string
}

// NewInterceptionModeCiliumIncompatibilityDowngrade returns the
// AuditEvent for the Cilium-incompatibility transparent → cooperative
// auto-downgrade. Issue #79 / spec §5.4 step 4.
func NewInterceptionModeCiliumIncompatibilityDowngrade(in InterceptionModeCiliumIncompatibilityDowngradeInput) *paddockv1alpha1.AuditEvent {
	return &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      auditEventName(in.RunName, "interception-cilium-downgrade"),
			Namespace: in.Namespace,
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Kind:     paddockv1alpha1.AuditKindInterceptionModeCiliumIncompatibilityDowngrade,
			Decision: paddockv1alpha1.AuditDecisionWarned,
			RunRef:   &paddockv1alpha1.LocalObjectReference{Name: in.RunName},
			Reason:   fmt.Sprintf("transparent → cooperative auto-downgrade: %s", in.Reason),
		},
	}
}
```

(`auditEventName` is the existing helper in this file. If a different
naming convention is used by neighbouring builders, follow that
instead.)

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/auditing/ -run TestNewInterceptionModeCiliumIncompatibilityDowngrade -v`
Expected: PASS.

- [ ] **Step 7: Add controller emit helper**

In `internal/controller/audit.go`, append after
`EmitNetworkPolicyEnforcementWithdrawn`:

```go
// EmitInterceptionModeCiliumIncompatibilityDowngrade records that an
// admission-time CNI-compat check forced a transparent → cooperative
// downgrade because the cluster is on Cilium-with-KPR. Reason is the
// detected cilium-config state (e.g. "kube-proxy-replacement=true").
func (c *ControllerAudit) EmitInterceptionModeCiliumIncompatibilityDowngrade(ctx context.Context, runName, namespace, reason string) {
	c.write(ctx,
		auditing.NewInterceptionModeCiliumIncompatibilityDowngrade(auditing.InterceptionModeCiliumIncompatibilityDowngradeInput{
			RunName:   runName,
			Namespace: namespace,
			Reason:    reason,
		}),
		"interception-mode-cilium-incompatibility-downgrade",
	)
}
```

- [ ] **Step 8: Run controller tests**

Run: `go test ./internal/controller/ -count=1 -v`
Expected: all tests pass.

- [ ] **Step 9: Commit**

```bash
git add api/v1alpha1/auditevent_types.go internal/auditing/builders.go internal/auditing/builders_test.go internal/controller/audit.go config/crd/
git commit -m "feat(audit): cilium-incompat transparent→cooperative downgrade kind (#79)"
```

(Stage `config/crd/` if `make manifests` regenerated CRD YAML; if no
files changed, drop the path.)

---

### Task 16: Add MinInterceptionMode field to BrokerPolicy

ADR-0013 specifies this field but it isn't implemented yet (verified
during plan-writing). Adding it is a prerequisite for Task 17.

**Files:**
- Modify: `api/v1alpha1/brokerpolicy_types.go` (add field)
- Modify: `internal/policy/interception_mode.go` (consume field)
- Modify: `internal/policy/interception_mode_test.go` (test merge logic)
- Generated: `config/crd/...` (kubebuilder regen)

- [ ] **Step 1: Write failing test for the merge rule**

In `internal/policy/interception_mode_test.go`, append:

```go
func TestResolveInterceptionMode_MinTransparentRejectsCooperative(t *testing.T) {
	cli := fake.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}},
	).Build()

	// Two policies: one cooperativeAccepted, one with
	// minInterceptionMode=transparent. Expected: Unavailable=true with
	// a clear reason; mode unset.
	coop := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "coop", Namespace: "test"},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			Interception: &paddockv1alpha1.InterceptionSpec{
				CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
					Accepted: true,
					Reason:   "trusted agent",
				},
			},
		},
	}
	strict := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "strict", Namespace: "test"},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			Interception: &paddockv1alpha1.InterceptionSpec{
				MinInterceptionMode: paddockv1alpha1.InterceptionModeTransparent,
			},
		},
	}
	got, err := ResolveInterceptionMode(context.Background(), cli, "test",
		[]*paddockv1alpha1.BrokerPolicy{coop, strict})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got.Unavailable {
		t.Fatalf("expected Unavailable=true; got mode=%s reason=%s", got.Mode, got.Reason)
	}
	if !strings.Contains(got.Reason, "minInterceptionMode") {
		t.Errorf("reason should mention minInterceptionMode: %s", got.Reason)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/policy/ -run TestResolveInterceptionMode_MinTransparent -v`
Expected: FAIL — `MinInterceptionMode` is undefined.

- [ ] **Step 3: Add the API field**

In `api/v1alpha1/brokerpolicy_types.go`, find `InterceptionSpec` and
append:

```go
	// MinInterceptionMode rejects (does not downgrade) admission when
	// the resolved interception mode would be weaker than this minimum.
	// Allowed values: "" (unset, no minimum), "transparent". When
	// "transparent" and admission would resolve to cooperative (either
	// via cooperativeAccepted or via the Cilium-incompat auto-downgrade
	// from Issue #79), the run is rejected with a reason naming the
	// cause. Hostile-tenant operators set this to fail-fast rather
	// than silently lose hostile-binary protection. ADR-0013 Decision +
	// Issue #79 update.
	// +optional
	// +kubebuilder:validation:Enum="";transparent
	MinInterceptionMode InterceptionMode `json:"minInterceptionMode,omitempty"`
```

- [ ] **Step 4: Implement MinInterceptionMode in the resolver**

In `internal/policy/interception_mode.go::ResolveInterceptionMode`,
after the `if merged.Mode == InterceptionModeCooperative` short-circuit,
before returning, check the merged minimum:

Modify `mergePolicyInterception` to also return a `MinMode` derived
from the strictest policy:

```go
type mergedInterception struct {
	Mode             paddockv1alpha1.InterceptionMode
	MinMode          paddockv1alpha1.InterceptionMode
	AcceptanceReason string
	MatchedPolicy    string
}

func mergePolicyInterception(matches []*paddockv1alpha1.BrokerPolicy) mergedInterception {
	if len(matches) == 0 {
		return mergedInterception{Mode: paddockv1alpha1.InterceptionModeTransparent}
	}
	allCooperative := true
	var firstReason, firstName string
	var minMode paddockv1alpha1.InterceptionMode
	for _, bp := range matches {
		i := bp.Spec.Interception
		if i == nil || i.CooperativeAccepted == nil || !i.CooperativeAccepted.Accepted {
			allCooperative = false
		}
		if i != nil && i.MinInterceptionMode == paddockv1alpha1.InterceptionModeTransparent {
			minMode = paddockv1alpha1.InterceptionModeTransparent
		}
		if i != nil && i.CooperativeAccepted != nil && i.CooperativeAccepted.Accepted && firstReason == "" {
			firstReason = i.CooperativeAccepted.Reason
			firstName = bp.Name
		}
	}
	if allCooperative {
		return mergedInterception{
			Mode:             paddockv1alpha1.InterceptionModeCooperative,
			MinMode:          minMode,
			AcceptanceReason: firstReason,
			MatchedPolicy:    firstName,
		}
	}
	return mergedInterception{Mode: paddockv1alpha1.InterceptionModeTransparent, MinMode: minMode}
}
```

Then in `ResolveInterceptionMode`, after computing `merged`, before
returning the cooperative branch, add the rejection check:

```go
	if merged.Mode == paddockv1alpha1.InterceptionModeCooperative &&
		merged.MinMode == paddockv1alpha1.InterceptionModeTransparent {
		return InterceptionDecision{
			Unavailable: true,
			Reason: "BrokerPolicy.spec.interception.minInterceptionMode=transparent " +
				"refuses cooperative-mode resolution. To run cooperative, drop the " +
				"minimum or split policies; for hostile-tenant posture, ensure the " +
				"cluster supports transparent-mode interception (no Cilium kube-proxy-" +
				"replacement, or a CNI mode that preserves iptables in pod netns).",
		}, nil
	}
```

- [ ] **Step 5: Regenerate manifests**

Run: `make manifests generate`
Expected: `config/crd/bases/paddock.dev_brokerpolicies.yaml` updated
with the new field; `zz_generated.deepcopy.go` updated.

- [ ] **Step 6: Run unit tests**

Run: `go test ./internal/policy/ -count=1 -v`
Expected: all existing tests pass; new
`TestResolveInterceptionMode_MinTransparentRejectsCooperative` passes.

- [ ] **Step 7: Commit**

```bash
git add api/v1alpha1/brokerpolicy_types.go internal/policy/interception_mode.go internal/policy/interception_mode_test.go config/crd/ api/v1alpha1/zz_generated.deepcopy.go
git commit -m "feat(api)!: BrokerPolicy.spec.interception.minInterceptionMode (#79)

ADR-0013 promised this field; Issue #79 needs it to reject (rather
than silently downgrade) when the cluster is Cilium-with-KPR and the
operator wants hostile-tenant posture."
```

(Note the `feat!` marker per project memory: breaking-change commits
that touch v1alpha1 carry the `!`.)

---

### Task 17: Wire CNI-incompat downgrade into resolveInterceptionMode

**Files:**
- Modify: `internal/controller/harnessrun_controller.go` (resolveInterceptionMode)
- Modify: `internal/controller/interception_resolve_test.go` (matrix tests)

- [ ] **Step 1: Write failing matrix test**

Append to `interception_resolve_test.go`:

```go
func TestResolveInterceptionMode_DowngradesOnCiliumKPR(t *testing.T) {
	type tc struct {
		name             string
		ciliumKPR        bool
		psaLabel         string
		minMode          paddockv1alpha1.InterceptionMode
		bp               *paddockv1alpha1.BrokerPolicy
		wantMode         paddockv1alpha1.InterceptionMode
		wantUnavailable  bool
		wantReasonHas    string
	}
	cases := []tc{
		{
			name:      "no cilium → transparent (PSA permits)",
			ciliumKPR: false,
			psaLabel:  "",
			wantMode:  paddockv1alpha1.InterceptionModeTransparent,
		},
		{
			name:      "cilium kpr-on, no min → cooperative downgrade",
			ciliumKPR: true,
			psaLabel:  "",
			wantMode:  paddockv1alpha1.InterceptionModeCooperative,
		},
		{
			name:            "cilium kpr-on + minTransparent → reject",
			ciliumKPR:       true,
			psaLabel:        "",
			minMode:         paddockv1alpha1.InterceptionModeTransparent,
			wantUnavailable: true,
			wantReasonHas:   "kube-proxy-replacement",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scheme := newControllerTestScheme(t)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "tenant",
					Labels: map[string]string{},
				},
			}
			if c.psaLabel != "" {
				ns.Labels[policy.PSAEnforceLabel] = c.psaLabel
			}
			objs := []client.Object{ns}
			if c.minMode != "" {
				objs = append(objs, &paddockv1alpha1.BrokerPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: "strict", Namespace: "tenant"},
					Spec: paddockv1alpha1.BrokerPolicySpec{
						AppliesToTemplates: []string{"*"},
						Interception: &paddockv1alpha1.InterceptionSpec{
							MinInterceptionMode: c.minMode,
						},
					},
				})
			}
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			r := &HarnessRunReconciler{
				Client:                       cli,
				IPTablesInitImage:            "img:tag",
				CiliumKubeProxyReplacement:   c.ciliumKPR,
			}
			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "tenant"},
			}
			tpl := &resolvedTemplate{SourceName: "any-template"}
			dec, err := r.resolveInterceptionMode(context.Background(), run, tpl)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if c.wantUnavailable {
				if !dec.Unavailable {
					t.Fatalf("expected Unavailable; got %+v", dec)
				}
				if c.wantReasonHas != "" && !strings.Contains(dec.Reason, c.wantReasonHas) {
					t.Errorf("reason missing %q: %s", c.wantReasonHas, dec.Reason)
				}
				return
			}
			if dec.Unavailable {
				t.Fatalf("unexpected Unavailable: %s", dec.Reason)
			}
			if dec.Mode != c.wantMode {
				t.Errorf("mode: got %s want %s", dec.Mode, c.wantMode)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestResolveInterceptionMode_DowngradesOnCiliumKPR -v`
Expected: FAIL — `CiliumKubeProxyReplacement` is undefined; downgrade
logic absent.

- [ ] **Step 3: Add CiliumKubeProxyReplacement field to reconciler**

In `internal/controller/harnessrun_controller.go`, add to
`HarnessRunReconciler`:

```go
	// CiliumKubeProxyReplacement is true when the cluster's Cilium
	// install runs with kube-proxy-replacement on (i.e. iptables nat
	// OUTPUT REDIRECT in pod netns is silently bypassed). When true,
	// resolveInterceptionMode auto-downgrades transparent → cooperative
	// and emits an audit event. Issue #79 / spec §5.4.
	CiliumKubeProxyReplacement bool
```

- [ ] **Step 4: Add corresponding field to ProxyBrokerConfig**

In `internal/controller/proxybroker_config.go`, append:

```go
	// CiliumKubeProxyReplacement: see HarnessRunReconciler.
	CiliumKubeProxyReplacement bool
```

- [ ] **Step 5: Modify resolveInterceptionMode**

In `internal/controller/harnessrun_controller.go::resolveInterceptionMode`,
after the existing PSA-derived decision is computed and before
returning, layer the CNI-incompat downgrade:

```go
	// CNI-incompat: Cilium-with-KPR silently bypasses the iptables
	// REDIRECT chain. If the policy decision is transparent and we
	// detect KPR, downgrade to cooperative; if minInterceptionMode=
	// transparent, reject instead.
	if !decision.Unavailable &&
		decision.Mode == paddockv1alpha1.InterceptionModeTransparent &&
		r.CiliumKubeProxyReplacement {
		// minMode came from the merged policy decision in
		// internal/policy; honour it here.
		// (We re-merge to read MinMode; cheap and avoids changing the
		// InterceptionDecision type.)
		matches, _ := policy.ListMatchingPolicies(ctx, r.Client, run.Namespace, tpl.SourceName)
		if hasMinTransparent(matches) {
			return policy.InterceptionDecision{
				Unavailable: true,
				Reason: "BrokerPolicy.spec.interception.minInterceptionMode=transparent " +
					"refuses to downgrade; cluster CNI is Cilium with kube-proxy-" +
					"replacement, which silently bypasses the iptables redirect used by " +
					"transparent mode. Use non-KPR Cilium for hostile-tenant posture, or " +
					"relax the BrokerPolicy.",
			}, nil
		}
		return policy.InterceptionDecision{
			Mode:   paddockv1alpha1.InterceptionModeCooperative,
			Reason: "auto-downgrade: kube-proxy-replacement=true",
		}, nil
	}
	return decision, nil
}
```

Add a helper at the bottom of the file:

```go
// hasMinTransparent returns true if any of the matching policies
// declares minInterceptionMode=transparent. Callers that already have
// a merged decision should prefer to thread the value through; this
// helper exists for the resolver wrapper which only has the unmerged
// list.
func hasMinTransparent(matches []*paddockv1alpha1.BrokerPolicy) bool {
	for _, bp := range matches {
		if bp.Spec.Interception != nil &&
			bp.Spec.Interception.MinInterceptionMode == paddockv1alpha1.InterceptionModeTransparent {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestResolveInterceptionMode_DowngradesOnCiliumKPR -count=1 -v`
Expected: PASS, all sub-cases.

- [ ] **Step 7: Commit**

```bash
git add internal/controller/harnessrun_controller.go internal/controller/proxybroker_config.go internal/controller/interception_resolve_test.go
git commit -m "feat(controller): cilium-KPR transparent→cooperative auto-downgrade (#79)"
```

---

### Task 18: Emit audit + WARN log on the downgrade

**Files:**
- Modify: `internal/controller/harnessrun_controller.go` (caller of resolveInterceptionMode)

The reconciler's existing call site for `resolveInterceptionMode`
already wires the cooperative-acceptance audit event. We add the new
event when the downgrade is the cause.

- [ ] **Step 1: Find the call site**

```bash
grep -n "resolveInterceptionMode\|EmitInterceptionMode" internal/controller/harnessrun_controller.go
```

Expected: a single call site around line 464; existing emit on
cooperative acceptance.

- [ ] **Step 2: Layer the new audit emit**

Where the existing reconciler emits an audit on cooperative
acceptance (search for `EmitInterceptionMode` or
`InterceptionModeCooperativeAccepted`), add a sibling emit when the
downgrade was the cause. Detect by inspecting `decision.Reason`
prefix, or — cleaner — extend `InterceptionDecision` with a
`DowngradeReason` field plumbed by the new branch in Task 17 §step 5.

If extending the type:

```go
// In internal/policy/interception_mode.go
type InterceptionDecision struct {
	// ... existing fields ...
	// DowngradeReason, when non-empty, indicates a CNI-incompat
	// auto-downgrade happened (Mode is cooperative, but the
	// reconciler did not get there via cooperativeAccepted).
	DowngradeReason string
}
```

In Task 17's downgrade branch, set `DowngradeReason: "kube-proxy-replacement=true"`.

Then in `harnessrun_controller.go` near the existing audit emit:

```go
	if decision.Mode == paddockv1alpha1.InterceptionModeCooperative {
		if decision.DowngradeReason != "" {
			r.Audit.EmitInterceptionModeCiliumIncompatibilityDowngrade(
				ctx, run.Name, run.Namespace, decision.DowngradeReason,
			)
			logger.Info("WARN cilium-incompat: transparent→cooperative auto-downgrade",
				"run", run.Name,
				"reason", decision.DowngradeReason,
			)
		} else if decision.AcceptanceReason != "" {
			// existing cooperativeAccepted emit (unchanged)
		}
	}
```

- [ ] **Step 3: Run unit tests**

Run: `go test ./internal/controller/ -count=1 -v ./...`
Expected: PASS (no new behaviour-tests beyond the matrix in Task 17).

- [ ] **Step 4: Commit**

```bash
git add internal/policy/interception_mode.go internal/controller/harnessrun_controller.go
git commit -m "feat(controller): emit cilium-incompat downgrade audit + WARN (#79)"
```

---

### Task 19: Wire DetectCiliumKubeProxyReplacement into startup

**Files:**
- Modify: `cmd/main.go`

- [ ] **Step 1: Add startup detection**

In `cmd/main.go`, near the existing `DetectNetworkPolicyCNI` call (the
spot edited in Task 9), add:

```go
	ciliumKPR, kprErr := controller.DetectCiliumKubeProxyReplacement(
		context.Background(), mgr.GetAPIReader(),
	)
	if kprErr != nil {
		setupLog.Error(kprErr, "Cilium KPR probe failed; assuming KPR=false")
		ciliumKPR = false
	}
	if ciliumKPR {
		setupLog.Info("WARN cilium-incompat: cluster has kube-proxy-replacement=true; " +
			"transparent-mode runs will auto-downgrade to cooperative; " +
			"set BrokerPolicy.spec.interception.minInterceptionMode=transparent " +
			"to refuse cooperative posture")
	}
```

Then add `CiliumKubeProxyReplacement: ciliumKPR,` to the
`proxyBrokerCfg` literal.

- [ ] **Step 2: Plumb to the reconciler**

Where `HarnessRunReconciler` is constructed (same file), add:

```go
	hrReconciler := &controller.HarnessRunReconciler{
		// ... existing fields ...
		CiliumKubeProxyReplacement: ciliumKPR,
	}
```

- [ ] **Step 3: Build manager binary**

Run: `go build ./cmd/main.go`
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add cmd/main.go
git commit -m "feat(controller): wire cilium-KPR detection at manager startup (#79)"
```

---

### Task 20: Update ADR-0013 with Issue #79 update

**Files:**
- Modify: `docs/contributing/adr/0013-proxy-interception-modes.md`

- [ ] **Step 1: Append the section**

Append to the end of the ADR:

```markdown
## Issue #79 update (2026-04-28)

Cilium-with-KPR (the modern default for Cilium 1.16+ and what
`make kind-up` ships) breaks both halves of the v0.4 NP/transparent
story: standard NetworkPolicy `ipBlock` rules don't enforce against
host-network destinations like the kube-apiserver static pod, AND
Cilium's BPF datapath silently bypasses the iptables `nat OUTPUT`
REDIRECT chain that transparent-mode iptables-init installs in pod
netns. See [tjorri/paddock#79](https://github.com/tjorri/paddock/issues/79)
for the diagnostic walkthrough and `docs/superpowers/specs/2026-04-28-cilium-compat-design.md`
for the design.

**Issue A — kube-apiserver classification (controller-side fix).**
When `cilium.io/v2/CiliumNetworkPolicy` is registered on the cluster,
the controller emits a CNP variant with
`egress: [toEntities: [kube-apiserver, remote-node]]` instead of the
standard NP. The `remote-node` entity covers the host-network
apiserver static pod where `kube-apiserver` alone does not. Standard
NetworkPolicy (with the Phase 2d apiserver-IP `ipBlock` rule) remains
the path for non-Cilium clusters.

**Issue B — iptables REDIRECT bypass (admission-time downgrade).**
The controller now reads `kube-system/cilium-config` once at admission.
If `kube-proxy-replacement=true`, transparent-mode requests are
auto-downgraded to cooperative regardless of PSA outcome. The
downgrade emits a new
`AuditKindInterceptionModeCiliumIncompatibilityDowngrade` AuditEvent
and a WARN log; the cluster also gets a startup-time WARN log if KPR
is detected.

`BrokerPolicy.spec.interception.minInterceptionMode: transparent`
(promised in this ADR's original Decision and now actually shipping)
**rejects** rather than downgrades. Hostile-tenant operators set this
field to fail-fast on Cilium-with-KPR rather than lose hostile-binary
protection silently.

CNI mode (the third interception mode listed as deferred above)
remains the structural answer for hostile-tenant posture under
Cilium-with-KPR; this update queues it for v1.0 explicitly. The Phase
1 findings doc (`docs/superpowers/plans/2026-04-28-cilium-compat-findings.md`)
captures the rationale: no Cilium config knob in 1.16.5 was found to
preserve pod-netns iptables interception while keeping KPR.
```

- [ ] **Step 2: Commit**

```bash
git add docs/contributing/adr/0013-proxy-interception-modes.md
git commit -m "docs(adr-0013): issue #79 update — cilium kpr compat (#79)"
```

---

### Task 21: Quickstart cleanup

**Files:**
- Modify: `docs/getting-started/quickstart.md`

- [ ] **Step 1: Locate the workaround note**

```bash
grep -n "KIND_NO_CNI" docs/getting-started/quickstart.md
```

Expected: one or more lines with the workaround callout.

- [ ] **Step 2: Replace with the new note**

Remove the `KIND_NO_CNI=1` callout. Add a one-paragraph note in the
same spot:

```markdown
> **Cilium with kube-proxy-replacement.** `make kind-up` ships Cilium
> 1.16.5 with `kubeProxyReplacement=true`. On this configuration,
> Paddock auto-downgrades transparent-mode interception to cooperative
> at admission (a WARN log + AuditEvent records every downgrade).
> Cooperative mode does not protect against a hostile agent binary;
> for hostile-tenant posture, install Cilium without
> kube-proxy-replacement and set
> `spec.interception.minInterceptionMode: transparent` on a matching
> BrokerPolicy. See [ADR-0013 §"Issue #79 update"](../contributing/adr/0013-proxy-interception-modes.md#issue-79-update-2026-04-28).
```

- [ ] **Step 3: Commit**

```bash
git add docs/getting-started/quickstart.md
git commit -m "docs(quickstart): replace KIND_NO_CNI workaround with cilium-kpr note (#79)"
```

---

### Task 22: E2E regression test — `cilium_compat_test.go`

**Files:**
- Create: `test/e2e/cilium_compat_test.go`

This is the long task. Patterns to mirror from existing tests:
- `test/e2e/e2e_test.go::Context("echo harness", ...)` for the
  end-to-end run flow (apply manifests, wait for HarnessRun
  Succeeded).
- `test/e2e/hostile_test.go` for the AuditEvent assertion shape.

- [ ] **Step 1: Skeleton + skip on non-Cilium clusters**

Create `test/e2e/cilium_compat_test.go`:

```go
//go:build e2e
// +build e2e

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tjorri/paddock/test/utils"
)

const ciliumCompatNamespace = "cilium-compat-e2e"

var _ = Describe("paddock cilium compat (Issue #79)", Ordered, func() {
	BeforeAll(func() {
		// Skip cleanly when not on a Cilium cluster — supports
		// `go test -run TestCiliumCompat` against a kindnet cluster.
		out, err := utils.Run(exec.Command("kubectl", "-n", "kube-system",
			"get", "configmap", "cilium-config", "-o", "name"))
		if err != nil || !strings.Contains(out, "cilium-config") {
			Skip("cilium_compat: cluster has no Cilium installation; skipping " +
				"(run on a Cilium-enabled Kind cluster, e.g. via make setup-test-e2e)")
		}
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", ciliumCompatNamespace, "--wait=false"))
	})

	It("HarnessRun against a non-trivial template Succeeds on Cilium-with-KPR", func() {
		// Apply namespace + secret + template + brokerpolicy + harnessrun.
		// Wait for status.phase == Succeeded.
		// Assert per-run policy resource present (CNP if available, else NP).
		// Assert proxy AuditEvents include >= 1 egress-allow.
		// Assert status.interceptionMode reflects expected resolved mode.

		// (Manifests omitted from this skeleton — see Step 2.)
		Skip("filled in by Step 2")
	})

	It("HarnessRun is rejected when minInterceptionMode=transparent on Cilium-with-KPR", func() {
		// Same setup but with BrokerPolicy.spec.interception.minInterceptionMode=transparent.
		// Assert admission rejects (or run resolves to Failed with InterceptionUnavailable).
		Skip("filled in by Step 3")
	})
})
```

- [ ] **Step 2: Implement the positive spec**

Replace the first `Skip` with the body. Manifests are shipped inline
to keep the test self-contained:

```go
	It("HarnessRun against a non-trivial template Succeeds on Cilium-with-KPR", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		By("creating the namespace")
		_, err := utils.Run(exec.Command("kubectl", "create", "ns", ciliumCompatNamespace))
		Expect(err).NotTo(HaveOccurred())

		By("applying credential secret")
		_, err = utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
			"create", "secret", "generic", "test-creds",
			"--from-literal=token=ignored-by-echo-adapter"))
		Expect(err).NotTo(HaveOccurred())

		By("applying ClusterHarnessTemplate")
		applyManifest(ctx, ciliumCompatTemplateYAML)

		By("applying BrokerPolicy")
		applyManifest(ctx, fmt.Sprintf(ciliumCompatBrokerPolicyYAML, ciliumCompatNamespace))

		By("submitting a HarnessRun")
		applyManifest(ctx, fmt.Sprintf(ciliumCompatHarnessRunYAML, ciliumCompatNamespace))

		By("waiting for HarnessRun to reach a terminal phase")
		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
				"get", "harnessrun", "compat-demo",
				"-o", "jsonpath={.status.phase}"))
			return strings.TrimSpace(out)
		}, 3*time.Minute, 5*time.Second).Should(Equal("Succeeded"))

		By("asserting per-run policy resource exists")
		// CNP-aware: try CNP first, then standard NP.
		out, _ := utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
			"get", "ciliumnetworkpolicy", "compat-demo-egress", "-o", "name"))
		standardOut, _ := utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
			"get", "networkpolicy", "compat-demo-egress", "-o", "name"))
		Expect(strings.TrimSpace(out) + strings.TrimSpace(standardOut)).
			NotTo(BeEmpty(), "expected per-run policy CNP or NP to exist")

		By("asserting proxy emitted >= 1 egress-allow audit event")
		Eventually(func() int {
			out, _ := utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
				"get", "auditevent", "-o",
				"jsonpath={range .items[?(@.spec.kind=='egress-allow')]}{.metadata.name}{'\\n'}{end}"))
			return strings.Count(strings.TrimSpace(out), "\n") + 1
		}, 1*time.Minute, 5*time.Second).Should(BeNumerically(">=", 1))

		By("asserting interceptionMode matches the cilium-kpr branch")
		// On Cilium-with-KPR we expect cooperative; otherwise transparent.
		// Read the cilium-config knob and assert accordingly.
		kprOut, _ := utils.Run(exec.Command("kubectl", "-n", "kube-system",
			"get", "cm", "cilium-config",
			"-o", "jsonpath={.data.kube-proxy-replacement}"))
		expectedMode := "transparent"
		if strings.TrimSpace(kprOut) == "true" {
			expectedMode = "cooperative"
		}
		mode, _ := utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
			"get", "harnessrun", "compat-demo",
			"-o", "jsonpath={.status.interceptionMode}"))
		Expect(strings.TrimSpace(mode)).To(Equal(expectedMode))

		By("asserting no FailedMount/BackOff/timeout in pod events")
		evt, _ := utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
			"get", "events",
			"--field-selector=involvedObject.kind=Pod",
			"-o", "jsonpath={range .items[*]}{.reason}{'\\t'}{.message}{'\\n'}{end}"))
		Expect(evt).NotTo(MatchRegexp(`(?i)FailedMount|BackOff|context deadline exceeded`))
	})
```

Manifests as Go constants at the bottom of the file:

```go
const ciliumCompatTemplateYAML = `
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata:
  name: cilium-compat-echo
spec:
  agent:
    image: paddock-echo:dev
  adapter:
    image: paddock-adapter-echo:dev
  requires:
    credentials:
      - name: token
        secretRef:
          name: test-creds
          key: token
    egress:
      - host: echo.paddock-system.svc.cluster.local
        ports: [80]
`

const ciliumCompatBrokerPolicyYAML = `
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: cilium-compat-policy
  namespace: %s
spec:
  appliesToTemplates: ["cilium-compat-echo"]
  credentials:
    - name: token
      secretRef:
        name: test-creds
        key: token
  egress:
    - host: echo.paddock-system.svc.cluster.local
      ports: [80]
`

const ciliumCompatHarnessRunYAML = `
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: compat-demo
  namespace: %s
spec:
  templateRef:
    name: cilium-compat-echo
  prompt: hello
`

func applyManifest(ctx context.Context, manifest string) {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
}
```

(Adapt manifests to whatever fields the actual ClusterHarnessTemplate
and BrokerPolicy CRDs require in this codebase. Cross-check the
existing echo template at
`config/samples/paddock_v1alpha1_clusterharnesstemplate_echo.yaml` —
the test should mirror its shape but add a non-empty `requires` block
to exercise the bug class.)

- [ ] **Step 3: Implement the negative spec**

Replace the second `Skip`:

```go
	It("HarnessRun is rejected when minInterceptionMode=transparent on Cilium-with-KPR", func() {
		// Skip if the cluster is not Cilium-with-KPR — there's nothing
		// to reject in that configuration.
		kprOut, _ := utils.Run(exec.Command("kubectl", "-n", "kube-system",
			"get", "cm", "cilium-config",
			"-o", "jsonpath={.data.kube-proxy-replacement}"))
		if strings.TrimSpace(kprOut) != "true" {
			Skip("cluster is not Cilium-with-KPR; nothing to reject")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		By("re-applying the BrokerPolicy with minInterceptionMode=transparent")
		strict := strings.Replace(
			fmt.Sprintf(ciliumCompatBrokerPolicyYAML, ciliumCompatNamespace),
			"  egress:",
			"  interception:\n    minInterceptionMode: transparent\n  egress:",
			1,
		)
		applyManifest(ctx, strict)

		By("submitting another HarnessRun, expecting Failed/Unavailable")
		applyManifest(ctx, strings.Replace(
			fmt.Sprintf(ciliumCompatHarnessRunYAML, ciliumCompatNamespace),
			"compat-demo", "compat-demo-strict", 1,
		))

		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
				"get", "harnessrun", "compat-demo-strict",
				"-o", `jsonpath={.status.conditions[?(@.type=="InterceptionUnavailable")].status}`))
			return strings.TrimSpace(out)
		}, 90*time.Second, 5*time.Second).Should(Equal("True"))

		By("verifying the reason mentions kube-proxy-replacement")
		reason, _ := utils.Run(exec.Command("kubectl", "-n", ciliumCompatNamespace,
			"get", "harnessrun", "compat-demo-strict",
			"-o", `jsonpath={.status.conditions[?(@.type=="InterceptionUnavailable")].message}`))
		Expect(reason).To(ContainSubstring("kube-proxy-replacement"))
	})
```

- [ ] **Step 4: Run the e2e test locally**

```bash
make test-e2e 2>&1 | tee /tmp/e2e.log
```

Expected: full suite passes; the new `cilium compat` Describe runs
with both `It` specs passing.

If a single-test iteration is needed:

```bash
KIND=$(go env GOPATH)/bin/kind KIND_CLUSTER=paddock-test-e2e \
  go test -tags=e2e -timeout=10m ./test/e2e/ -v -ginkgo.v \
  -ginkgo.focus='cilium compat' 2>&1 | tee /tmp/e2e.log
```

- [ ] **Step 5: Commit**

```bash
git add test/e2e/cilium_compat_test.go
git commit -m "test(e2e): cilium compat regression for #79"
```

---

### Task 23: Verify the full e2e suite still passes

**Files:** none

- [ ] **Step 1: Run the full suite**

```bash
make cleanup-test-e2e 2>/dev/null || true
make test-e2e 2>&1 | tee /tmp/e2e.log
```

Expected: all specs PASS, including the new cilium-compat ones.

- [ ] **Step 2: If anything fails, diagnose and re-iterate**

The test infrastructure dumps `kubectl logs` / `describe` on failure;
inspect `/tmp/e2e.log` first.

- [ ] **Step 3: No commit needed** unless the run surfaces something
to fix. If a fix is needed, commit it as a separate task with a
descriptive Conventional-Commits message.

---

### Task 24: Open the PR

**Files:** none

- [ ] **Step 1: Push the branch**

```bash
git push -u origin fix/cilium-compat
```

- [ ] **Step 2: Open the PR via gh**

```bash
gh pr create --title "fix: cilium kube-apiserver classification + transparent-mode interception (#79)" \
  --body "$(cat <<'BODY'
## Summary

Fixes #79: HarnessRun with non-empty \`requires\` failed on Cilium-with-KPR (the \`make kind-up\` default). Two independent root causes — kube-apiserver classification and iptables REDIRECT silent-bypass — both resolved.

Phase 1 of the work investigated Cilium configuration knobs to preserve transparent-mode interception; findings are in \`docs/superpowers/plans/2026-04-28-cilium-compat-findings.md\`. Phase 2 implements the chosen fix branches: \`A-FIX-toEntities\` (CNP variant with \`toEntities: [kube-apiserver, remote-node]\` when CNP CRDs are present) and \`B-FIX-cooperative-downgrade\` (admission auto-downgrade to cooperative on Cilium-with-KPR, with \`BrokerPolicy.spec.interception.minInterceptionMode: transparent\` rejecting rather than downgrading).

ADR-0013 has an "Issue #79 update" section. Quickstart's \`KIND_NO_CNI=1\` workaround is removed.

## Test plan

- [ ] \`make test-e2e\` passes with the new \`cilium_compat_test.go\` specs.
- [ ] \`go test ./...\` passes (unit-test matrix in \`internal/policy\` + \`internal/controller\`).
- [ ] On a fresh \`make kind-up\` cluster, the \`config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code.yaml\` quickstart Step 4 succeeds end-to-end.
- [ ] On the same cluster with a \`BrokerPolicy.spec.interception.minInterceptionMode: transparent\`, the run is rejected with the cilium-incompat diagnostic.
BODY
)"
```

(Per project memory: omit the "Generated with Claude Code" footer; no
\`Claude\` mention in commit messages or PR body.)

- [ ] **Step 3: Done**

PR open; the Phase 1 findings doc, the spec, the implementation
commits, and the e2e regression test are all reviewable in one place.

---

## Self-review checklist

After implementing through Task 24, run these checks before requesting
review:

- [ ] `git log --oneline fix/cilium-compat ^main` — commit history
  reads as a coherent sequence (Phase 1, then Issue A, then Issue B,
  then docs, then test).
- [ ] `grep -r 'TODO\|TBD\|FIXME' docs/superpowers/plans/2026-04-28-cilium-compat-findings.md` —
  no leftover placeholders in the findings doc.
- [ ] `make manifests generate fmt vet` — clean (no uncommitted
  generated artifacts).
- [ ] Quickstart Step 4 walked end-to-end on a fresh `make kind-up`
  cluster with a real Anthropic API key (per the v0.4 quickstart
  contract).

---

## Phase 1 update — design pivot (2026-04-28)

Phase 1 ran. Findings doc:
`docs/superpowers/plans/2026-04-28-cilium-compat-findings.md`. The
real Issue B mechanism is the per-run NetworkPolicy missing a
loopback allow rule, NOT iptables-init being silently bypassed. See
spec §11 ("Phase 1 update — design pivot") for the full narrative.

**Tasks 14–20 (cilium-KPR detection, cooperative downgrade, AuditKind,
MinInterceptionMode, etc.) are obsolete and dropped from this branch.**
The remaining task list is the much-smaller substitute below. Tasks
keep their original numbers where the work is unchanged; new tasks
prefix `2P1-` ("Phase 2 post-Phase-1").

### Task 8 (revised): detect Cilium CNP CRDs at startup

Same as the original Task 8 above. No revision.

### Task 9 (revised): wire CNP detection into startup

Same as original Task 9 above. No revision.

### Task 10 (revised): build CNP variant with toEntities + LOOPBACK ALLOW

Same as original Task 10 except the rule list adds **one extra
entry** for loopback. Update the test expectations to assert
the new rule is present.

In the rule list inside `buildCiliumEgressPolicy`, AFTER the
broker rule and BEFORE `ciliumEgressRulesAdditional()`, add:

```go
// Loopback allow — required so iptables nat OUTPUT REDIRECT from
// agent traffic on TCP 80/443 to the proxy at 127.0.0.1:15001 is
// not dropped by Cilium-with-KPR's NP enforcement (Issue #79). On
// kindnet/Calico this is a no-op (loopback isn't policed).
rules = append(rules, map[string]interface{}{
    "toCIDRSet": []interface{}{
        map[string]interface{}{"cidr": "127.0.0.0/8"},
    },
    "toPorts": []interface{}{
        map[string]interface{}{
            "ports": []interface{}{
                map[string]interface{}{"protocol": "TCP"},
            },
        },
    },
})
```

Update `TestBuildCiliumEgressPolicy_HasKubeApiserverAndRemoteNodeEntities`
(or split into a dedicated `TestBuildCiliumEgressPolicy_HasLoopbackAllow`)
to assert the loopback rule is present.

### Task 11 (revised): branch ensureRunNetworkPolicy on CNP availability

Same as original Task 11. No revision specific to the pivot — the
branching logic stands.

### Task 2P1-1 (NEW): add loopback allow to STANDARD NetworkPolicy builder

**Files:**
- Modify: `internal/controller/network_policy.go` (add loopback rule
  to the `rules := []networkingv1.NetworkPolicyEgressRule{...}`
  literal in `buildEgressNetworkPolicy`)
- Modify: `internal/controller/network_policy_test.go` (add test
  asserting the rule is present in standard-NP output)

The same loopback gap exists on the standard-NP path (it just
doesn't break kindnet/Calico because they don't police loopback).
Add the rule for parity and defence-in-depth.

- [ ] **Step 1: Write the failing test**

In `network_policy_test.go`, append:

```go
func TestBuildEgressNetworkPolicy_HasLoopbackAllow(t *testing.T) {
    cfg := networkPolicyConfig{
        ClusterPodCIDR:     "10.244.0.0/16",
        ClusterServiceCIDR: "10.96.0.0/12",
        BrokerNamespace:    "paddock-system",
        BrokerPort:         8443,
    }
    np := buildEgressNetworkPolicy(
        metav1.LabelSelector{MatchLabels: map[string]string{"x": "y"}},
        "x", "y", nil, cfg,
    )
    found := false
    for _, rule := range np.Spec.Egress {
        for _, peer := range rule.To {
            if peer.IPBlock != nil && peer.IPBlock.CIDR == "127.0.0.0/8" {
                found = true
            }
        }
    }
    if !found {
        t.Errorf("expected egress rule with ipBlock 127.0.0.0/8")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestBuildEgressNetworkPolicy_HasLoopbackAllow -v`
Expected: FAIL — no loopback rule yet.

- [ ] **Step 3: Add the loopback rule**

In `internal/controller/network_policy.go::buildEgressNetworkPolicy`,
inside the `rules := []networkingv1.NetworkPolicyEgressRule{...}`
literal, after the public-internet TCP/443 + TCP/80 rules and the
optional broker rule, before the optional apiserver rule, add:

```go
{
    To: []networkingv1.NetworkPolicyPeer{
        {IPBlock: &networkingv1.IPBlock{CIDR: "127.0.0.0/8"}},
    },
    Ports: []networkingv1.NetworkPolicyPort{
        {Protocol: &tcp},
    },
},
```

The empty-`Port` field means "any TCP port to loopback." See
the godoc for `NetworkPolicyPort.Port`: nil/zero matches all ports.

(If the existing rules construction structure isn't a single
literal, add the loopback rule in the same place via `append`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestBuildEgressNetworkPolicy_HasLoopbackAllow -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/network_policy.go internal/controller/network_policy_test.go
git commit -m "fix(controller): allow loopback egress in per-run NetworkPolicy (#79)

Cilium-with-KPR enforces NP on iptables-redirected loopback flows.
Without the rule, the agent's TCP/443 → 127.0.0.1:15001 redirect is
dropped before reaching the proxy."
```

### Task 12 (revised): extend Owns() watch to CNP

Same as original Task 12. No revision.

### Task 13 (revised): SKIP — no longer applies

Original Task 13 dropped the apiserver-IP rule from the CNP path.
Keeping it now is fine (defence-in-depth) and the CNP builder may
or may not consume `cfg.APIServerIPs` — if it does, the redundancy
is harmless. Skip Task 13.

### Tasks 14–20: SKIPPED

Cilium-KPR detection (14), AuditKind constants (15),
MinInterceptionMode field (16), CNI-incompat downgrade in
resolveInterceptionMode (17), audit emit + WARN log (18), startup
detection wiring (19) — all dropped per the pivot. The much-simpler
loopback rule (Task 2P1-1 above) supersedes the entire
B-FIX-cooperative-downgrade work.

### Task 2P1-2 (NEW): build manager image, kind load, helm upgrade, end-to-end retest

**Files:** none (this is operational validation, no code changes)

This is the load-bearing manual validation per the user's
end-to-end requirement: build the patched manager, get it into the
local `paddock-dev` cluster, retry the failing claude-code
HarnessRun, and confirm the agent reaches the proxy under Cilium-
with-KPR.

- [ ] **Step 1: Rebuild the manager image with the new code**

```bash
make docker-build IMG=paddock-manager:dev
```

Expected: clean build; image present in local docker registry.

- [ ] **Step 2: Load into the kind cluster**

```bash
kind load docker-image paddock-manager:dev --name paddock-dev
```

Expected: image loaded onto both the `paddock-dev-control-plane` and
`paddock-dev-worker` nodes.

- [ ] **Step 3: Restart the controller-manager to pick up the new image**

```bash
kubectl -n paddock-system rollout restart deploy paddock-controller-manager
kubectl -n paddock-system rollout status deploy paddock-controller-manager --timeout=2m
```

- [ ] **Step 4: Confirm clean state in `claude-demo` namespace**

```bash
kubectl -n claude-demo delete harnessrun --all --wait=true
kubectl -n claude-demo delete networkpolicy --all
kubectl -n claude-demo delete ciliumnetworkpolicy --all 2>/dev/null || true
```

- [ ] **Step 5: Submit a fresh HarnessRun**

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: e2e-validate
  namespace: claude-demo
spec:
  templateRef:
    name: claude-code
  prompt: "hello"
EOF
```

- [ ] **Step 6: Wait for terminal phase + collect diagnostics**

```bash
SECONDS_WAITED=0
until [ "$(kubectl -n claude-demo get harnessrun e2e-validate -o jsonpath='{.status.phase}' 2>/dev/null)" = "Failed" ] || \
      [ "$(kubectl -n claude-demo get harnessrun e2e-validate -o jsonpath='{.status.phase}' 2>/dev/null)" = "Succeeded" ] || \
      [ $SECONDS_WAITED -gt 120 ]; do
  echo "T=${SECONDS_WAITED}s phase=$(kubectl -n claude-demo get harnessrun e2e-validate -o jsonpath='{.status.phase}' 2>/dev/null)"
  sleep 5
  SECONDS_WAITED=$((SECONDS_WAITED+5))
done

POD=$(kubectl -n claude-demo get pod -l paddock.dev/run=e2e-validate -o name)
echo "===== proxy logs ====="
kubectl -n claude-demo logs "$POD" -c proxy --tail=40
echo "===== agent logs ====="
kubectl -n claude-demo logs "$POD" -c agent --tail=20
echo "===== per-run policy ====="
kubectl -n claude-demo get networkpolicy,ciliumnetworkpolicy
```

- [ ] **Step 7: Pass criteria**

The proxy logs MUST show at least one connection-accept event for
`downloads.claude.ai:443`. The agent's curl-style timeout MUST NOT
appear in the agent logs. The per-run policy MUST be a CNP (since
CNP CRDs are present in this cluster).

The run may still fail at the TLS-trust step (the unrelated
out-of-scope bug). That's acceptable for this validation — the bug
we're fixing is the connection-level failure, not the trust issue.

If the proxy still shows zero connection-accepts: the loopback rule
isn't taking effect; investigate.

- [ ] **Step 8: No commit needed** (this task is operational
  validation only). If diagnostics surface a problem, fix it as a
  new commit and re-run.

### Task 20 (revised): ADR-0013 update

Use the revised content reflecting Phase 1 findings. The original
Task 20's content described "CNI-compatibility-detection algorithm"
which is now obsolete. Replace with content explaining the per-run NP
loopback fix.

Append to `docs/contributing/adr/0013-proxy-interception-modes.md`:

```markdown
## Issue #79 update (2026-04-28)

Cilium-with-KPR (the modern Cilium default and what `make kind-up`
ships) breaks Phase 2d's per-run NetworkPolicy enforcement model in
two places. Both have controller-side fixes that preserve transparent
mode under hostile-tenant posture.

**Issue A — kube-apiserver classification.** Standard NetworkPolicy
`ipBlock` rules don't enforce against host-network destinations like
the kube-apiserver static pod on Cilium. The fix: when the cluster
has the `cilium.io/v2/CiliumNetworkPolicy` CRD registered, the
controller emits a CNP variant with
`egress: [toEntities: [kube-apiserver, remote-node]]` instead of the
standard NP. The `remote-node` entity covers the host-network
apiserver static pod where `kube-apiserver` alone may not. Standard
NetworkPolicy (with the Phase 2d apiserver-IP `ipBlock` rule)
remains the path for non-Cilium clusters.

**Issue B — per-run NP missing a loopback allow.** iptables-init
installs `nat OUTPUT -j REDIRECT --to-ports 15001` for TCP/443 and
TCP/80; the agent's traffic to `downloads.claude.ai:443` (etc.)
gets rewritten to `127.0.0.1:15001` and lands on the proxy. On
kindnet/Calico this loopback flow isn't policed. **On Cilium-with-KPR
it is**, and the per-run NP's egress rules don't allow loopback —
the redirected packet is dropped before reaching the proxy. The
fix: add `egress: [{to: [{ipBlock: {cidr: 127.0.0.0/8}}], ports:
[{protocol: TCP}]}]` to both standard NP and CNP variants. One
rule, defence-in-depth on non-Cilium clusters, mandatory on
Cilium-with-KPR.

CNI mode (the third interception mode listed as deferred in this
ADR's original Decision) remains the long-term answer for
environments where iptables interception is structurally unviable.
Issue #79 does not trigger that case — the v0.5 fix preserves
transparent mode under all tested CNIs.
```

Commit:

```bash
git add docs/contributing/adr/0013-proxy-interception-modes.md
git commit -m "docs(adr-0013): issue #79 update — per-run NP loopback allow + CNP variant (#79)"
```

### Task 21 (revised): quickstart cleanup

Same intent as original Task 21, simplified copy.

`docs/getting-started/quickstart.md`: remove the `KIND_NO_CNI=1`
workaround note. Replace with one line indicating the quickstart
now works on the default `make kind-up` cluster:

```markdown
> **Cilium support.** Paddock's per-run NetworkPolicy + transparent
> proxy interception now works on Cilium-with-kube-proxy-replacement
> (the default for `make kind-up`). See ADR-0013 §"Issue #79 update"
> for the per-run NP shape.
```

Commit:

```bash
git add docs/getting-started/quickstart.md
git commit -m "docs(quickstart): drop KIND_NO_CNI workaround; cilium-KPR now supported (#79)"
```

### Task 22 (revised): e2e regression test

Largely same intent as original Task 22, with revised assertions.
The interception mode is `transparent` (not cooperative), and the
positive spec asserts the run reaches at least the proxy-connection
stage successfully. The negative spec for `MinInterceptionMode` is
SKIPPED (we're not adding that field).

Substitute the original Task 22's spec body for:

```go
It("HarnessRun against a non-trivial template reaches the proxy on Cilium-with-KPR", func() {
    // ... apply template, BrokerPolicy, HarnessRun ...
    // Wait for terminal phase OR for first proxy connection-accept event,
    // whichever comes first.

    // Assertions:
    // - run reached at least the BrokerReady=True / EgressConfigured=True
    //   conditions (i.e., admission resolved interception mode and the
    //   sidecars came up).
    // - per-run policy resource exists (CNP if CNP CRDs present, NP otherwise).
    //   The policy includes the loopback allow rule (cidr 127.0.0.0/8 TCP).
    // - HarnessRun.status.interceptionMode == "transparent" (regardless of CNI).
    // - Proxy emitted >= 1 connection-accept event.
    // - No "context deadline exceeded" / "BackOff" in pod events.
})
```

Drop the original "Negative assertion (separate spec in the same file)"
block — it relied on `MinInterceptionMode` which we've dropped.

Skip-on-non-Cilium logic stands.

Commit + run:

```bash
git add test/e2e/cilium_compat_test.go
git commit -m "test(e2e): cilium compat regression for #79 — transparent mode + loopback allow"
make test-e2e 2>&1 | tee /tmp/e2e.log
```

### Task 23 (unchanged): full e2e suite passes

Same as original.

### Task 24 (revised): open the PR

Same as original Task 24, with revised PR body reflecting actual
fix shape:

```bash
git push -u origin fix/cilium-compat
gh pr create --title "fix: cilium per-run NP loopback allow + CNP variant for kube-apiserver (#79)" \
  --body "$(cat <<'BODY'
## Summary

Fixes #79: HarnessRun with non-empty `requires` failed on Cilium-with-KPR (the `make kind-up` default). The original walkthrough's "iptables silently bypassed" hypothesis was empirically refuted (see `docs/superpowers/plans/2026-04-28-cilium-compat-findings.md`); the actual root causes are two per-run NetworkPolicy gaps:

- **Issue A** — kube-apiserver classification. Cilium-with-KPR doesn't enforce standard NP `ipBlock` rules against host-network destinations; switched to CiliumNetworkPolicy with `toEntities: [kube-apiserver, remote-node]` when CNP CRDs are present.

- **Issue B** — per-run NP missing a loopback allow. iptables nat OUTPUT REDIRECT rewrites the agent's TCP/443 destination to `127.0.0.1:15001`. Cilium-with-KPR enforces NP on this redirected loopback flow (kindnet/Calico don't), and no rule allowed it. Added `egress: [{to: [{ipBlock: {cidr: 127.0.0.0/8}}], ports: [{protocol: TCP}]}]` to both standard NP and CNP variants.

Validated end-to-end on a fresh `make kind-up` cluster: agent reaches the proxy under transparent mode. ADR-0013 has an "Issue #79 update" section; quickstart's `KIND_NO_CNI=1` workaround is removed.

## Test plan

- [ ] `go test ./...` passes.
- [ ] `make test-e2e` passes including the new `cilium_compat_test.go` specs.
- [ ] On a fresh `make kind-up` cluster, manually submitting a claude-code HarnessRun results in the proxy logging at least one connection-accept event for the agent (transparent mode resolution, per-run CNP emitted).
BODY
)"
```

### Task 2P1-3 (NEW): add header note to probe script

**Files:**
- Modify: `hack/cilium-probe-iptables-redirect.sh`

The script's `RESULT (variant): FAIL` outputs in Phase 1 were
sink-side artifacts (busybox `nc -e cat` exits after one
connection). The iptables interception under Cilium-with-KPR
actually works fine; the bug was in the per-run NP not allowing
loopback. Add a header note explaining this so the script doesn't
mislead future debugging.

- [ ] **Step 1: Append to the docstring at the top of the script**

After the existing variants list (line ~22 area), insert:

```bash
#
# NOTE (2026-04-28): Phase 1 of Issue #79 ran this script across all
# variants and got FAIL on every one. That outcome is a SINK-SIDE
# ARTIFACT, not a real iptables-bypass result. Busybox netcat's
# `nc -lk -p 15001 -e cat` exits after one connection, so subsequent
# curls land on a closed port. With a robust sink (Python http.server
# on :15001, no NetworkPolicy applied), iptables nat OUTPUT REDIRECT
# under Cilium-with-KPR works end-to-end. Issue #79's real root cause
# was the per-run NetworkPolicy missing a loopback allow rule. See
# `docs/superpowers/plans/2026-04-28-cilium-compat-findings.md`.
#
# This script is kept in-tree as scaffolding for future Cilium-config-
# variant probing; replace the busybox sink with a python http.server
# or ncat-based listener if you re-use it.
```

- [ ] **Step 2: Lint-pass**

`bash -n hack/cilium-probe-iptables-redirect.sh` — must produce no output.

- [ ] **Step 3: Commit**

```bash
git add hack/cilium-probe-iptables-redirect.sh
git commit -m "docs(hack): note that probe-script FAILs were busybox-nc artifacts (#79)"
```
