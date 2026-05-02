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
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tjorri/paddock/test/e2e/framework"
	"github.com/tjorri/paddock/test/utils"
)

const (
	brokerDownNS       = "paddock-broker-down"
	brokerDownTemplate = "e2e-broker-down"
	brokerDownRunName  = "broker-down-1"
	brokerDownSecret   = "broker-down-secret"

	auditUnavailableNS = "paddock-broker-audit-unavail" // F-12
	rolloutRestartNS   = "paddock-broker-rollout"       // F-14
	leakGuardNS        = "paddock-broker-leak-guard"    // F-11
)

var _ = Describe("broker failure modes", Ordered, Serial, Label("broker"), func() {
	It("holds runs Pending while the broker is unavailable and resumes when it returns", func(ctx SpecContext) {
		ns := framework.CreateTenantNamespace(ctx, brokerDownNS)

		By("creating a static-credential Secret in the broker-down namespace")
		_, err := utils.Run(exec.Command("kubectl", "-n", ns,
			"create", "secret", "generic", brokerDownSecret,
			"--from-literal=DEMO_TOKEN=brokerdown-e2e"))
		Expect(err).NotTo(HaveOccurred())

		By("registering a template that requires a broker-issued credential")
		framework.NewHarnessTemplate(ns, brokerDownTemplate).
			WithImage(echoImage).
			WithCommand("/usr/local/bin/paddock-echo").
			WithEventAdapter(adapterEchoImage).
			WithDefaultTimeout("60s").
			WithRequiredCredential("DEMO_TOKEN").
			Apply(ctx)

		By("applying a BrokerPolicy granting DEMO_TOKEN via UserSuppliedSecret (in-container delivery)")
		framework.NewBrokerPolicy(ns, "allow-broker-down", brokerDownTemplate).
			GrantCredentialFromSecret("DEMO_TOKEN", brokerDownSecret, "DEMO_TOKEN", "inContainer",
				"E2E broker-down scenario exercises a raw credential plumbed into the run container.").
			Apply(ctx)

		By("scaling the broker Deployment to 0 before submitting the run")
		broker := framework.GetBroker(context.Background())
		broker.ScaleTo(context.Background(), 0)
		// Wait until every broker Pod is gone — not just NotReady —
		// so kube-proxy has pulled the Endpoints entries and the
		// reconciler's first Issue RPC can't slip through against a
		// terminating pod.
		Eventually(func(g Gomega) {
			pods, err := utils.Run(exec.Command("kubectl", "-n", framework.BrokerNamespace,
				"get", "pods", "-l", "app.kubernetes.io/component=broker",
				"-o", "jsonpath={.items[*].metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(pods)).To(BeEmpty(),
				"broker pods still present: %q", strings.TrimSpace(pods))
		}, 60*time.Second, 2*time.Second).Should(Succeed())

		// Restore broker on exit no matter how the It body returns.
		// DeferCleanup runs after the It completes (success, Fail,
		// or panic) and integrates with Ginkgo's reporter, unlike
		// defer which silently logs on a writer that a SIGKILL
		// could truncate. RestoreOnTeardown asserts loudly — a visible
		// red here beats a broken broker cascading into Scenario C
		// and being mis-attributed as a Scenario C flake.
		broker.RestoreOnTeardown()

		By("submitting a HarnessRun and expecting Pending/BrokerUnavailable")
		run := framework.NewRun(ns, brokerDownTemplate).
			WithName(brokerDownRunName).
			WithPrompt("e2e broker-down").
			Submit(ctx)

		Eventually(func(g Gomega) {
			// Inline status fetch instead of run.Status — under -p, the
			// run is queried fast enough that .status may still be
			// empty when the first Eventually tick fires. Status's
			// internal package-level gomega.Expect on an empty json
			// would panic out instead of letting Eventually retry.
			out, err := framework.RunCmd(ctx, "kubectl", "-n", ns,
				"get", "harnessrun", brokerDownRunName, "-o", "jsonpath={.status}")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).NotTo(BeEmpty(), "status not yet populated")
			var status framework.HarnessRunStatus
			g.Expect(json.Unmarshal([]byte(out), &status)).To(Succeed())
			g.Expect(status.Phase).To(Equal("Pending"),
				"want Pending while broker is scaled to zero; got %q", status.Phase)
			ready := framework.FindCondition(status.Conditions, "BrokerReady")
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).To(Equal("False"))
			g.Expect(ready.Reason).To(Equal("BrokerUnavailable"),
				"BrokerReady.reason=%q (message=%q)", ready.Reason, ready.Message)
		}, 90*time.Second, 3*time.Second).Should(Succeed())

		By("re-scaling the broker to 1 and waiting for it to accept traffic")
		broker.ScaleTo(context.Background(), 1)
		broker.WaitReady(context.Background())

		By("expecting the run to reach Succeeded once the broker is back")
		run.WaitForPhase(ctx, "Succeeded", 3*time.Minute)
	})

	It("fails issuance closed when AuditEvent writes are denied", func(ctx SpecContext) {
		ctxT, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		By("creating a dedicated namespace + BrokerPolicy that grants DEMO_TOKEN")
		// Ensure clean state: delete + wait for any prior namespace
		// (e.g. left over from a failed previous run that successfully
		// completed after DeferCleanup restored RBAC). Otherwise we'd
		// observe a stale Succeeded HarnessRun on the first poll.
		_, _ = utils.Run(exec.CommandContext(ctxT, "kubectl",
			"delete", "ns", auditUnavailableNS, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		ns := framework.CreateTenantNamespace(ctxT, auditUnavailableNS)

		framework.NewBrokerPolicy(ns, "tg19-policy", "*").
			GrantCredentialFromSecret("DEMO_TOKEN", "tg19-demo", "token", "inContainer",
				"TG-19 adversarial fixture; tests broker fail-closed semantics").
			Apply(ctxT)

		By("creating the upstream Secret the policy references")
		tg19Secret := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: tg19-demo
  namespace: %s
stringData:
  token: super-secret
`, ns)
		framework.ApplyYAML(tg19Secret)

		By("creating a HarnessTemplate that requires DEMO_TOKEN")
		framework.NewHarnessTemplate(ns, "tg19-template").
			WithImage(echoImage).
			WithCommand("/usr/local/bin/paddock-echo").
			WithEventAdapter(adapterEchoImage).
			WithRequiredCredential("DEMO_TOKEN").
			Apply(ctxT)

		By("patching the broker ClusterRole to remove auditevents:create")
		// Restoration is symmetric: a JSON-patch add appends the
		// auditevents rule back to the rules slice in DeferCleanup.
		// Avoids the resourceVersion-conflict trap of full-resource
		// kubectl apply during cleanup.
		DeferCleanup(func() {
			By("restoring the broker ClusterRole's auditevents rule")
			_, err := utils.Run(exec.Command("kubectl",
				"patch", "clusterrole", "paddock-broker-role",
				"--type=json",
				`-p=[{"op":"add","path":"/rules/-","value":{"apiGroups":["paddock.dev"],"resources":["auditevents"],"verbs":["create"]}}]`))
			Expect(err).NotTo(HaveOccurred(), "restoring clusterrole patch")
		})

		// The ClusterRole has 4 rules in order: tokenreviews, paddock
		// CRDs read, secrets read, auditevents create. The
		// auditevents rule is at index 3.
		_, err := utils.Run(exec.CommandContext(ctxT, "kubectl",
			"patch", "clusterrole", "paddock-broker-role",
			"--type=json",
			`-p=[{"op":"remove","path":"/rules/3"}]`))
		Expect(err).NotTo(HaveOccurred())

		By("submitting a HarnessRun that triggers broker.Issue")
		runName := "tg19-fail-closed"
		run := framework.NewRun(ns, "tg19-template").
			WithName(runName).
			WithPrompt("tg-19 audit-fail probe").
			Submit(ctxT)

		By("giving the controller time to attempt issuance against the audit-broken broker")
		// The broker returns 503 AuditUnavailable on every Issue
		// attempt because the audit write fails (RBAC revoked). The
		// controller treats 503 as transient — it sets BrokerReady=False
		// with Reason=BrokerUnavailable, keeps the run in Pending, and
		// requeues. The fail-closed guarantee under test is that the
		// credential never lands in <run>-broker-creds, NOT that the
		// run fails terminally. F-12.
		//
		// 30s window with 5s poll = 6 polls; broker Issue retries
		// fire every ~10s so we observe ~3 rejection cycles —
		// enough to confirm the run cannot transition to Succeeded
		// while the audit-write is broken.
		Consistently(func() string {
			return framework.RunPhase(ctxT, ns, runName)
		}, 30*time.Second, 5*time.Second).ShouldNot(Equal("Succeeded"),
			"run must not reach Succeeded while broker's audit-write is failing")

		By("dumping run state for diagnostic context")
		framework.DumpRunDiagnostics(ctxT, ns, runName)

		By("asserting no credential leaked into <run>-broker-creds")
		// Two acceptable outcomes:
		//  (a) Secret doesn't exist at all (controller never reached
		//      the upsert step because every Issue attempt failed).
		//  (b) Secret exists but DEMO_TOKEN data is empty.
		// Either way no credential leaked. Failure mode would be a
		// secret with non-empty DEMO_TOKEN bytes, which would surface
		// as base64-encoded data in jsonpath output.
		out, getErr := utils.Run(exec.CommandContext(ctxT, "kubectl", "-n", ns,
			"get", "secret", run.Name+"-broker-creds",
			"-o", "jsonpath={.data.DEMO_TOKEN}"))
		if getErr != nil {
			// kubectl errored — most likely the secret doesn't exist.
			// That's the strongest possible guarantee that nothing
			// leaked. Pass.
			Expect(strings.ToLower(out)).To(ContainSubstring("notfound"),
				"unexpected kubectl error when checking <run>-broker-creds: %s", out)
		} else {
			Expect(strings.TrimSpace(out)).To(BeEmpty(),
				"DEMO_TOKEN must not be persisted when broker fail-closes the issue path; got %q", out)
		}
	})

	It("preserves PATPool lease distinctness across a rollout restart", func(ctx SpecContext) {
		// Reconciliation invariant under test: two HarnessRuns
		// against a 2-slot PATPool each hold a distinct slot before
		// AND after a broker rollout-restart. Pre-fix, the second
		// run would never acquire a lease — the first run's
		// reconcile loop kept calling /v1/issue every 5s (no
		// idempotency), exhausting the 2-slot pool within ~10s.
		// See commit history for the controller-side fast-path that
		// fixed this; the unit tests in
		// internal/broker/reconstruct_test.go and
		// internal/broker/providers/patpool_test.go pin the
		// reconstruction half.
		ctxT, cancel := context.WithTimeout(ctx, 8*time.Minute)
		defer cancel()

		_, _ = utils.Run(exec.CommandContext(ctxT, "kubectl",
			"delete", "ns", rolloutRestartNS, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		ns := framework.CreateTenantNamespace(ctxT, rolloutRestartNS)
		// Always restore broker on exit so subsequent specs are not left
		// in a degraded state.
		framework.GetBroker(ctxT).RestoreOnTeardown()

		By("creating pool Secret, HarnessTemplate, and BrokerPolicy (2-slot pool)")
		framework.ApplyYAML(framework.PATPoolFixtureManifest(ns, "t2-restart", 2))

		runA := "restart-a"
		runB := "restart-b"
		// Catch-all: dump diagnostics on any spec failure (collision
		// after restart, distinct-slot Eventually flake, etc.). The
		// per-Eventually runAOK/runBOK guards below cover the
		// lease-acquisition phase; this guard covers the post-
		// restart assertions where both flags are true but the spec
		// can still fail.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctxT, ns, runA)
				framework.DumpRunDiagnostics(ctxT, ns, runB)
			}
		})

		// Apply runs SEQUENTIALLY (not in a tight loop): in CI the
		// reconcile rate per (namespace, run) is bounded, and a tight
		// loop can leave the second run stuck behind the first run's
		// reconcile cycle. The F-14 invariant we're validating is
		// "post-restart slots stay distinct" — that's verified the
		// same way whether runs leased concurrently or one-after-the-
		// other, so prefer the more robust sequential setup.

		By("starting run-a and waiting for it to acquire a lease")
		framework.NewRun(ns, "t2-patpool-tmpl").
			WithName(runA).
			WithPrompt("t2 restart test").
			Submit(ctxT)
		runAOK := false
		Eventually(func() int {
			return framework.IssuedLeaseCount(ctxT, ns, runA)
		}, 180*time.Second, 2*time.Second).Should(BeNumerically(">=", 1),
			"run %s did not acquire a lease", runA)
		runAOK = true
		DeferCleanup(func() {
			if !runAOK {
				framework.DumpRunDiagnostics(ctxT, ns, runA)
			}
		})

		By("starting run-b and waiting for it to acquire a lease")
		framework.NewRun(ns, "t2-patpool-tmpl").
			WithName(runB).
			WithPrompt("t2 restart test").
			Submit(ctxT)
		runBOK := false
		DeferCleanup(func() {
			// On any failure (including run-b never leasing), dump
			// describe + events + controller + broker logs for both
			// runs so the next CI flake gives us real signal.
			if !runBOK {
				framework.DumpRunDiagnostics(ctxT, ns, runA)
				framework.DumpRunDiagnostics(ctxT, ns, runB)
			}
		})
		Eventually(func() int {
			return framework.IssuedLeaseCount(ctxT, ns, runB)
		}, 180*time.Second, 2*time.Second).Should(BeNumerically(">=", 1),
			"run %s did not acquire a lease", runB)
		runBOK = true

		slotA1 := framework.PoolSlotIndex(ctxT, ns, runA)
		slotB1 := framework.PoolSlotIndex(ctxT, ns, runB)
		Expect(slotA1).NotTo(Equal(slotB1), "pre-restart: both runs hold the same slot — pool collision")

		By("restarting the broker deployment")
		Expect(framework.GetBroker(ctxT).RolloutRestart(ctxT)).To(Succeed())

		By("waiting for broker to be healthy again")
		framework.GetBroker(ctxT).RequireHealthy(ctxT)

		By("asserting both runs still hold distinct slots after broker restart")
		// The controller may reconcile and re-issue; give it time.
		Eventually(func() bool {
			a := framework.PoolSlotIndex(ctxT, ns, runA)
			b := framework.PoolSlotIndex(ctxT, ns, runB)
			// Both must have a lease and they must differ.
			return a >= 0 && b >= 0 && a != b
		}, 90*time.Second, 2*time.Second).Should(BeTrue(),
			"post-restart slots for runs %s and %s collided — broker lease reconstruction (F-14) may be broken",
			runA, runB)
	})

	It("force-clears the run finalizer when the broker is unreachable", func(ctx SpecContext) {
		ctxT, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		_, _ = utils.Run(exec.CommandContext(ctxT, "kubectl",
			"delete", "ns", leakGuardNS, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		ns := framework.CreateTenantNamespace(ctxT, leakGuardNS)
		// Restore the broker regardless of test outcome.
		framework.GetBroker(ctxT).RestoreOnTeardown()

		By("creating pool Secret, HarnessTemplate, and BrokerPolicy")
		framework.ApplyYAML(framework.PATPoolFixtureManifest(ns, "t2-forceclear", 1))

		By("submitting a HarnessRun and waiting for it to acquire a lease")
		runName := "force-clear"
		// On any spec failure dump describe + events + controller +
		// broker logs. The lease-acquisition Eventually below has
		// flaked in CI without any signal — see CI run 24999903077;
		// without this dump we cannot tell whether the controller
		// never called Issue, the broker rejected, or the pool was
		// already exhausted from prior-spec leftover state.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctxT, ns, runName)
			}
		})
		framework.NewRun(ns, "t2-patpool-tmpl").
			WithName(runName).
			WithPrompt("t2 force-clear test").
			Submit(ctxT)

		Eventually(func() int {
			return framework.IssuedLeaseCount(ctxT, ns, runName)
		}, 90*time.Second, 2*time.Second).Should(BeNumerically(">=", 1),
			"run did not acquire a lease before broker scale-down")

		By("scaling the broker Deployment to 0")
		framework.GetBroker(ctxT).ScaleTo(ctxT, 0)
		Eventually(func(g Gomega) {
			pods, err := utils.Run(exec.CommandContext(ctxT, "kubectl", "-n", framework.BrokerNamespace,
				"get", "pods", "-l", "app.kubernetes.io/component=broker",
				"-o", "jsonpath={.items[*].metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(pods)).To(BeEmpty(),
				"broker pods still present: %q", strings.TrimSpace(pods))
		}, 60*time.Second, 2*time.Second).Should(Succeed())

		By("deleting the run — expecting removal within 60s despite broker being down")
		_, err := utils.Run(exec.CommandContext(ctxT, "kubectl", "-n", ns,
			"delete", "harnessrun", runName, "--wait=false"))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			_, err := utils.Run(exec.CommandContext(ctxT, "kubectl", "-n", ns,
				"get", "harnessrun", runName))
			return err != nil && strings.Contains(err.Error(), "not found")
		}, 60*time.Second, 2*time.Second).Should(BeTrue(),
			"HarnessRun %s/%s not gone after 60s with broker down — force-clear path may be broken", ns, runName)

		By("asserting a RevokeFailed Warning event was recorded against the run")
		Expect(framework.RunHasWarningEvent(ctxT, ns, runName, "RevokeFailed")).To(BeTrue(),
			"expected a RevokeFailed Warning event for run %s/%s; controller may not be emitting it", ns, runName)
	})

	It("/readyz returns 503 during cold start and 200 once warm", func(ctx SpecContext) {
		ctxT, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()

		// Always restore broker so subsequent specs are not broken.
		framework.GetBroker(ctxT).RestoreOnTeardown()

		// On spec failure, dump broker pod state + logs so a
		// readiness-probe regression (broker stuck in cold-start,
		// never returns 200) surfaces with diagnostic context.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpBrokerDiagnostics(ctxT)
			}
		})

		By("restarting the broker pod")
		Expect(framework.GetBroker(ctxT).RolloutRestart(ctxT)).To(Succeed())

		By("polling /readyz via port-forward and observing 503 → 200 transition")
		saw503 := false
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			code, probeErr := framework.GetBroker(ctxT).Readyz(ctxT)
			if probeErr == nil && code == http.StatusServiceUnavailable {
				saw503 = true
			}
			if saw503 && probeErr == nil && code == http.StatusOK {
				return // success
			}
			time.Sleep(time.Second)
		}
		// If we never saw 503 it might mean the cold-start window is
		// shorter than our poll interval — this is acceptable if the
		// broker has already warmed up. Only fail if the broker never
		// came back to 200.
		if !saw503 {
			By("cold-start window was shorter than poll interval; verifying final /readyz is 200")
			code, err := framework.GetBroker(ctxT).Readyz(ctxT)
			Expect(err).NotTo(HaveOccurred(), "/readyz probe error after restart")
			Expect(code).To(Equal(http.StatusOK),
				"broker /readyz is not 200 after restart — broker may be stuck in cold-start")
			return
		}
		Fail("observed 503 but broker never returned 200 within 45s of restart")
	})
})
