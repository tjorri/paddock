# Controller Dedup Pass — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` (recommended) or
> `superpowers:executing-plans` (inline). Steps use checkbox (`- [ ]`)
> syntax for tracking.
>
> **Dependency structure:** Tasks land in the order written. Each task
> ends in a self-contained commit and a green `make test` (Tasks 1–7,
> 9) or `go test ./internal/controller/... -run <name>` (Task 8). The
> phasing is reviewer-cost-ordered: Phase 1 mechanical extractions
> first, then behaviour-preserving moves, then the larger structural
> moves, then the new tests. Tasks 5 (Sleep removal) and 8 (PSS test)
> are independent and can shift earlier in the order if convenient,
> but the written order keeps reviewer cost monotonically rising.

**Goal:** Land the nine controller dedup/cleanup items C-01 through
C-09 from `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
as a single coherent "controller hygiene pass" PR.

**Architecture:** Four phases, nine tasks, all on
`feature/controller-dedup-pass`. Phase 1 (Tasks 1–3) extracts the two
duplicated bodies and moves a misplaced helper. Phase 2 (Tasks 4–5)
makes a single small behaviour change (one-resolve-per-reconcile) and
swaps a brittle `time.Sleep` for a deterministic cache-sync. Phase 3
(Tasks 6–7) introduces `ProxyBrokerConfig` to dedup the nine reconciler
config fields and extracts `reconcileCredentials` from the inline
70-LOC block in `Reconcile`. Phase 4 (Tasks 8–9) adds the missing PSS
test for the seed pod and promotes `fakeBroker` to a reusable
`testutil` package. No file splits beyond `conditions.go` and the
seed-NP move.

**Tech Stack:** Go 1.24, controller-runtime, envtest, Ginkgo/Gomega
(controller suite tests), plain `testing` (`pod_spec_test.go`-style
unit tests), `k8s.io/pod-security-admission/policy` (PSS evaluator).
Tools: `Read`, `Edit`, `Write`, `Bash` (`go test`, `golangci-lint`,
`make test`, `git`).

---

## Working assumptions

- **Branch:** `feature/controller-dedup-pass` (already created,
  rebased onto `main` at commit `a071380`, force-pushed to origin).
- **Working directory:** `/Users/ttj/projects/personal/paddock`.
- **Spec:** `docs/superpowers/specs/2026-04-26-controller-dedup-pass-design.md` —
  re-read whenever a task says "per the design."
- **Findings reference:** mini-cards C-01 through C-09 in
  `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`.
- **Pre-commit hook** runs `go vet -tags=e2e ./...` and
  `golangci-lint run`. Don't bypass; fix and create a new commit.
- **Test cadence:** after every task, `go test ./internal/controller/...`
  must pass. After Tasks 6, 7, 9 (the larger ones), also run
  `go test ./...`. After Task 9 land, run the full e2e suite locally:
  `make test-e2e 2>&1 | tee /tmp/e2e.log`.
- **Commit style:** Conventional Commits, `refactor(controller): …`
  for the dedup tasks, `test(controller): …` for the test additions,
  `chore(controller): …` for the file-move-only step. No `!` markers
  — none of these tasks change a CRD or CLI flag (Task 6 adds a new
  flag, no existing one changes).

---

## File structure

**Files created:**
- `internal/controller/conditions.go` — new home for `setCondition`
  (Task 3). Empty entry-point file other condition helpers can grow
  into.
- `internal/controller/proxybroker_config.go` — new home for
  `ProxyBrokerConfig` struct (Task 6).
- `internal/controller/testutil/fake_broker.go` — exported
  `FakeBroker` (Task 9).

**Files modified:**
- `internal/controller/broker_ca.go` — `ensureBrokerCA` becomes a
  one-liner over `copyCAToSecret` (Task 1). Hosts `copyCAToSecret`
  itself.
- `internal/controller/workspace_broker.go` — `ensureSeedBrokerCA`
  becomes a one-liner over `copyCAToSecret` (Task 1).
- `internal/controller/network_policy.go` — gains
  `buildEgressNetworkPolicy` builder; `buildRunNetworkPolicy` becomes
  a wrapper. Also gains the relocated `buildSeedNetworkPolicy` (Task 2).
- `internal/controller/workspace_seed.go` — loses
  `buildSeedNetworkPolicy` (moved to `network_policy.go` in Task 2).
- `internal/controller/workspace_controller.go` — loses
  `setCondition` (moved to `conditions.go` in Task 3); gains
  `ProxyBrokerConfig` embedding (Task 6).
- `internal/controller/harnessrun_controller.go` — `ensureJob`
  signature gains `decision policy.InterceptionDecision` parameter
  (Task 4); reconciler struct shrinks via `ProxyBrokerConfig`
  embedding (Task 6); credential block extracted into
  `reconcileCredentials` (Task 7).
- `internal/controller/suite_test.go` — `time.Sleep(500ms)` replaced
  with `mgr.GetCache().WaitForCacheSync(ctx)` (Task 5).
- `internal/controller/broker_credentials_test.go` — `fakeBroker`
  references rewritten to `testutil.FakeBroker` (Task 9).
- `cmd/main.go` — populates one `ProxyBrokerConfig` and assigns to
  both reconcilers; gains `--broker-port` flag (Task 6).

**Test files created:**
- `internal/controller/workspace_seed_psstest_test.go` — new
  `TestSeedJobPodSpec_PSSRestricted` (Task 8).
- `internal/controller/testutil/fake_broker_test.go` — sanity-check
  test that `*FakeBroker` satisfies `controller.BrokerIssuer` and
  that the basic Issue-by-name path returns the configured value
  (Task 9).

**Test files modified:**
- `internal/controller/harnessrun_controller_test.go` — if any test
  calls `ensureJob` directly, update for new signature (Task 4).

---

## Task 1: Extract `copyCAToSecret` (C-01)

**Files:**
- Modify: `internal/controller/broker_ca.go:43-102`
- Modify: `internal/controller/workspace_broker.go:102-141`
- Test: `internal/controller/ca_projected_test.go` (existing — must
  still pass), plus the broker_credentials/workspace test surface
  generally exercising both ensure functions.

The two CA-copy functions today differ only in:
- caller-passed owner type (`*HarnessRun` vs `*Workspace`)
- destination Secret name + namespace + labels
- whether `r.brokerProxyConfigured()` short-circuits (only the
  `HarnessRunReconciler` carries it; the Workspace path is gated at
  call sites)
- whether the create-only branch emits an audit event (only
  `ensureBrokerCA` does, via `r.Audit.EmitCAProjected`)

The shared helper will not gate on `brokerProxyConfigured()` (that's
a caller concern) and will not emit the audit event (caller can do
so when the helper reports `op == Created` via a third return value).

- [ ] **Step 1: Read the two existing functions side-by-side**

```bash
sed -n '43,102p' internal/controller/broker_ca.go
sed -n '102,141p' internal/controller/workspace_broker.go
```

Confirm the only differences are owner type, destination Secret
identity, the brokerProxyConfigured short-circuit, and the audit
emit. Cross-check with the design doc §1.

- [ ] **Step 2: Write a unit test for `copyCAToSecret`**

Add a new file `internal/controller/copy_ca_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
...
*/

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestCopyCAToSecret_PropagatesCABundle(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-serving-cert", Namespace: "paddock-system"},
		Data:       map[string][]byte{"ca.crt": []byte("PEM-BUNDLE")},
	}
	owner := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-x", Namespace: "team-a", UID: "u1"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src).Build()

	created, err := copyCAToSecret(context.Background(), cli, scheme, owner,
		types.NamespacedName{Namespace: "paddock-system", Name: "broker-serving-cert"},
		"run-x-broker-ca", "team-a",
		map[string]string{"app.kubernetes.io/component": "harnessrun-broker-ca"})
	if err != nil {
		t.Fatalf("copyCAToSecret: %v", err)
	}
	if !created {
		t.Errorf("expected created=true on first call")
	}

	var got corev1.Secret
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "run-x-broker-ca"}, &got); err != nil {
		t.Fatalf("get dst: %v", err)
	}
	if string(got.Data["ca.crt"]) != "PEM-BUNDLE" {
		t.Errorf("dst ca.crt = %q, want PEM-BUNDLE", got.Data["ca.crt"])
	}
	if got.Labels["app.kubernetes.io/component"] != "harnessrun-broker-ca" {
		t.Errorf("dst labels missing component label: %#v", got.Labels)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != "u1" {
		t.Errorf("owner ref not set: %#v", got.OwnerReferences)
	}
}

func TestCopyCAToSecret_SourceMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = paddockv1alpha1.AddToScheme(scheme)
	owner := &paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: "run-x", Namespace: "team-a", UID: "u1"}}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()

	created, err := copyCAToSecret(context.Background(), cli, scheme, owner,
		types.NamespacedName{Namespace: "paddock-system", Name: "missing"},
		"run-x-broker-ca", "team-a", nil)
	if err != nil {
		t.Fatalf("copyCAToSecret: %v", err)
	}
	if created {
		t.Errorf("expected created=false when source missing")
	}
}

