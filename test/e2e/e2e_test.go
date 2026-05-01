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
	multiNamespace = "paddock-multi-e2e"
	multiWorkspace = "multi"
	multiDebugPod  = "multi-debug"
	multiRepoAURL  = "https://github.com/octocat/Hello-World.git"
	multiRepoBURL  = "https://github.com/octocat/Spoon-Knife.git"
	multiRepoAPath = "hello"
	multiRepoBPath = "spoon"

	// v0.3 broker/proxy scenarios (spec 0002 §15.5-7). Each lives in
	// its own namespace so the AuditEvent queries don't cross-contaminate
	// and the scenarios can run independently.
	v3EgressNamespace      = "paddock-v3-egress-e2e"
	v3EgressTemplate       = "e2e-egress"
	v3EgressRunName        = "egress-1"
	v3BrokerDownNamespace  = "paddock-v3-broker-down-e2e"
	v3BrokerDownTemplate   = "e2e-broker-down"
	v3BrokerDownRunName    = "broker-down-1"
	v3BrokerDownSecretName = "broker-down-secret"
	v3PolicyDelNamespace   = "paddock-v3-policy-delete-e2e"
	v3PolicyDelPolicyName  = "transient-policy"
	v3PolicyDelRunName     = "policy-delete-1"
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
			runNamespace, multiNamespace,
			v3EgressNamespace, v3BrokerDownNamespace, v3PolicyDelNamespace,
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
		_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete", "clusterharnesstemplate", v3EgressTemplate, "--ignore-not-found=true")
		_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete", "clusterharnesstemplate", v3BrokerDownTemplate, "--ignore-not-found=true")

		// Suite-level AfterSuite handles `make undeploy` + `make
		// uninstall` after every Describe finishes — the controller
		// stays alive across Describes so the next one's tenant-state
		// reconciliation just works.
	})

	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	Context("multi-repo workspace seeding", func() {
		It("clones every repo to its own subdir and writes /workspace/.paddock/repos.json", func() {
			By("creating the multi-repo namespace")
			_, err := utils.Run(exec.Command("kubectl", "create", "ns", multiNamespace))
			Expect(err).NotTo(HaveOccurred())

			By("creating a Workspace with two public repos")
			framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  storage:
    size: 100Mi
  seed:
    repos:
      - url: %s
        path: %s
        depth: 1
      - url: %s
        path: %s
        depth: 1
`, multiWorkspace, multiNamespace, multiRepoAURL, multiRepoAPath, multiRepoBURL, multiRepoBPath))

			By("waiting for the Workspace to reach phase=Active")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "-n", multiNamespace,
					"get", "workspace", multiWorkspace, "-o", "jsonpath={.status.phase}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("Active"),
					"workspace still in phase %q", strings.TrimSpace(out))
			}, 3*time.Minute, 3*time.Second).Should(Succeed())

			By("verifying the seed Job emitted an init container per repo")
			initNames, err := utils.Run(exec.Command("kubectl", "-n", multiNamespace,
				"get", "job", multiWorkspace+"-seed",
				"-o", "jsonpath={.spec.template.spec.initContainers[*].name}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(strings.TrimSpace(initNames))).To(ConsistOf("repo-0", "repo-1"))

			By("launching a debug Pod that mounts the PVC and prints the layout")
			framework.ApplyYAML(fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    fsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: inspect
      image: busybox:1.36
      command:
        - sh
        - -c
        - |
          set -eu
          echo '===MANIFEST==='
          cat /workspace/.paddock/repos.json
          echo '===HELLO==='
          ls /workspace/%s
          echo '===SPOON==='
          ls /workspace/%s
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: ws
          mountPath: /workspace
  volumes:
    - name: ws
      persistentVolumeClaim:
        claimName: ws-%s
`, multiDebugPod, multiNamespace, multiRepoAPath, multiRepoBPath, multiWorkspace))

			By("waiting for the debug pod to Succeed")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "-n", multiNamespace,
					"get", "pod", multiDebugPod, "-o", "jsonpath={.status.phase}"))
				g.Expect(err).NotTo(HaveOccurred())
				phase := strings.TrimSpace(out)
				g.Expect(phase).To(Equal("Succeeded"), "debug pod phase=%q", phase)
			}, 90*time.Second, 2*time.Second).Should(Succeed())

			By("verifying the manifest and directory layout")
			logs, err := utils.Run(exec.Command("kubectl", "-n", multiNamespace,
				"logs", multiDebugPod))
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(ContainSubstring("===MANIFEST==="))
			Expect(logs).To(ContainSubstring(`"url": "` + multiRepoAURL + `"`))
			Expect(logs).To(ContainSubstring(`"url": "` + multiRepoBURL + `"`))
			Expect(logs).To(ContainSubstring(`"path": "` + multiRepoAPath + `"`))
			Expect(logs).To(ContainSubstring(`"path": "` + multiRepoBPath + `"`))
			// Hello-World repo contains README; both clones should
			// leave a real working tree with a .git dir.
			Expect(logs).To(ContainSubstring("===HELLO==="))
			Expect(logs).To(ContainSubstring("README"))
			Expect(logs).To(ContainSubstring("===SPOON==="))
		})

		It("rejects a Workspace with a git:// seed URL at admission (F-46)", func() {
			By("attempting to create a Workspace whose seed repo URL uses git://")
			yaml := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: ws-bad-scheme
  namespace: %s
spec:
  storage:
    size: 100Mi
  seed:
    repos:
      - url: git://github.com/foo/bar.git
        path: foo
        depth: 1
`, multiNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(yaml)
			out, err := cmd.CombinedOutput()
			Expect(err).To(HaveOccurred(), "expected admission to reject git:// URL")
			Expect(string(out)).To(ContainSubstring("https:// or ssh://"),
				"webhook error message should name the allowlist; got: %s", out)
		})
	})

	// v0.3 M12 scenarios (spec 0002 §15.5-§15.7). Each scenario owns
	// its own namespace; the shared BeforeAll (install + deploy) has
	// wired the proxy sidecar, broker, cert-manager Issuer, and per-run
	// MITM CA already, so the scenarios only add the tenant objects
	// (BrokerPolicy, ClusterHarnessTemplate, HarnessRun).
	Context("v0.3 hostile prompt egress-block", func() {
		It("records an egress-block AuditEvent when the agent hits an ungranted destination", func() {
			By("creating the egress-scenario namespace")
			_, err := utils.Run(exec.Command("kubectl", "create", "ns", v3EgressNamespace))
			Expect(err).NotTo(HaveOccurred())

			By("registering the e2e-egress ClusterHarnessTemplate (empty requires)")
			// Empty requires means every namespace admits the template
			// without a matching BrokerPolicy; admission is a fast path,
			// enforcement is at the proxy.
			framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata:
  name: %s
spec:
  harness: e2e-egress
  image: %s
  command: ["/usr/local/bin/paddock-e2e-egress"]
  eventAdapter:
    image: %s
  defaults:
    timeout: 120s
  workspace:
    required: true
    mountPath: /workspace
`, v3EgressTemplate, e2eEgressImage, adapterEchoImage))

			By("submitting a HarnessRun whose probe target is evil.com")
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
  prompt: "e2e egress-block"
  extraEnv:
    - name: E2E_EGRESS_TARGETS
      value: "https://evil.com"
