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
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/e2e/framework"
	"paddock.dev/paddock/test/utils"
)

const (
	controlPlaneNamespace = "paddock-system"
	runNamespace          = "paddock-e2e"

	// Multi-repo seeding test. Canonical tiny public repos that have
	// been stable for a decade — shallow clones take <1s. Pinned paths
	// give the assertions something concrete to check.
	multiWorkspace = "multi"
	multiDebugPod  = "multi-debug"
	multiRepoAURL  = "https://github.com/octocat/Hello-World.git"
	multiRepoBURL  = "https://github.com/octocat/Spoon-Knife.git"
	multiRepoAPath = "hello"
	multiRepoBPath = "spoon"

	// v0.3 broker/proxy scenarios (spec 0002 §15.5-7). Each lives in
	// its own namespace so the AuditEvent queries don't cross-contaminate
	// and the scenarios can run independently.
	v3BrokerDownNamespace  = "paddock-v3-broker-down-e2e"
	v3BrokerDownTemplate   = "e2e-broker-down"
	v3BrokerDownRunName    = "broker-down-1"
	v3BrokerDownSecretName = "broker-down-secret"
)

var _ = Describe("paddock v0.1-v0.3 pipeline", Ordered, func() {
	// The suite-level BeforeSuite installs CRDs + deploys the
	// controller-manager; this Describe does not redeploy. Per-Describe
	// state isolation is via the tenant namespaces declared below; the
	// AfterAll drains them while the controller is still alive.

	AfterAll(func() {
		// Leave the ns/run behind when KEEP_E2E_RUN=1 so a contributor
		// can poke at the cluster state post-failure.
		if os.Getenv("KEEP_E2E_RUN") == "1" {
			return
		}
		// Teardown order matters: the controller-manager owns two
		// finalizers (paddock.dev/harnessrun-finalizer and
		// paddock.dev/workspace-finalizer). If `make undeploy` scales
		// the controller to zero before it finishes reconciling the
		// namespace-delete cascade, leftover HarnessRun/Workspace
		// objects keep their finalizers forever → namespace pins in
		// Terminating → CRD delete in `kustomize delete` blocks →
		// undeploy hangs until its RunCmdWithTimeout fires.
		//
		// Fix: drain tenant state first (namespaces must fully
		// disappear while the controller is still alive), THEN
		// undeploy. Force-clearing finalizers is the fallback — it
		// only fires when the controller genuinely failed to
		// converge, and emits a loud warning so a regression in the
		// finalizer loop can't hide behind a green teardown.
		testNamespaces := []string{
			runNamespace,
			v3BrokerDownNamespace,
		}

		// 1. Kick every namespace's reconcile-delete chain in
		//    parallel. --wait=false so we can drive our own wait
		//    loop below and keep the parallelism.
		for _, ns := range testNamespaces {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete", "ns", ns,
				"--wait=false", "--ignore-not-found=true")
		}

		// 2. Wait for each namespace to fully terminate. Budget per
		//    ns covers HarnessRun Job delete + Workspace PVC cascade
		//    with slack. 120s is generous for CI's 2-vCPU runners
		//    (controller reconcile latency + the Workspace finalizer's
		//    15s requeue-on-activeRunRef cadence can easily add up to
		//    45-60s of drain time); still well below Ginkgo's 11-min
		//    suite timeout. Fallback on timeout → force-clear + warn.
		for _, ns := range testNamespaces {
			if framework.WaitForNamespaceGone(context.Background(), ns, 120*time.Second) {
				continue
			}
			fmt.Fprintf(GinkgoWriter,
				"WARNING: namespace %s stuck in Terminating after 120s; "+
					"controller-side finalizer drain likely broken — force-clearing\n", ns)
			framework.ForceClearFinalizers(context.Background(), ns)
			// One more short wait so subsequent steps aren't racing
			// a half-gone namespace; fall through regardless.
			framework.WaitForNamespaceGone(context.Background(), ns, 20*time.Second)
		}

		// 3. Non-finalizer cluster-scoped resources.
		_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete", "clusterharnesstemplate", v3BrokerDownTemplate, "--ignore-not-found=true")

		// Suite-level AfterSuite handles `make undeploy` + `make
		// uninstall` after every Describe finishes — the controller
		// stays alive across Describes so the next one's tenant-state
		// reconciliation just works.
	})

	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	Context("v0.3 broker scaled to zero fails closed", func() {
		It("holds the run in Pending with BrokerReady=False and resumes when the broker is back", func() {
			By("creating the broker-down-scenario namespace + static-credential Secret")
			_, err := utils.Run(exec.Command("kubectl", "create", "ns", v3BrokerDownNamespace))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "-n", v3BrokerDownNamespace,
				"create", "secret", "generic", v3BrokerDownSecretName,
				"--from-literal=DEMO_TOKEN=brokerdown-e2e"))
			Expect(err).NotTo(HaveOccurred())

			By("registering a template that requires a broker-issued credential")
			framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata:
  name: %s
spec:
  harness: echo
  image: %s
  command: ["/usr/local/bin/paddock-echo"]
  eventAdapter:
    image: %s
  requires:
    credentials:
      - name: DEMO_TOKEN
  defaults:
    timeout: 60s
  workspace:
    required: true
    mountPath: /workspace
`, v3BrokerDownTemplate, echoImage, adapterEchoImage))

			By("applying a BrokerPolicy granting DEMO_TOKEN via UserSuppliedSecret (in-container delivery)")
			framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: allow-broker-down
  namespace: %s
spec:
  appliesToTemplates: ["%s"]
  grants:
    credentials:
      - name: DEMO_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: %s
            key: DEMO_TOKEN
          deliveryMode:
            inContainer:
              accepted: true
              reason: "E2E broker-down scenario exercises a raw credential plumbed into the run container."
`, v3BrokerDownNamespace, v3BrokerDownTemplate, v3BrokerDownSecretName))

			By("scaling the broker Deployment to 0 before submitting the run")
			broker := framework.GetBroker(context.Background())
			broker.ScaleTo(context.Background(), 0)
			// Wait until every broker Pod is gone — not just NotReady —
			// so kube-proxy has pulled the Endpoints entries and the
			// reconciler's first Issue RPC can't slip through against a
			// terminating pod.
			Eventually(func(g Gomega) {
				pods, err := utils.Run(exec.Command("kubectl", "-n", controlPlaneNamespace,
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
			framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: %s
    kind: ClusterHarnessTemplate
  prompt: "e2e broker-down"
`, v3BrokerDownRunName, v3BrokerDownNamespace, v3BrokerDownTemplate))

			Eventually(func(g Gomega) {
				var status framework.HarnessRunStatus
				out, err := utils.Run(exec.Command("kubectl", "-n", v3BrokerDownNamespace,
					"get", "harnessrun", v3BrokerDownRunName, "-o", "jsonpath={.status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(BeEmpty())
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
			Eventually(func(g Gomega) {
				phase, err := utils.Run(exec.Command("kubectl", "-n", v3BrokerDownNamespace,
					"get", "harnessrun", v3BrokerDownRunName, "-o", "jsonpath={.status.phase}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(phase)).To(Equal("Succeeded"),
					"run still in phase %q after broker returned", strings.TrimSpace(phase))
			}, 3*time.Minute, 3*time.Second).Should(Succeed())
		})
	})

})