func TestCopyCAToSecret_SourceEmpty(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = paddockv1alpha1.AddToScheme(scheme)
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-serving-cert", Namespace: "paddock-system"},
		Data:       map[string][]byte{"ca.crt": nil},
	}
	owner := &paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: "run-x", Namespace: "team-a", UID: "u1"}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src).Build()

	created, err := copyCAToSecret(context.Background(), cli, scheme, owner,
		types.NamespacedName{Namespace: "paddock-system", Name: "broker-serving-cert"},
		"run-x-broker-ca", "team-a", nil)
	if err != nil {
		t.Fatalf("copyCAToSecret: %v", err)
	}
	if created {
		t.Errorf("expected created=false when source ca.crt is empty")
	}
}
```

- [ ] **Step 3: Run the test — must FAIL with "undefined: copyCAToSecret"**

```bash
go test ./internal/controller/ -run TestCopyCAToSecret -count=1
```

Expected: build error mentioning `undefined: copyCAToSecret`.

- [ ] **Step 4: Add `copyCAToSecret` to `broker_ca.go`**

Insert just below `brokerCAKey` (around line 41), before
`ensureBrokerCA`:

```go
// copyCAToSecret copies the ca.crt key out of the source Secret into a
// destination Secret in the owner's namespace, owned by `owner`. Returns
// (created, err):
//   - created=false, err=nil: source Secret missing or its ca.crt key
//     missing/empty. Caller flips its waiting condition + requeues.
//   - created=false, err!=nil: transient API error.
//   - created=true on the first reconcile pass that materialises dst;
//     subsequent passes (including no-op updates) return false.
//
// Conflict errors on CreateOrUpdate are swallowed (the next reconcile
// re-tries) per the optimistic-concurrency convention canonicalised in
// ADR-0017.
func copyCAToSecret(
	ctx context.Context,
	cli client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	src types.NamespacedName,
	dstName, dstNamespace string,
	labels map[string]string,
) (bool, error) {
	var srcSec corev1.Secret
	if err := cli.Get(ctx, src, &srcSec); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading CA source Secret %s/%s: %w",
			src.Namespace, src.Name, err)
	}
	ca, ok := srcSec.Data[brokerCAKey]
	if !ok || len(ca) == 0 {
		return false, nil
	}
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dstName,
			Namespace: dstNamespace,
			Labels:    labels,
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, cli, dst, func() error {
		if err := controllerutil.SetControllerReference(owner, dst, scheme); err != nil {
			return err
		}
		dst.Type = corev1.SecretTypeOpaque
		dst.Data = map[string][]byte{brokerCAKey: ca}
		return nil
	})
	if err != nil && !apierrors.IsConflict(err) {
		return false, fmt.Errorf("upserting %s/%s: %w", dstNamespace, dstName, err)
	}
	return op == controllerutil.OperationResultCreated, nil
}
```

Add the missing import `"k8s.io/apimachinery/pkg/runtime"` and
`"sigs.k8s.io/controller-runtime/pkg/client"` to the file's import
block if not already there.

- [ ] **Step 5: Run the new test — must PASS**

```bash
go test ./internal/controller/ -run TestCopyCAToSecret -count=1
```

Expected: PASS for all three sub-tests.

- [ ] **Step 6: Replace `ensureBrokerCA` body with helper call**

Edit `internal/controller/broker_ca.go` lines 58–102 so the function
collapses to:

```go
func (r *HarnessRunReconciler) ensureBrokerCA(ctx context.Context, run *paddockv1alpha1.HarnessRun) (bool, error) {
	if !r.brokerProxyConfigured() {
		return true, nil
	}
	dstName := brokerCASecretName(run.Name)
	created, err := copyCAToSecret(ctx, r.Client, r.Scheme, run,
		types.NamespacedName{Namespace: r.BrokerCASource.Namespace, Name: r.BrokerCASource.Name},
		dstName, run.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "harnessrun-broker-ca",
			"paddock.dev/run":             run.Name,
		})
	if err != nil {
		return false, err
	}
	if created {
		r.Audit.EmitCAProjected(ctx, run.Name, run.Namespace, dstName)
	}
	// Source missing/empty: helper returns created=false, err=nil. The
	// helper has no way to distinguish "source missing" from "source
	// already projected on a prior pass" via `created` alone, so we
	// re-check the destination Secret to derive the (ok bool) the
	// caller needs.
	if err == nil && !created {
		var dst corev1.Secret
		getErr := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: dstName}, &dst)
		if apierrors.IsNotFound(getErr) {
			return false, nil
		}
		if getErr != nil {
			return false, getErr
		}
		if len(dst.Data[brokerCAKey]) == 0 {
			return false, nil
		}
	}
	return true, nil
}
```

**Note on the recheck:** The original API returns `(ok bool, err
error)` where `ok=true` means "the destination Secret has a
non-empty bundle." The helper's `created` boolean does not carry
that information on the steady-state path. The recheck is the
minimal-surgery way to preserve the caller contract. (If the
recheck feels ugly, an alternative is a fourth return value `ok
bool` from the helper — feel free to refactor that way; the test
should already exercise both shapes via the source-missing /
source-empty cases.)

- [ ] **Step 7: Replace `ensureSeedBrokerCA` body with helper call**

Edit `internal/controller/workspace_broker.go` lines 104–141:

```go
func (r *WorkspaceReconciler) ensureSeedBrokerCA(ctx context.Context, ws *paddockv1alpha1.Workspace) (bool, error) {
	dstName := workspaceBrokerCASecretName(ws.Name)
	created, err := copyCAToSecret(ctx, r.Client, r.Scheme, ws,
		types.NamespacedName{Namespace: r.BrokerCASource.Namespace, Name: r.BrokerCASource.Name},
		dstName, ws.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "workspace-broker-ca",
			"paddock.dev/workspace":       ws.Name,
		})
	if err != nil {
		return false, err
	}
	_ = created
	if !created {
		var dst corev1.Secret
		getErr := r.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: dstName}, &dst)
		if apierrors.IsNotFound(getErr) {
			return false, nil
		}
		if getErr != nil {
			return false, getErr
		}
		if len(dst.Data[brokerCAKey]) == 0 {
			return false, nil
		}
	}
	return true, nil
}
```

(Same recheck pattern as the run path. The Workspace path does not
emit an audit event today; preserve that.)

- [ ] **Step 8: Run the full controller suite**

```bash
go test ./internal/controller/... -count=1
```

Expected: PASS across all packages. `ca_projected_test.go`,
`broker_credentials_test.go`, `workspace_seed_broker_test.go`, and
the integration suite all exercise both ensure functions.

- [ ] **Step 9: Lint**

```bash
golangci-lint run ./internal/controller/...
```

Expected: clean (no new findings introduced).

- [ ] **Step 10: Commit**

```bash
git add internal/controller/broker_ca.go \
        internal/controller/workspace_broker.go \
        internal/controller/copy_ca_test.go
git commit -m "$(cat <<'EOF'
refactor(controller): extract copyCAToSecret helper (C-01)

ensureBrokerCA and ensureSeedBrokerCA were near-identical 40-LOC
functions. Extract the shared body into a package-level
copyCAToSecret helper so any change to the CA-copy logic — including
the F-44/F-51 silent-loop fix — lands in exactly one place.

Both wrappers preserve the (ok, err) contract via a steady-state
re-Get of the destination Secret; the helper itself only reports
whether *this* reconcile pass created the destination.
EOF
)"
```

---

## Task 2: Extract `buildEgressNetworkPolicy` and relocate seed builder (C-02)

**Files:**
- Modify: `internal/controller/network_policy.go:148-257` —
  introduce `buildEgressNetworkPolicy`, refactor
  `buildRunNetworkPolicy` to call it.
- Modify: `internal/controller/network_policy.go` (append) — add
  the relocated `buildSeedNetworkPolicy`.
- Modify: `internal/controller/workspace_seed.go:660-757` — delete
  `buildSeedNetworkPolicy` (moved). Keep `seedNetworkPolicyName`
  where it is (it's used by other callers in `workspace_seed.go`).
- Test: `internal/controller/network_policy_test.go` (existing —
  must still pass), `workspace_seed_test.go` (existing — must still
  pass).

The two builders differ only in:
- Pod selector (`paddock.dev/run` vs `paddock.dev/workspace`)
- Object name (`runNetworkPolicyName(run.Name)` vs
  `seedNetworkPolicyName(ws)`)
- Namespace (`run.Namespace` vs `ws.Namespace`)
- ObjectMeta labels (component + owner-key pairs)

The egress-rule list (DNS, TCP/443, TCP/80, broker rule, apiserver
rule) is byte-for-byte identical.

- [ ] **Step 1: Confirm structural identity**

```bash
diff <(sed -n '166,257p' internal/controller/network_policy.go) \
     <(sed -n '669,757p' internal/controller/workspace_seed.go)