`, v3EgressRunName, v3EgressNamespace, v3EgressTemplate))

			By("waiting for the run to Succeed (probe failure is swallowed by the harness)")
			Eventually(func(g Gomega) {
				phase, err := utils.Run(exec.Command("kubectl", "-n", v3EgressNamespace,
					"get", "harnessrun", v3EgressRunName, "-o", "jsonpath={.status.phase}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(phase)).To(Equal("Succeeded"),
					"run still in phase %q", strings.TrimSpace(phase))
			}, 3*time.Minute, 3*time.Second).Should(Succeed())

			By("confirming an egress-block AuditEvent landed for evil.com:443")
			events := framework.ListAuditEvents(context.Background(), v3EgressNamespace)
			var blocked *framework.AuditEvent
			for i := range events {
				e := events[i]
				if e.Spec.Kind == "egress-block" && e.Spec.Destination != nil &&
					e.Spec.Destination.Host == "evil.com" && e.Spec.Destination.Port == 443 {
					blocked = &events[i]
					break
				}
			}
			Expect(blocked).NotTo(BeNil(),
				"expected AuditEvent with kind=egress-block destination=evil.com:443; got %+v", events)
			Expect(blocked.Spec.Decision).To(Equal("denied"))
			Expect(blocked.Spec.RunRef).NotTo(BeNil())
			Expect(blocked.Spec.RunRef.Name).To(Equal(v3EgressRunName))
		})
	})

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

	Context("v0.3 BrokerPolicy deleted mid-run", func() {
		It("keeps blocking new upstream connections after the policy is deleted", func() {
			By("confirming Scenario B left the broker serving (pre-check)")
			framework.GetBroker(context.Background()).RequireHealthy(context.Background())

			By("creating the policy-delete-scenario namespace")
			_, err := utils.Run(exec.Command("kubectl", "create", "ns", v3PolicyDelNamespace))
			Expect(err).NotTo(HaveOccurred())

			By("registering a BrokerPolicy (empty grants) so admission has something to match and deletion is meaningful")
			// The policy grants nothing — evil.com stays denied both
			// before and after deletion. The test doesn't assert a
			// behaviour change; it asserts enforcement continues after
			// the policy object disappears (spec §8.2: "new connections
			// blocked within ~10s").
			//
			// `grants:` must be present: the CRD schema lists it as a
			// required field on .spec (see
			// config/crd/bases/paddock.dev_brokerpolicies.yaml).
			// Server-side structural validation rejects the object
			// pre-webhook if the key is absent, so supply an empty
			// credentials array to satisfy the schema.
			framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: %s
  namespace: %s
spec:
  appliesToTemplates: ["%s"]
  grants:
    credentials: []
`, v3PolicyDelPolicyName, v3PolicyDelNamespace, v3EgressTemplate))

			By("submitting a HarnessRun that loop-probes evil.com while holding the Pod for 45s")
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
  prompt: "e2e policy-delete"
  extraEnv:
    - name: E2E_EGRESS_LOOP
      value: "https://evil.com"
    - name: E2E_HOLD_SECONDS
      value: "45"
    - name: E2E_LOOP_SECONDS
      value: "3"
