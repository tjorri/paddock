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

package framework

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

// WaitForNamespaceGone polls `kubectl get ns <ns>` until the API
// server returns NotFound or the budget expires. Returns true on
// disappearance, false on timeout. Each poll call is bounded by a
// 5 s per-poll context so a totally unresponsive apiserver can't
// stall teardown past its own deadline.
//
// Used by the teardown sequence to wait for the controller's
// finalizer drain to finish BEFORE `make undeploy` scales it to zero
// — the alternative (kubectl delete ns --wait with one --timeout per
// call) serialises the work; this keeps namespace deletions running
// in parallel and just watches from the outside.
func WaitForNamespaceGone(ctx context.Context, ns string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := RunCmd(pollCtx, "kubectl", "get", "ns", ns)
		cancel()
		if err != nil && strings.Contains(err.Error(), "not found") {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// ForceClearFinalizers is the last-resort fallback for AfterAll —
// fires only when WaitForNamespaceGone times out, which means the
// controller's reconcile-delete loop failed to converge. Null-patches
// .metadata.finalizers on every HarnessRun and Workspace in ns so the
// namespace terminator can finish. Safe in test teardown only — the
// follow-on `kind delete cluster` reclaims any owned resources the
// finalizer cleanup would have handled (Job, PVC, Secret).
//
// When this runs it is a signal that a controller-side change broke
// finalizer convergence — the calling AfterAll branch emits a loud
// warning so the regression doesn't hide behind a green teardown.
func ForceClearFinalizers(ctx context.Context, ns string) {
	for _, kind := range []string{"harnessruns", "workspaces"} {
		out, err := RunCmd(ctx, "kubectl", "-n", ns, "get", kind,
			"-o", "jsonpath={.items[*].metadata.name}", "--ignore-not-found")
		if err != nil {
			continue
		}
		for _, name := range strings.Fields(strings.TrimSpace(out)) {
			_, _ = RunCmdWithTimeout(10*time.Second, "kubectl", "-n", ns, "patch", kind, name,
				"--type=merge", "-p", `{"metadata":{"finalizers":null}}`)
		}
	}
}

// DrainAllPaddockResources deletes every paddock CR cluster-wide with
// --wait so finalizers run while the controller is still alive.
// Idempotent: per-Describe AfterAlls usually cover their own state;
// this is the safety net that catches any namespace they missed.
//
// Order: HarnessRun first (its finalizer drives Workspace
// activeRunRef clearance), then Workspace, then the rest. Other CRs
// have no inter-finalizer dependencies — order among them is for
// reading-order clarity only.
//
// Per-CR --timeout=60s + outer RunCmdWithTimeout=90s means a single
// stuck finalizer caps drain cost rather than dragging the whole
// AfterSuite past Ginkgo's deadline. ForceClearAllPaddockCRs fires
// per surviving CR with a loud warning so a regression in a finalizer
// loop can't hide behind a green AfterSuite.
func DrainAllPaddockResources(ctx context.Context) {
	type drainTarget struct {
		kind       string // plural.fqdn so kubectl resolves unambiguously
		namespaced bool
	}
	targets := []drainTarget{
		{"harnessruns.paddock.dev", true},
		{"workspaces.paddock.dev", true},
		{"brokerpolicies.paddock.dev", true},
		{"harnesstemplates.paddock.dev", true},
		{"auditevents.paddock.dev", true},
		{"clusterharnesstemplates.paddock.dev", false},
	}

	for _, t := range targets {
		args := []string{"delete", t.kind, "--all", "--ignore-not-found=true",
			"--wait=true", "--timeout=60s"}
		if t.namespaced {
			args = append(args, "-A")
		}
		_, _ = RunCmdWithTimeout(90*time.Second, "kubectl", args...)
	}

	// Belt-and-braces: any namespaced CR still present after the
	// targeted delete means its finalizer didn't converge — emit a
	// loud warning and force-clear so AfterSuite still completes
	// (the cluster is about to be torn down anyway).
	ForceClearAllPaddockCRs(ctx)
}

// ForceClearAllPaddockCRs nulls out finalizers on any
// HarnessRun/Workspace that survived the targeted drain. Mirrors
// the per-namespace ForceClearFinalizers fallback used by
// AfterAll teardown, but cluster-wide and AfterSuite-scoped.
//
// Emits a WARNING on every survivor so a regression in the
// controller's reconcile-delete loop is visible in CI logs even
// when the suite reports green.
func ForceClearAllPaddockCRs(ctx context.Context) {
	for _, kind := range []string{"harnessruns", "workspaces"} {
		out, err := RunCmd(ctx, "kubectl", "get", kind, "-A",
			"-o", "jsonpath={range .items[*]}{.metadata.namespace} {.metadata.name}{\"\\n\"}{end}",
			"--ignore-not-found")
		if err != nil || strings.TrimSpace(out) == "" {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			parts := strings.Fields(line)
			if len(parts) != 2 {
				continue
			}
			ns, name := parts[0], parts[1]
			_, _ = fmt.Fprintf(ginkgo.GinkgoWriter,
				"WARNING: %s %s/%s survived AfterSuite drain — force-clearing finalizers; "+
					"investigate the controller's reconcile-delete loop\n", kind, ns, name)
			_, _ = RunCmdWithTimeout(10*time.Second, "kubectl", "-n", ns, "patch", kind, name,
				"--type=merge", "-p", `{"metadata":{"finalizers":null}}`)
		}
	}
}

// CreateTenantNamespace creates a tenant namespace and registers a
// DeferCleanup hook that drains finalizers, force-clears on timeout,
// and emits a WARNING if the namespace pins in Terminating.
//
// Caller must NOT register its own AfterAll/AfterEach for this
// namespace — DeferCleanup handles teardown in the right order even
// across panics.
//
// Returns the resolved namespace string. Under PR 1 (proc-suffix
// stub), the returned name equals `base`; PR 4 wires up per-process
// suffixing. Both the proc-1 and proc-N return values are
// always-valid kubectl namespace identifiers.
func CreateTenantNamespace(ctx context.Context, base string) string {
	ginkgo.GinkgoHelper()
	ns := base + GinkgoProcessSuffix()
	_, err := RunCmd(ctx, "kubectl", "create", "ns", ns)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "create ns %s", ns)

	ginkgo.DeferCleanup(func(ctx ginkgo.SpecContext) {
		// Best-effort delete; finalizers reconciled while controller
		// is still alive. 90s budget covers HarnessRun Job cleanup +
		// Workspace PVC cascade with slack.
		_, _ = RunCmdWithTimeout(10*time.Second, "kubectl", "delete", "ns", ns,
			"--wait=false", "--ignore-not-found=true")
		if WaitForNamespaceGone(ctx, ns, 120*time.Second) {
			return
		}
		fmt.Fprintf(ginkgo.GinkgoWriter,
			"WARNING: namespace %s stuck in Terminating after 120s; "+
				"controller-side finalizer drain likely broken — force-clearing\n", ns)
		ForceClearFinalizers(ctx, ns)
		WaitForNamespaceGone(ctx, ns, 20*time.Second)
	})

	return ns
}