```

The diff should show only header line, selector label key, name
function, namespace field, and label component lines as different.

- [ ] **Step 2: Read the existing run-NP test to understand the shape**

```bash
sed -n '1,80p' internal/controller/network_policy_test.go
```

Note which assertions are structural (egress rules in order,
broker rule presence, apiserver rule presence) vs identity-shaped
(pod selector matches the run). The new builder is exercised
indirectly via these existing tests.

- [ ] **Step 3: Add `buildEgressNetworkPolicy` to `network_policy.go`**

Insert just above `buildRunNetworkPolicy` (around line 148). The
signature follows the design (§Phase 1, Step 2):

```go
// buildEgressNetworkPolicy renders the shared defence-in-depth egress
// NetworkPolicy used by both per-run pods (HarnessRun reconciler) and
// per-workspace seed pods (Workspace reconciler). The selector,
// object identity, and ObjectMeta labels differ between the two
// callers; the egress rule list (DNS to kube-dns, TCP/443 + TCP/80
// to public-internet excluding cluster CIDRs, optional broker rule,
// optional apiserver rule) is identical. See findings F-19 and F-45.
func buildEgressNetworkPolicy(
	selector metav1.LabelSelector,
	name, namespace string,
	labels map[string]string,
	cfg networkPolicyConfig,
) *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	dnsPort := intstr.FromInt32(53)
	httpsPort := intstr.FromInt32(443)
	httpPort := intstr.FromInt32(80)
	exceptCIDRs := buildExceptCIDRs(cfg)

	rules := []networkingv1.NetworkPolicyEgressRule{
		{
			To: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": "kube-system",
						},
					},
					PodSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"k8s-app": "kube-dns"},
					},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &udp, Port: &dnsPort},
				{Protocol: &tcp, Port: &dnsPort},
			},
		},
		{
			To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: openCIDR, Except: exceptCIDRs}},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &httpsPort},
			},
		},
		{
			To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: openCIDR, Except: exceptCIDRs}},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &httpPort},
			},
		},
	}
	if brokerRule := buildBrokerEgressRule(cfg); brokerRule != nil {
		rules = append(rules, *brokerRule)
	}
	// Apiserver allow rule. Sidecars (collector for AuditEvent emission,
	// adapter for status writes) need TCP/443 to the kube-apiserver.
	// Pod-wide because NetworkPolicy operates at pod level. F-41.
	if len(cfg.APIServerIPs) > 0 {
		apiPeers := make([]networkingv1.NetworkPolicyPeer, 0, len(cfg.APIServerIPs))
		for _, ip := range cfg.APIServerIPs {
			apiPeers = append(apiPeers, networkingv1.NetworkPolicyPeer{
				IPBlock: &networkingv1.IPBlock{CIDR: ip.String() + "/32"},
			})
		}
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{
			To: apiPeers,
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &httpsPort},
			},
		})
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: selector,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
}
```

- [ ] **Step 4: Replace `buildRunNetworkPolicy` body with delegation**

Edit `internal/controller/network_policy.go` lines 166–257 down to:

```go
func buildRunNetworkPolicy(run *paddockv1alpha1.HarnessRun, cfg networkPolicyConfig) *networkingv1.NetworkPolicy {
	return buildEgressNetworkPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"paddock.dev/run": run.Name}},
		runNetworkPolicyName(run.Name), run.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "harnessrun-egress",
			"paddock.dev/run":             run.Name,
		},
		cfg,
	)
}
```

Keep the doc comment block above the function (lines 148–165) — it
still describes what the run NP is for, just delegating its body.

- [ ] **Step 5: Move `buildSeedNetworkPolicy` to `network_policy.go`**

In `internal/controller/network_policy.go`, append just below
`buildRunNetworkPolicy`:

```go
// buildSeedNetworkPolicy mirrors buildRunNetworkPolicy for workspace
// seed Pods. Selector matches the workspace's seed Pod (labeled
// paddock.dev/workspace=<name>); egress shape is identical (delegates
// to buildEgressNetworkPolicy). See finding F-45.
func buildSeedNetworkPolicy(ws *paddockv1alpha1.Workspace, cfg networkPolicyConfig) *networkingv1.NetworkPolicy {
	return buildEgressNetworkPolicy(
		metav1.LabelSelector{MatchLabels: map[string]string{"paddock.dev/workspace": ws.Name}},
		seedNetworkPolicyName(ws), ws.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "workspace-seed-egress",
			"paddock.dev/workspace":       ws.Name,
		},
		cfg,
	)
}
```

- [ ] **Step 6: Delete the old `buildSeedNetworkPolicy` from `workspace_seed.go`**

In `internal/controller/workspace_seed.go`, delete lines 664–757
(the whole `buildSeedNetworkPolicy` function and its preceding
comment). Leave `seedNetworkPolicyName` (line 660) where it is —
it's used by other callers in `workspace_seed.go` (the
ensure/delete pair).

- [ ] **Step 7: Build to confirm imports stayed consistent**

```bash
go build ./internal/controller/...
```

If `workspace_seed.go` becomes unused-import-noisy after the
deletion, remove the now-orphaned imports (likely
`networkingv1`, `intstr`, etc. — only if no other function in
the file still uses them; spot-check before removing).

- [ ] **Step 8: Run the controller tests**

```bash
go test ./internal/controller/... -count=1
```

Expected: PASS. `network_policy_test.go` exercises the run NP shape;
`workspace_seed_test.go` and `workspace_seed_broker_test.go`
exercise the seed NP path through the reconciler.

- [ ] **Step 9: Lint**

```bash
golangci-lint run ./internal/controller/...
```

Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add internal/controller/network_policy.go \
        internal/controller/workspace_seed.go
git commit -m "$(cat <<'EOF'
refactor(controller): extract buildEgressNetworkPolicy + move seed builder (C-02)

buildRunNetworkPolicy and buildSeedNetworkPolicy were structural
copies of the same 75-LOC egress rule builder. Extract the shared
body into buildEgressNetworkPolicy parameterised on selector, name,
namespace, and ObjectMeta labels. Each caller becomes a 10-line
wrapper. Any new egress rule (e.g. UDP/443 for QUIC/HTTP3) now
lands in exactly one place.

Also relocate buildSeedNetworkPolicy from workspace_seed.go to
network_policy.go where it logically belongs.
EOF
)"
```

---

## Task 3: Move `setCondition` to `conditions.go` (C-05)

**Files:**
- Create: `internal/controller/conditions.go`
- Modify: `internal/controller/workspace_controller.go:471-489`

Move-only. No signature change. Both reconcilers continue to call
`setCondition` exactly as before.

- [ ] **Step 1: Create `internal/controller/conditions.go`**

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
)

// setCondition sets or replaces the condition of the given type on the
// slice. Preserves LastTransitionTime when Status doesn't change.
//
// Used by both reconcilers (HarnessRun and Workspace) and the
// BrokerPolicy reconciler's discovery-conditions helper. Lives in its
// own file so readers of any reconciler can find it in the obvious
// place; future condition helpers (e.g. a unified setConditionWithLTT
// that absorbs applyDiscoveryConditions) can grow alongside it.
func setCondition(conds *[]metav1.Condition, c metav1.Condition) {
	now := metav1.Now()
	for i, existing := range *conds {
		if existing.Type != c.Type {
			continue
		}
		if existing.Status == c.Status {
			c.LastTransitionTime = existing.LastTransitionTime
		} else {
			c.LastTransitionTime = now
		}
		(*conds)[i] = c
		return
	}
	c.LastTransitionTime = now
	*conds = append(*conds, c)
}
```

- [ ] **Step 2: Delete the original from `workspace_controller.go`**

Remove lines 471–489 in `internal/controller/workspace_controller.go`
(the comment block + the `setCondition` function body).

- [ ] **Step 3: Build to confirm nothing else regressed**

```bash
go build ./internal/controller/...
```

Expected: success. If `metav1` import in `workspace_controller.go`
is now unused, remove it (it almost certainly is still used by
other code in that file — spot-check).

- [ ] **Step 4: Run controller tests**

```bash
go test ./internal/controller/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Lint**

```bash
golangci-lint run ./internal/controller/...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/conditions.go \
        internal/controller/workspace_controller.go
git commit -m "$(cat <<'EOF'
chore(controller): move setCondition to conditions.go (C-05)

setCondition lived in workspace_controller.go but is used by both
reconcilers (HarnessRun and Workspace) and the discovery-conditions
helper in BrokerPolicy. Move it to a new conditions.go entry-point
file so readers of any reconciler find it in the obvious place.

Pure relocation — no signature or behaviour change. Future condition
helpers (e.g. a unified setCondition that absorbs
applyDiscoveryConditions) can grow alongside it.
EOF
)"
```

---

## Task 4: Resolve `resolveInterceptionMode` once per reconcile (C-04)

**Files:**
- Modify: `internal/controller/harnessrun_controller.go:474` (Reconcile)
  and `:1054-1111` (ensureJob signature + body).
- Possibly modify: `internal/controller/harnessrun_controller_test.go`
  if any test calls `ensureJob` directly. The
  `interception_resolve_test.go` exercises `resolveInterceptionMode`
  in isolation — should not need changes.

Today `resolveInterceptionMode` runs twice per reconcile (once at
line 474 for the EgressConfigured condition, once at line 1074
inside `ensureJob` for the Job spec). Both calls list BrokerPolicies
and read the namespace's PSA labels. Wasteful + small TOCTOU window.

- [ ] **Step 1: Verify call sites**

```bash
grep -n "resolveInterceptionMode" internal/controller/*.go
```

Expected: definition at `harnessrun_controller.go:1219`, callers at
`harnessrun_controller.go:474` and `harnessrun_controller.go:1074`,
plus `interception_resolve_test.go` callers (which exercise the
resolver directly and are fine to keep).

- [ ] **Step 2: Inspect the `policy.InterceptionDecision` type**

```bash
grep -n "type InterceptionDecision" internal/policy/*.go
```

Confirm the public name is `policy.InterceptionDecision` so we know
what to import + spell in the new parameter type.

- [ ] **Step 3: Change `ensureJob` signature**

In `internal/controller/harnessrun_controller.go` line 1054, change:

```go
func (r *HarnessRunReconciler) ensureJob(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	tpl *resolvedTemplate,
	pvcName string,
) (*batchv1.Job, error) {
```

to:

```go
// ensureJob builds and creates the backing Job. No-op when one
// already exists. The `decision` parameter is the resolved
// interception mode for the proxy-enabled path; callers in the
// non-proxy-configured path may pass a zero-value decision (it
// won't be consulted). Resolving once at the top of Reconcile
// (rather than recomputing here) avoids a duplicate
// BrokerPolicy List + PSA-label read and closes a small TOCTOU
// window where the EgressConfigured condition and the Job spec
// could disagree if a BrokerPolicy changed between reads.
func (r *HarnessRunReconciler) ensureJob(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	tpl *resolvedTemplate,
	pvcName string,
	decision policy.InterceptionDecision,
) (*batchv1.Job, error) {
```

(`policy` is already imported in this file — verify. If not, add
`policy "paddock.dev/paddock/internal/policy"` to the import block.)

- [ ] **Step 4: Drop the duplicate resolve inside `ensureJob`**

Replace lines 1074–1083 (the in-function `resolveInterceptionMode`
call + Unavailable check) with use of the parameter:

```go
	if r.proxyConfigured() {
		in.proxyImage = r.ProxyImage
		in.proxyTLSSecret = proxyTLSSecretName(run.Name)
		in.proxyAllowList = r.ProxyAllowList
		if decision.Unavailable {
			// The reconcile CA-ready path above already emitted the
			// event and marked the run Failed. Defensive guard: refuse
			// to build a Job in this state.
			return nil, fmt.Errorf("interception unavailable: %s", decision.Reason)
		}
		in.interceptionMode = decision.Mode
		if decision.Mode == paddockv1alpha1.InterceptionModeTransparent {
			in.iptablesInitImage = r.IPTablesInitImage
		}
		if r.brokerProxyConfigured() {
			in.brokerEndpoint = r.BrokerEndpoint
			in.brokerCASecret = brokerCASecretName(run.Name)
		}
	}
```

(`ctx` is now no longer used inside the proxy block. Suppress the
linter only if `ctx` becomes truly unused across the entire
function — almost certainly it's still used by `r.Get`/`r.Create`
elsewhere in `ensureJob`.)

- [ ] **Step 5: Pass the resolved decision from `Reconcile`**

In `internal/controller/harnessrun_controller.go` lines 474–515 (the
proxy-configured block in `Reconcile`), the `decision` is already in
scope after the `r.resolveInterceptionMode(ctx, &run, tpl)` call at
line 474. Hold it in a variable visible to the `ensureJob` call at
line 554.

The `decision` variable is currently scoped to the `if
r.proxyConfigured()` block. Hoist its declaration:

```go
	var decision policy.InterceptionDecision
	if r.proxyConfigured() {
		// ... existing proxy-configured branch, but now write to the
		// outer `decision`:
		var mErr error
		decision, mErr = r.resolveInterceptionMode(ctx, &run, tpl)
		if mErr != nil {
			return ctrl.Result{}, mErr
		}
		// ... rest of the existing branch unchanged
	} else {
		// ... existing else branch unchanged
	}
```

Then update the `ensureJob` call at line 554:

```go
	job, err := r.ensureJob(ctx, &run, tpl, ws.Status.PVCName, decision)
```

- [ ] **Step 6: Hunt down any other ensureJob callers**

```bash
grep -n "ensureJob(" internal/controller/*.go
```

Expected: one definition + one call site (the one we just updated).
If a test file calls it directly, update the call site to pass a
zero-value decision (or a sentinel for the test scenario).

- [ ] **Step 7: Run controller tests**

```bash
go test ./internal/controller/... -count=1
```

Expected: PASS. The `interception_resolve_test.go` and
`harnessrun_controller_test.go` cover the relevant code paths.

- [ ] **Step 8: Lint**

```bash
golangci-lint run ./internal/controller/...
```

Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add internal/controller/harnessrun_controller.go
[ -n "$(git status --porcelain internal/controller/harnessrun_controller_test.go)" ] && \
  git add internal/controller/harnessrun_controller_test.go
git commit -m "$(cat <<'EOF'
refactor(controller): resolve interception mode once per reconcile (C-04)

resolveInterceptionMode was called twice per reconcile pass — once
at the top of the proxy-enabled block to set EgressConfigured, then
again inside ensureJob to populate the Job spec. Both calls List
BrokerPolicies and read the namespace's PSA labels. The duplicate
work wasted API traffic on every reconcile and opened a small TOCTOU
window where the condition and the Job spec could disagree if a
BrokerPolicy changed between reads.

Hoist the resolved decision into a Reconcile-scoped variable and
pass it to ensureJob as a parameter.
EOF
)"
```

---

## Task 5: Replace `time.Sleep(500ms)` with cache-sync wait (C-08)

**Files:**
- Modify: `internal/controller/suite_test.go:100-106`

The existing settle Sleep is brittle on a loaded CI host (spurious
failures) and wasteful on a fast workstation.
`mgr.GetCache().WaitForCacheSync(ctx)` is the deterministic
replacement — it returns when the controller-runtime informers
have synced their caches.

- [ ] **Step 1: Inspect the current goroutine + sleep block**

```bash
sed -n '95,115p' internal/controller/suite_test.go
```

Confirm the structure: `go func() { mgr.Start(ctx) }()` followed by
`time.Sleep(500 * time.Millisecond)`.

- [ ] **Step 2: Replace the Sleep with WaitForCacheSync**

Edit `internal/controller/suite_test.go` lines 100–107:

```go
	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()

	// Wait for controller-runtime informer caches to sync before any
	// spec runs. Replaces an earlier 500ms sleep that was brittle on
	// loaded CI hosts and wasteful on fast workstations.
	Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())
})
```

- [ ] **Step 3: Check whether `time` import is still used**

```bash
grep -n "time\." internal/controller/suite_test.go
```

If the file no longer uses `time`, remove the import.

- [ ] **Step 4: Run the controller suite once normally**

```bash
go test ./internal/controller/... -count=1
```

Expected: PASS, no spurious flakes. If this is the first failure
mode you hit, the cache-sync timing differs from what we expected;
investigate before forcing it.

- [ ] **Step 5: Run the controller suite under load**

(Optional, but recommended per the design doc — confirms no
regression on a throttled host.)

```bash
# In one shell, generate CPU pressure:
yes >/dev/null & yes >/dev/null & yes >/dev/null & yes >/dev/null &
# In another shell:
go test ./internal/controller/... -count=3
# Then kill the load generators:
kill %1 %2 %3 %4
```

Expected: PASS across 3 runs even under load.

- [ ] **Step 6: Lint**

```bash
golangci-lint run ./internal/controller/...
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/controller/suite_test.go
git commit -m "$(cat <<'EOF'
test(controller): swap Sleep(500ms) for cache-sync wait (C-08)

The 500ms settle Sleep in the suite-level BeforeSuite was brittle on
loaded CI hosts (spurious failures) and wasteful on fast
workstations. Replace with mgr.GetCache().WaitForCacheSync(ctx),
which is the deterministic readiness signal controller-runtime
exposes for exactly this scenario.
EOF
)"
```

---

## Task 6: Introduce `ProxyBrokerConfig` (C-03)

**Files:**
- Create: `internal/controller/proxybroker_config.go` —
  `ProxyBrokerConfig` struct definition.
- Modify: `internal/controller/harnessrun_controller.go:67-161`
  (struct definition).
- Modify: `internal/controller/workspace_controller.go:49-88`
  (struct definition).
- Modify: `cmd/main.go` — populate one struct, assign to both;
  add `--broker-port` flag.

The nine fields duplicated across both reconcilers (`ProxyImage`,
`BrokerEndpoint`, `ProxyCAClusterIssuer`, `BrokerCASource`,
`NetworkPolicyEnforce`, `NetworkPolicyAutoEnabled`, `ClusterPodCIDR`,
`ClusterServiceCIDR`, `APIServerIPs`) plus the dangling `BrokerPort`
default-of-8443 collapse into one struct.

The design notes the option of landing this in two sub-steps
(Workspace first, HarnessRun second) for reviewer ease. We follow
that suggestion to keep the patch sequence clean.

### Sub-step 6a: Define `ProxyBrokerConfig` and embed in `WorkspaceReconciler`

- [ ] **Step 1: Create `proxybroker_config.go`**

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
	"net"
)

// ProxyBrokerConfig is the shared cluster-and-manager configuration
// that both reconcilers (HarnessRun + Workspace) need to render
// proxy-sidecar pod specs and per-pod NetworkPolicies. Defined once
// here, embedded in both reconciler structs, populated from one set
// of CLI flags in cmd/main.go.
//
// Adding a new flag now requires editing only this struct, the
// flag-parsing block, and the populate-then-assign pair in main.go —
// not four places (two reconciler structs + two assignments).
type ProxyBrokerConfig struct {
	// ProxyImage is the image used for the per-run egress proxy
	// sidecar. Empty disables the proxy sidecar (the run still
	// proceeds; EgressConfigured stays False with reason=ProxyNotConfigured).
	ProxyImage string

	// BrokerEndpoint is the in-cluster broker URL the proxy sidecar
	// calls for ValidateEgress + SubstituteAuth. Empty disables
	// broker-backed proxy enforcement (proxy falls back to
	// --proxy-allow static list).
	BrokerEndpoint string

	// BrokerNamespace is the namespace where the broker is deployed
	// (default `paddock-system`). Used by the per-pod NetworkPolicy
	// to allow broker egress when NP enforcement is on.
	BrokerNamespace string

	// BrokerPort is the broker's TLS service port. Defaults to 8443
	// when 0; populated from --broker-port at manager startup.
	// (Previously defaulted inside buildBrokerEgressRule with no CLI
	// override; promoted to a real flag in this refactor.)
	BrokerPort int32

	// BrokerCASource names the cert-manager-issued broker-serving-cert
	// Secret whose ca.crt is copied into per-run/per-workspace
	// broker-ca Secrets. Zero Name disables broker-CA copy.
	BrokerCASource BrokerCASource

	// ProxyCAClusterIssuer is the cert-manager ClusterIssuer (kind:
	// CA) that signs per-run intermediate CAs (F-18 / Phase 2f).
	// Empty disables proxy-TLS integration.
	ProxyCAClusterIssuer string

	// NetworkPolicyEnforce selects whether per-pod NetworkPolicy
	// objects are emitted. "auto" defers to the CNI probe.
	NetworkPolicyEnforce NetworkPolicyEnforceMode

	// NetworkPolicyAutoEnabled is set at manager startup from the
	// CNI probe when NetworkPolicyEnforce="auto".
	NetworkPolicyAutoEnabled bool

	// ClusterPodCIDR is the cluster's pod CIDR. Excluded from
	// per-pod NetworkPolicy public-internet egress (F-19).
	ClusterPodCIDR string

	// ClusterServiceCIDR is the cluster's service CIDR. Same
	// purpose as ClusterPodCIDR.
	ClusterServiceCIDR string

	// APIServerIPs is the set of IPv4 addresses the controller
	// resolves the kube-apiserver to (F-41). Each becomes a /32
	// allow rule in the per-pod NP.
	APIServerIPs []net.IP
}
```