`, v3PolicyDelRunName, v3PolicyDelNamespace, v3EgressTemplate))

			By("waiting for at least one egress-block AuditEvent (pre-delete baseline)")
			Eventually(func(g Gomega) {
				events := framework.ListAuditEvents(context.Background(), v3PolicyDelNamespace)
				var count int
				for _, e := range events {
					if e.Spec.Kind == "egress-block" {
						count++
					}
				}
				g.Expect(count).To(BeNumerically(">=", 1),
					"pre-delete: want >=1 egress-block, got %d", count)
			}, 60*time.Second, 3*time.Second).Should(Succeed())

			By("deleting the BrokerPolicy and noting the cutoff time")
			deleteAt := time.Now().UTC()
			_, err = utils.Run(exec.Command("kubectl", "-n", v3PolicyDelNamespace,
				"delete", "brokerpolicy", v3PolicyDelPolicyName))
			Expect(err).NotTo(HaveOccurred())

			By("waiting for a fresh egress-block AuditEvent created AFTER the delete")
			// Spec §8.2 quotes ~10s for cache refresh; bump to 30s to
			// absorb kubelet scheduling + loop cadence + kubectl
			// round-trips without being flaky.
			Eventually(func(g Gomega) {
				events := framework.ListAuditEvents(context.Background(), v3PolicyDelNamespace)
				var freshest time.Time
				for _, e := range events {
					if e.Spec.Kind != "egress-block" {
						continue
					}
					ts, parseErr := time.Parse(time.RFC3339, e.Metadata.CreationTimestamp)
					if parseErr != nil {
						continue
					}
					if ts.After(freshest) {
						freshest = ts
					}
				}
				g.Expect(freshest.After(deleteAt)).To(BeTrue(),
					"freshest egress-block (%s) is not after the policy delete (%s)",
					freshest.Format(time.RFC3339), deleteAt.Format(time.RFC3339))
			}, 30*time.Second, 3*time.Second).Should(Succeed())

			By("waiting for the run to complete on its own")
			Eventually(func(g Gomega) {
				phase, err := utils.Run(exec.Command("kubectl", "-n", v3PolicyDelNamespace,
					"get", "harnessrun", v3PolicyDelRunName, "-o", "jsonpath={.status.phase}"))
				g.Expect(err).NotTo(HaveOccurred())
				p := strings.TrimSpace(phase)
				g.Expect(p).To(BeElementOf("Succeeded", "Failed"),
					"run still in phase %q", p)
			}, 3*time.Minute, 3*time.Second).Should(Succeed())
		})
	})
})