- [ ] **Step 2: Embed in `WorkspaceReconciler`**

In `internal/controller/workspace_controller.go` lines 49–88,
replace the duplicated fields with the embedded struct:

```go
type WorkspaceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// SeedImage overrides the default alpine/git image. Primarily for
	// tests; production uses defaultSeedImage.
	SeedImage string

	// ProxyBrokerConfig carries the shared cluster-and-manager config
	// used to render seed-pod proxy sidecars and per-seed-Pod
	// NetworkPolicies. Populated once in cmd/main.go and embedded in
	// both reconcilers.
	ProxyBrokerConfig
}
```

The fields `ProxyImage`, `BrokerEndpoint`, `ProxyCAClusterIssuer`,
`BrokerCASource`, `NetworkPolicyEnforce`, `NetworkPolicyAutoEnabled`,
`ClusterPodCIDR`, `ClusterServiceCIDR`, `BrokerNamespace`,
`APIServerIPs` are all accessible as `r.ProxyImage` etc. via field
promotion — no call-site rewrites needed.

- [ ] **Step 3: Update `cmd/main.go` Workspace assignment**

Edit `cmd/main.go` lines 355–371:

```go
	if err := (&controller.WorkspaceReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		ProxyBrokerConfig: proxyBrokerCfg,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Workspace")
		os.Exit(1)
	}
```

Where `proxyBrokerCfg` is constructed once, just before the
HarnessRunReconciler block (we'll wire it fully in Step 5):

```go
	proxyBrokerCfg := controller.ProxyBrokerConfig{
		ProxyImage:               proxyImage,
		BrokerEndpoint:           brokerEndpoint,
		BrokerNamespace:          brokerNamespace,
		BrokerPort:               int32(brokerPort),
		BrokerCASource:           controller.BrokerCASource{Name: brokerCAName, Namespace: brokerCANamespace},
		ProxyCAClusterIssuer:     proxyCAClusterIssuer,
		NetworkPolicyEnforce:     npEnforce,
		NetworkPolicyAutoEnabled: npAuto,
		ClusterPodCIDR:           clusterPodCIDR,
		ClusterServiceCIDR:       clusterServiceCIDR,
		APIServerIPs:             apiserverIPs,
	}
```

- [ ] **Step 4: Add `--broker-port` flag**

In `cmd/main.go` (in the flag block around line 117), declare:

```go
	var brokerPort int
	flag.IntVar(&brokerPort, "broker-port", 8443,
		"TLS service port the broker listens on. Plumbed into per-pod NetworkPolicy egress rules.")
```

(Place it next to the other `--broker-*` flags.)

- [ ] **Step 5: Add `ProxyBrokerConfig` argument to `networkPolicyConfig`**

The existing `networkPolicyConfig` already has `BrokerPort int32`.
Where it's constructed (e.g. `network_policy.go:275-280` for the
run path, and the equivalent point in `workspace_seed.go` /
`workspace_controller.go` for the seed path), pass the value
through:

```go
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     r.ClusterPodCIDR,
		ClusterServiceCIDR: r.ClusterServiceCIDR,
		BrokerNamespace:    r.BrokerNamespace,
		BrokerPort:         r.BrokerPort,
		APIServerIPs:       r.APIServerIPs,
	}
```

(Search for `networkPolicyConfig{` to find every construction site
— there should be two in production code: `network_policy.go:275`
(run path) and `workspace_controller.go:444` (seed path). Test
files construct their own and may set or omit `BrokerPort`
deliberately; leave those untouched and let the test gate in
Step 6 catch any that need updating.)

```bash
grep -n "networkPolicyConfig{" internal/controller/*.go | grep -v _test.go
```

- [ ] **Step 6: Build + run controller tests**

```bash
go build ./...
go test ./internal/controller/... -count=1
```

Expected: PASS. Field-promotion makes the rename invisible to all
existing call sites; the only behavior change is that `BrokerPort`
now flows from a CLI flag.

- [ ] **Step 7: Commit sub-step 6a**

```bash
git add internal/controller/proxybroker_config.go \
        internal/controller/workspace_controller.go \
        internal/controller/network_policy.go \
        internal/controller/workspace_seed.go \
        cmd/main.go
git commit -m "$(cat <<'EOF'
refactor(controller): introduce ProxyBrokerConfig (Workspace half) (C-03 part 1)

Define ProxyBrokerConfig in proxybroker_config.go and embed it in
WorkspaceReconciler. Populate one struct in cmd/main.go and assign
to the Workspace reconciler. Promote the previously-defaulted
BrokerPort to a real --broker-port CLI flag (was hard-coded to 8443
inside buildBrokerEgressRule with no manager override).

Field-promotion keeps every existing call site working unchanged
(r.ProxyImage, r.BrokerEndpoint, etc. still resolve via the
embedded struct).

HarnessRunReconciler still carries its own copies of the fields;
the next commit embeds the struct there too.
EOF
)"
```

### Sub-step 6b: Embed in `HarnessRunReconciler`

- [ ] **Step 8: Embed `ProxyBrokerConfig` in `HarnessRunReconciler`**

In `internal/controller/harnessrun_controller.go` lines 67–161,
delete the nine duplicated fields (`ProxyImage`, `BrokerEndpoint`,
`BrokerNamespace`, `ProxyCAClusterIssuer`, `BrokerCASource`,
`NetworkPolicyEnforce`, `NetworkPolicyAutoEnabled`,
`ClusterPodCIDR`, `ClusterServiceCIDR`, `APIServerIPs`) and embed
the struct in their place:

```go
type HarnessRunReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// CollectorImage, RingMaxEvents, BrokerClient, ProxyAllowList,
	// IPTablesInitImage, Audit — fields specific to HarnessRun.
	CollectorImage    string
	RingMaxEvents     int
	BrokerClient      BrokerIssuer
	ProxyAllowList    string
	IPTablesInitImage string
	Audit             *ControllerAudit

	// ProxyBrokerConfig carries the shared cluster-and-manager config
	// used to render run-pod proxy sidecars and per-run NetworkPolicies.
	// Populated once in cmd/main.go and embedded in both reconcilers.
	ProxyBrokerConfig
}
```

(Preserve the doc comments on the HarnessRun-specific fields —
`CollectorImage`, `RingMaxEvents`, `BrokerClient`, `ProxyAllowList`,
`IPTablesInitImage`, `Audit`. The promoted-field comments live on
the `ProxyBrokerConfig` struct definition.)

- [ ] **Step 9: Update `cmd/main.go` HarnessRun assignment**

Edit `cmd/main.go` lines 303–326:

```go
	hrReconciler := &controller.HarnessRunReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		CollectorImage:    collectorImage,
		RingMaxEvents:     ringMaxEvents,
		ProxyAllowList:    proxyAllowList,
		IPTablesInitImage: iptablesInitImage,
		ProxyBrokerConfig: proxyBrokerCfg,
		Audit: &controller.ControllerAudit{
			Sink: &auditing.KubeSink{Client: mgr.GetClient(), Component: "controller"},
		},
	}
```

The `npAuto` mutation later in main.go (lines 339–346) writes
`hrReconciler.NetworkPolicyAutoEnabled = enabled` — this still
works via field promotion and writes to the embedded struct.

- [ ] **Step 10: Build + run controller tests**

```bash
go build ./...
go test ./internal/controller/... -count=1
```

Expected: PASS. The `harnessrun_controller_test.go` integration
suite constructs its own `HarnessRunReconciler` — search to see
whether any test directly initializes the now-removed fields:

```bash
grep -n "HarnessRunReconciler{" internal/controller/*.go
```

If any test sets `ProxyImage:` or similar in a struct literal,
move those into a `ProxyBrokerConfig{...}` literal under the new
embedded field name.

- [ ] **Step 11: Lint**

```bash
golangci-lint run ./...
```

Expected: clean.

- [ ] **Step 12: Commit sub-step 6b**

```bash
git add internal/controller/harnessrun_controller.go \
        cmd/main.go
[ -n "$(git status --porcelain internal/controller/harnessrun_controller_test.go)" ] && \
  git add internal/controller/harnessrun_controller_test.go
git commit -m "$(cat <<'EOF'
refactor(controller): embed ProxyBrokerConfig in HarnessRunReconciler (C-03 part 2)

Drop the nine duplicated config fields from HarnessRunReconciler in
favor of the embedded ProxyBrokerConfig struct. cmd/main.go now
assigns the same single struct to both reconcilers — adding a new
flag now requires editing the struct, the flag block, and the
populate site, not four places.
EOF
)"
```

---

## Task 7: Extract `reconcileCredentials` (C-06)

**Files:**
- Modify: `internal/controller/harnessrun_controller.go:351-425`
  (the inline credential block in `Reconcile`).
- Create or modify: `internal/controller/harnessrun_controller_test.go`
  (or a new `reconcile_credentials_test.go`) — direct unit test on
  the extracted method.

The inline 70-LOC block mixes four concerns: secret materialisation
(`ensureBrokerCredentials`), condition setting (BrokerReady,
BrokerCredentialsReady), event emission (`CredentialIssued`,
`InContainerCredentialDelivered`), and delivery-metadata collection
(`status.credentials`). Extract into a method that returns the
delivery-metadata + a reconcile result + an error; the handler
becomes a single call.

- [ ] **Step 1: Read the existing block carefully**

```bash
sed -n '350,430p' internal/controller/harnessrun_controller.go
```

Map every effect: which Status fields it writes (`Credentials`,
`Phase`, conditions), which events it emits, which exit paths it
has (early return on broker-fatal, early return on broker-pending
with requeue, fall through on success).

- [ ] **Step 2: Define the extracted method shape**

The method needs to convey four outcomes back to the caller:
1. **Fatal broker error** → caller does `r.fail(...)` + `commitStatus`.
2. **Broker pending** → caller does `commitStatus` + requeue 10s.
3. **Success** → caller continues.
4. **Transient error** → caller returns the error.

A clean signature:

```go
type credentialsReconcileOutcome struct {
	credStatus []paddockv1alpha1.CredentialStatus
	requeue    bool          // when true, caller commits + requeues 10s
	fatal      bool          // when true, caller marks BrokerReady fatal and commits
	fatalReason, fatalMsg string
}

// reconcileCredentials issues per-credential bearers via the broker,
// updates per-credential status + summary conditions + events, and
// reports back to the Reconcile loop how to proceed.
//
// Outcome interpretation (in caller):
//   - err != nil: transient failure; return (ctrl.Result{}, err) without
//     committing status changes the method has already written.
//   - outcome.fatal == true: r.fail with outcome.fatalReason/Msg, then
//     commitStatus.
//   - outcome.requeue == true: commitStatus then requeue 10s.
//   - otherwise: continue Reconcile.
//
// The method is idempotent: a steady-state reconcile loop with no
// broker change emits no new events (the EventRecorder dedupes by
// reason/message).
func (r *HarnessRunReconciler) reconcileCredentials(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	tpl *resolvedTemplate,
) (credentialsReconcileOutcome, error) {
	credsOk, credStatus, brFatalReason, brFatalMsg, brErr := r.ensureBrokerCredentials(ctx, run, tpl)
	if brErr != nil {
		return credentialsReconcileOutcome{}, brErr
	}
	if brFatalReason != "" {
		return credentialsReconcileOutcome{fatal: true, fatalReason: brFatalReason, fatalMsg: brFatalMsg}, nil
	}
	if !credsOk {
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:               paddockv1alpha1.HarnessRunConditionBrokerReady,
			Status:             metav1.ConditionFalse,
			Reason:             "BrokerUnavailable",
			Message:            "waiting on broker to issue credentials",
			ObservedGeneration: run.Generation,
		})
		run.Status.Phase = paddockv1alpha1.HarnessRunPhasePending
		return credentialsReconcileOutcome{requeue: true}, nil
	}

	run.Status.Credentials = credStatus
	nProxy, nInContainer := 0, 0
	for _, c := range credStatus {
		switch c.DeliveryMode {
		case paddockv1alpha1.DeliveryModeProxyInjected:
			nProxy++
		case paddockv1alpha1.DeliveryModeInContainer:
			nInContainer++
		}
	}
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:   paddockv1alpha1.HarnessRunConditionBrokerCredentialsReady,
		Status: metav1.ConditionTrue,
		Reason: "AllIssued",
		Message: fmt.Sprintf("%d credentials issued: %d proxy-injected, %d in-container",
			len(credStatus), nProxy, nInContainer),
		ObservedGeneration: run.Generation,
	})
	for _, c := range credStatus {
		switch c.DeliveryMode {
		case paddockv1alpha1.DeliveryModeProxyInjected:
			r.Recorder.Eventf(run, corev1.EventTypeNormal, "CredentialIssued",
				"name=%s mode=ProxyInjected provider=%s", c.Name, c.Provider)
		case paddockv1alpha1.DeliveryModeInContainer:
			reason := c.InContainerReason
			if len(reason) > 60 {
				reason = reason[:60] + "..."
			}
			r.Recorder.Eventf(run, corev1.EventTypeNormal, "InContainerCredentialDelivered",
				"name=%s reason=%q", c.Name, reason)
		}
	}

	brokerMsg := "no broker credentials required"
	if len(tpl.Spec.Requires.Credentials) > 0 {
		brokerMsg = fmt.Sprintf("broker issued %d credential(s)", len(tpl.Spec.Requires.Credentials))
	}
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionBrokerReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Issued",
		Message:            brokerMsg,
		ObservedGeneration: run.Generation,
	})
	return credentialsReconcileOutcome{credStatus: credStatus}, nil
}
```

- [ ] **Step 3: Add the extracted method**

Append the `credentialsReconcileOutcome` type and
`reconcileCredentials` method to
`internal/controller/harnessrun_controller.go` (e.g., just below
the existing `Reconcile` function block — before
`reconcileDelete`).

- [ ] **Step 4: Replace the inline block in `Reconcile`**

Edit `internal/controller/harnessrun_controller.go` lines 351–425
to:

```go
	// 4a. Issue broker-backed credentials for any requires.credentials
	// the template declares (ADR-0015). Delegated to
	// reconcileCredentials, which sets BrokerReady +
	// BrokerCredentialsReady, emits per-credential events, and writes
	// status.credentials.
	credOutcome, err := r.reconcileCredentials(ctx, &run, tpl)
	if err != nil {
		return ctrl.Result{}, err
	}
	if credOutcome.fatal {
		r.fail(&run, paddockv1alpha1.HarnessRunConditionBrokerReady, credOutcome.fatalReason, credOutcome.fatalMsg)
		return r.commitStatus(ctx, &run, origStatus)
	}
	if credOutcome.requeue {
		if _, err := r.commitStatus(ctx, &run, origStatus); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
```

- [ ] **Step 5: Add a direct test for `reconcileCredentials`**

The credential block already has integration coverage via the
suite spec on `ensureBrokerCredentials`, but the design specifies
"Test the credential path independently of the full reconcile
loop." Add a Ginkgo Describe in
`internal/controller/broker_credentials_test.go` (or a new
`reconcile_credentials_test.go`):

```go
var _ = Describe("reconcileCredentials", func() {
	const ns = "rc-creds-test"

	BeforeEach(func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(SatisfyAny(Succeed(), WithTransform(apierrors.IsAlreadyExists, BeTrue())))
	})

	It("returns success outcome and sets BrokerReady=True when the broker issues all credentials", func() {
		fb := &fakeBroker{values: map[string]string{"K": "v"}}
		r := &HarnessRunReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			Recorder:     record.NewFakeRecorder(8),
			BrokerClient: fb,
		}
		run := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{Name: "rc-success", Namespace: ns},
			Spec:       paddockv1alpha1.HarnessRunSpec{TemplateRef: paddockv1alpha1.TemplateRef{Name: "tpl"}, Prompt: "hi"},
		}
		Expect(k8sClient.Create(ctx, run)).To(Succeed())

		out, err := r.reconcileCredentials(ctx, run, &resolvedTemplate{
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Requires: paddockv1alpha1.RequireSpec{
					Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.fatal).To(BeFalse())
		Expect(out.requeue).To(BeFalse())
		Expect(out.credStatus).To(HaveLen(1))

		var ready *metav1.Condition
		for i, c := range run.Status.Conditions {
			if c.Type == paddockv1alpha1.HarnessRunConditionBrokerReady {
				ready = &run.Status.Conditions[i]
			}
		}
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
	})

	It("returns requeue outcome when the broker is unavailable", func() {
		fb := &fakeBroker{errs: map[string]error{"K": fmt.Errorf("connection refused")}}
		r := &HarnessRunReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			Recorder:     record.NewFakeRecorder(8),
			BrokerClient: fb,
		}
		run := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{Name: "rc-requeue", Namespace: ns},
			Spec:       paddockv1alpha1.HarnessRunSpec{TemplateRef: paddockv1alpha1.TemplateRef{Name: "tpl"}, Prompt: "hi"},
		}
		Expect(k8sClient.Create(ctx, run)).To(Succeed())

		out, err := r.reconcileCredentials(ctx, run, &resolvedTemplate{
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Requires: paddockv1alpha1.RequireSpec{
					Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.fatal).To(BeFalse())
		Expect(out.requeue).To(BeTrue())
	})
})
```

(If `record.NewFakeRecorder` import isn't already present in the
file, add `"k8s.io/client-go/tools/record"`. Adapt the
`fakeBroker` references if Task 9 has already shipped — see Task 9
for the rename.)

- [ ] **Step 6: Run controller tests**

```bash
go test ./internal/controller/... -count=1
```

Expected: PASS, including the two new specs.

- [ ] **Step 7: Run full module tests**

```bash
go test ./... -count=1
```

Expected: PASS. This task touches the central `Reconcile` loop —
worth the broader run.

- [ ] **Step 8: Lint**

```bash
golangci-lint run ./...
```

Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add internal/controller/harnessrun_controller.go \
        internal/controller/broker_credentials_test.go
git commit -m "$(cat <<'EOF'
refactor(controller): extract reconcileCredentials (C-06)

The credential block in Reconcile mixed four concerns inline (secret
materialisation, condition setting, event emission, delivery-metadata
collection) in 70 LOC of a 450-LOC Reconcile function. Extract into
a reconcileCredentials method returning a credentialsReconcileOutcome
the caller switches on.

Adds two direct Ginkgo specs against the extracted method (success +
broker-unavailable requeue) so the credential path can be verified
without spinning the full reconcile loop.
EOF
)"
```

---

## Task 8: Add seed-pod PSS-restricted test (C-07)

**Files:**
- Create: `internal/controller/workspace_seed_psstest_test.go`

The existing `pod_spec_test.go` runs the real PSS evaluator
(`k8s.io/pod-security-admission/policy`) against the run-pod spec.
The seed pod has no equivalent — a PSS regression in the seed pod
would not surface until e2e. Add a mirror.

Read the existing `TestBuildPodSpec_FirstPartyContainersPassPSSRestricted`
(`pod_spec_test.go:820`) for the test shape. The seed pod is built
by `seedJobForWorkspace`; the per-container shapes we'll re-evaluate
in isolation are the seed init containers + the optional seed
proxy sidecar.

- [ ] **Step 1: Inspect what `seedJobForWorkspace` returns**

```bash
sed -n '113,120p' internal/controller/workspace_seed.go
```

Confirm the function returns `*batchv1.Job`. The pod spec is at
`job.Spec.Template.Spec`.

- [ ] **Step 2: Find an existing test fixture for a seeded Workspace**

```bash
grep -n "func .*Workspace.*Fixture\|seedJobForWorkspace(" internal/controller/workspace_seed_test.go
```

Expected: existing helpers create a `*paddockv1alpha1.Workspace`
with `Spec.Seed.Repos` populated. Re-use one (or write a small
local one) so the test produces a real seed Job.

- [ ] **Step 3: Write the failing test**

Create `internal/controller/workspace_seed_psstest_test.go`:

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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	psapi "k8s.io/pod-security-admission/api"
	pspolicy "k8s.io/pod-security-admission/policy"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// TestSeedJobPodSpec_PSSRestricted asserts each first-party container
// in the seed Job's pod spec, viewed in isolation, satisfies the PSS
// `restricted` profile. Mirrors
// TestBuildPodSpec_FirstPartyContainersPassPSSRestricted for the
// HarnessRun-pod path. Without this test, a PSS regression in the
// seed pod (e.g. someone adds a container without
// allowPrivilegeEscalation=false) would not surface until e2e.
//
// First-party containers in the seed pod:
//   - the per-repo seed init containers (alpine/git)
//   - the optional seed proxy sidecar (paddock-proxy)
// Plus the writer-main container.
func TestSeedJobPodSpec_PSSRestricted(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-pss", Namespace: "default"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Storage: paddockv1alpha1.WorkspaceStorageSpec{Size: resource.MustParse("1Gi")},
			Seed: &paddockv1alpha1.WorkspaceSeedSpec{
				Repos: []paddockv1alpha1.WorkspaceSeedRepo{
					{
						URL: "https://github.com/example/repo.git",
						BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
							Name: "ws-pss-broker-creds", Key: "GITHUB_TOKEN",
						},
					},
				},
			},
		},
	}
	in := seedJobInputs{
		proxyImage:     "paddock-proxy:test",
		proxyTLSSecret: "ws-pss-proxy-tls",
		brokerEndpoint: "https://broker.paddock-system.svc:8443",
		brokerCASecret: "ws-pss-broker-ca",
	}

	job := seedJobForWorkspace(ws, "alpine/git:test", in)
	ps := job.Spec.Template.Spec

	evaluator, err := pspolicy.NewEvaluator(pspolicy.DefaultChecks(), nil)
	if err != nil {
		t.Fatalf("pss evaluator: %v", err)
	}
	level := psapi.LevelVersion{Level: psapi.LevelRestricted, Version: psapi.LatestVersion()}
	podMeta := &metav1.ObjectMeta{Name: ws.Name, Namespace: ws.Namespace}

	allContainers := append([]corev1.Container{}, ps.InitContainers...)
	allContainers = append(allContainers, ps.Containers...)

	for _, c := range allContainers {
		isolatedPod := corev1.PodSpec{
			Containers:      []corev1.Container{c},
			SecurityContext: ps.SecurityContext,
		}
		results := evaluator.EvaluatePod(level, podMeta, &isolatedPod)
		for _, r := range results {
			if r.Allowed {
				continue
			}
			// Filter out emulation-version-only differences if the seed
			// pod legitimately requires any restricted-rule exemption
			// (e.g. similar to iptables-init in pod_spec_test.go). At
			// the time of writing, no seed container needs an exemption;
			// if a future change introduces one, list it here as
			// pod_spec_test.go does for iptables-init.
			t.Errorf("container %q PSS restricted violation: %s — %s",
				c.Name, r.ForbiddenReason, r.ForbiddenDetail)
		}
	}

	// Sanity: the container list isn't empty (a silent regression where
	// seedJobForWorkspace returns no containers would otherwise pass
	// vacuously).
	if len(allContainers) == 0 {
		t.Fatalf("seed job has no containers; PSS check would pass vacuously")
	}
	_ = strings.Join // keep strings import if linter complains
}
```

- [ ] **Step 4: Run the new test**

```bash
go test ./internal/controller/ -run TestSeedJobPodSpec_PSSRestricted -count=1 -v
```

Two expected outcomes:
- **PASS:** the seed pod already satisfies PSS restricted (likely
  given Phase 2e's pod-spec hardening). Move on.
- **FAIL:** an actual gap surfaced. Investigate before deciding
  whether to (a) tighten the seed pod's SecurityContext or (b)
  document a permitted exemption with a code comment + the same
  shaped filter as the iptables-init exemption in `pod_spec_test.go`.
  Either way, this is the early-warning channel doing its job.

- [ ] **Step 5: Lint**

```bash
golangci-lint run ./internal/controller/...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/workspace_seed_psstest_test.go
git commit -m "$(cat <<'EOF'
test(controller): add PSS-restricted check for seed pod spec (C-07)

pod_spec_test.go runs the real k8s.io/pod-security-admission policy
evaluator against the run-pod spec. The seed pod
(seedJobForWorkspace) had no equivalent — a PSS regression there
would not surface until e2e. Add the mirror.
EOF
)"
```

---

## Task 9: Promote `fakeBroker` to `testutil.FakeBroker` (C-09)

**Files:**
- Create: `internal/controller/testutil/fake_broker.go`
- Create: `internal/controller/testutil/fake_broker_test.go`
- Modify: `internal/controller/broker_credentials_test.go` —
  rewrite `fakeBroker` references to `testutil.FakeBroker`.
- Possibly modify: any other test that references `fakeBroker`
  (Task 7's specs use it; if Task 7 has already landed, those will
  need updating here).

The `fakeBroker` type in `broker_credentials_test.go` is a useful
in-memory `BrokerIssuer` for any reconciler test. Promoting to
`internal/controller/testutil` unblocks reuse from future packages
(future proxy tests, e2e fixtures).

- [ ] **Step 1: Create the testutil package**

```bash
mkdir -p internal/controller/testutil
```

- [ ] **Step 2: Write `fake_broker.go`**

Create `internal/controller/testutil/fake_broker.go`:

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

// Package testutil holds shared fakes and helpers for tests that
// exercise the controller package. Importable from any test package
// without dragging in the controller package's own test fixtures.
package testutil

import (
	"context"
	"sync"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// BrokerError is the testutil-package mirror of controller.BrokerError.
// Promoted alongside FakeBroker so the fake can return shaped errors
// without forcing test packages to import the controller package just
// for the type. (The controller package can keep its own BrokerError
// for HTTP-error wrapping; this is the test surface.)
type BrokerError struct {
	Status  int
	Code    string
	Message string
}

func (e *BrokerError) Error() string { return e.Code + ": " + e.Message }

// FakeBroker is an in-memory BrokerIssuer for reconciler tests.
// Satisfies the controller.BrokerIssuer interface — the controller
// package's tests construct it as `&FakeBroker{Values: ...}` and
// inject it into HarnessRunReconciler.BrokerClient.
//
// Concurrency: Issue is safe to call from multiple goroutines (the
// only mutable field, Calls, is guarded by mu).
type FakeBroker struct {
	Values map[string]string                  // credential name → value
	Errs   map[string]error                   // credential name → fatal error
	Meta   map[string]brokerapi.IssueResponse // credential name → response metadata override
	mu     sync.Mutex
	Calls  int
}

// Issue implements controller.BrokerIssuer.
func (f *FakeBroker) Issue(_ context.Context, _ string, _ string, credentialName string) (*brokerapi.IssueResponse, error) {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
	if err, ok := f.Errs[credentialName]; ok {
		return nil, err
	}
	v, ok := f.Values[credentialName]
	if !ok {
		return nil, &BrokerError{Status: 404, Code: "CredentialNotFound", Message: credentialName}
	}
	resp := brokerapi.IssueResponse{
		Value:     v,
		LeaseID:   "lease-" + credentialName,
		ExpiresAt: time.Now().Add(1 * time.Hour),
		Provider:  "Static",
	}
	if m, ok := f.Meta[credentialName]; ok {
		if m.Provider != "" {
			resp.Provider = m.Provider
		}
		resp.DeliveryMode = m.DeliveryMode
		resp.Hosts = m.Hosts
		resp.InContainerReason = m.InContainerReason
	}
	return &resp, nil
}
```

- [ ] **Step 3: Write a sanity-check test**

Create `internal/controller/testutil/fake_broker_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
...
*/

package testutil_test

import (
	"context"
	"testing"

	"paddock.dev/paddock/internal/controller"
	"paddock.dev/paddock/internal/controller/testutil"
)

// Compile-time check.
var _ controller.BrokerIssuer = (*testutil.FakeBroker)(nil)

func TestFakeBroker_IssueByName(t *testing.T) {
	fb := &testutil.FakeBroker{Values: map[string]string{"K": "v"}}
	resp, err := fb.Issue(context.Background(), "run", "ns", "K")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if resp.Value != "v" {
		t.Errorf("Value = %q, want v", resp.Value)
	}
	if fb.Calls != 1 {
		t.Errorf("Calls = %d, want 1", fb.Calls)
	}
}

func TestFakeBroker_IssueErrorByName(t *testing.T) {
	fb := &testutil.FakeBroker{
		Errs: map[string]error{"K": &testutil.BrokerError{Status: 503, Code: "BrokerDown", Message: "oops"}},
	}
	if _, err := fb.Issue(context.Background(), "run", "ns", "K"); err == nil {
		t.Errorf("expected error, got nil")
	}
}
```

- [ ] **Step 4: Build to confirm the new package compiles**

```bash
go build ./internal/controller/testutil/
go test ./internal/controller/testutil/ -count=1
```

Expected: PASS.

- [ ] **Step 5: Update existing references in `broker_credentials_test.go`**

Edit `internal/controller/broker_credentials_test.go`:

1. Add `"paddock.dev/paddock/internal/controller/testutil"` to the
   imports.
2. Delete the in-file `fakeBroker` type definition + Issue method
   (lines 36–68).
3. Rewrite every `&fakeBroker{...}` literal to
   `&testutil.FakeBroker{...}` and lowercase field names
   (`values:`, `errs:`, `meta:`) to PascalCase
   (`Values:`, `Errs:`, `Meta:`):

```bash
sed -i.bak \
  -e 's/&fakeBroker{/\&testutil.FakeBroker{/g' \
  -e 's/values:/Values:/g' \
  -e 's/errs:/Errs:/g' \
  -e 's/meta:/Meta:/g' \
  internal/controller/broker_credentials_test.go
rm internal/controller/broker_credentials_test.go.bak
```

(Be careful with the sed: only the literals inside the test file
should change. Spot-check the diff before staging — `git diff
internal/controller/broker_credentials_test.go`.)

- [ ] **Step 6: Update Task 7's reconcileCredentials specs (if applicable)**

If Task 7 has already landed, its `reconcileCredentials` Ginkgo
specs reference `fakeBroker`. Same rewrite pattern as Step 5.

- [ ] **Step 7: Hunt down any other `fakeBroker` references**

```bash
grep -rn "fakeBroker\b" internal/
```

Expected: no remaining references after Steps 5–6.

- [ ] **Step 8: Run controller tests**

```bash
go test ./internal/controller/... -count=1
```

Expected: PASS.

- [ ] **Step 9: Lint**

```bash
golangci-lint run ./...
```

Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add internal/controller/testutil/ \
        internal/controller/broker_credentials_test.go
git commit -m "$(cat <<'EOF'
test(controller): promote fakeBroker to testutil.FakeBroker (C-09)

fakeBroker was useful in any reconciler test that needs an in-memory
BrokerIssuer, but it lived in broker_credentials_test.go (package
controller) and was unimportable. Promote to
internal/controller/testutil.FakeBroker so future proxy tests + e2e
fixtures can reuse the same shape.

Also promotes the in-test BrokerError shim to a public testutil
type so callers don't need to import the controller package just to
return a shaped error from the fake.
EOF
)"
```

---

## Final acceptance gate

After all nine tasks land on the branch:

- [ ] **Run the full controller suite**

```bash
go test ./internal/controller/... -count=1
```

Expected: PASS.

- [ ] **Run module-wide tests**

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Run the e2e suite locally** (per project CLAUDE.md — local
  iteration is faster than CI)

```bash
make test-e2e 2>&1 | tee /tmp/e2e.log
```

Expected: PASS. Both `paddock v0.1-v0.3 pipeline` and `Phase 2a P0
hotfix validation` Describes complete green.

- [ ] **Lint pass**

```bash
golangci-lint run ./...
```

Expected: clean.

- [ ] **`go vet` with the e2e tag**

```bash
go vet -tags=e2e ./...
```

Expected: clean.

- [ ] **Verify each acceptance criterion from the design doc**

Cross-check against `docs/superpowers/specs/2026-04-26-controller-dedup-pass-design.md`
§Acceptance criteria:

| Criterion | Verify with |
|---|---|
| `copyCAToSecret` exists; both ensure functions are wrappers | `grep -n "func copyCAToSecret\|copyCAToSecret(ctx" internal/controller/` |
| `buildEgressNetworkPolicy` exists; both builders are wrappers; seed builder lives in `network_policy.go` | `grep -n "buildEgressNetworkPolicy\|buildSeedNetworkPolicy\|buildRunNetworkPolicy" internal/controller/network_policy.go internal/controller/workspace_seed.go` |
| `setCondition` lives in `conditions.go` | `grep -n "func setCondition" internal/controller/` |
| `resolveInterceptionMode` called once per reconcile; `ensureJob` takes `decision` parameter | `grep -n "resolveInterceptionMode\|ensureJob(" internal/controller/harnessrun_controller.go` |
| `ProxyBrokerConfig` defined and embedded in both reconcilers; `--broker-port` is a real flag | `grep -n "ProxyBrokerConfig\|broker-port" internal/controller/proxybroker_config.go cmd/main.go` |
| `reconcileCredentials` exists; inline credential block replaced | `grep -n "reconcileCredentials" internal/controller/harnessrun_controller.go` |
| `TestSeedJobPodSpec_PSSRestricted` exists and passes | `go test ./internal/controller/ -run TestSeedJobPodSpec_PSSRestricted -v` |
| `suite_test.go` no longer has `time.Sleep(500ms)` | `grep -n "time.Sleep" internal/controller/suite_test.go` |
| `internal/controller/testutil/fake_broker.go` exists with exported `FakeBroker` | `ls internal/controller/testutil/ && grep -n "type FakeBroker" internal/controller/testutil/fake_broker.go` |

- [ ] **Open the PR**

```bash
git push -u origin feature/controller-dedup-pass
gh pr create --title "refactor(controller): controller dedup pass (C-01..C-09)" \
  --body "$(cat <<'EOF'
## Summary

- Lands the nine controller dedup/cleanup items C-01 through C-09
  from the core-systems engineering-quality review (PR #38) as a
  single coherent "controller hygiene pass."
- No CRD or behaviour changes. One new CLI flag (`--broker-port`,
  default 8443 — was previously hard-coded inside
  `buildBrokerEgressRule`).
- Eliminates ~200 LOC of duplication: CA-copy (40 LOC × 2),
  NetworkPolicy builder (75 LOC × 2), reconciler config fields
  (9 fields × 2). Adds the missing seed-pod PSS-restricted test
  (early-warning channel for future PSS regressions).

Closes #50.

## Test plan

- [ ] `go test ./... -count=1` passes
- [ ] `golangci-lint run ./...` clean
- [ ] `make test-e2e` passes on a fresh Kind cluster
- [ ] `--broker-port` flag visible in `--help` output and plumbed
      through to per-pod NetworkPolicy egress rules
- [ ] `TestSeedJobPodSpec_PSSRestricted` runs the real PSS evaluator
      against `seedJobForWorkspace` and passes
EOF
)"
```

---

## Notes for the executor

- **Each task is its own commit.** The sequence above produces 10
  commits (Task 6 splits into 6a + 6b). If any task balloons in
  scope during execution, prefer to split into more commits, not
  fewer — release-please reads the conventional-commit headers,
  and a single oversize commit makes the changelog less useful.
- **Don't bypass the pre-commit hook.** It runs `go vet -tags=e2e
  ./...` and `golangci-lint run`. If a hook fails, fix the
  underlying issue and create a new commit (don't `--amend` — the
  hook fails before the commit lands, so amending modifies the
  *previous* commit and risks losing work).
- **The branch tracks `origin/feature/controller-dedup-pass`** —
  push with `git push` (not `--force-with-lease`) once all tasks
  land. The single-commit rebased branch on origin will be cleanly
  fast-forwarded by the new commits.
- **If Task 1's recheck pattern feels wrong:** the alternative
  (giving `copyCAToSecret` a fourth `ok bool` return value) is
  fine; just keep the call sites consistent. The test in Step 2
  exercises both the source-missing and source-empty paths so
  either signature shape is verifiable.
- **If Task 8's PSS test fails on first run:** that's the
  early-warning channel doing its job. Investigate whether the seed
  pod has a legitimate need for an exemption (and document it in a
  filter block per `pod_spec_test.go`'s iptables-init pattern) or
  whether the seed pod's SecurityContext needs tightening.
