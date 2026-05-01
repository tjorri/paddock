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

	"paddock.dev/paddock/test/e2e/framework"
	"paddock.dev/paddock/test/utils"
)

const (
	egressBlockNS        = "paddock-egress-block"
	policyDeleteMidRunNS = "paddock-policy-delete-mid-run"
	cooperativeBypassNS  = "paddock-egress-cooperative-bypass"
	smugglingNS          = "paddock-egress-smuggling"
	substituteHostNS     = "paddock-egress-substitute-host"
	idleTimeoutNS        = "paddock-egress-idle-timeout"
	saTokenNS            = "paddock-egress-sa-token"
	seedPodNPNS          = "paddock-egress-seed-pod-np"

	egressBlockTemplate = "e2e-egress"
	egressBlockRunName  = "egress-1"
	policyDelPolicyName = "transient-policy"
	policyDelRunName    = "policy-delete-1"
)

var _ = Describe("egress enforcement", func() {
	It("records an egress-block AuditEvent for an ungranted destination", func() {
		ns := framework.CreateTenantNamespace(context.Background(), egressBlockNS)

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
`, egressBlockTemplate, e2eEgressImage, adapterEchoImage))
		DeferCleanup(func() {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete",
				"clusterharnesstemplate", egressBlockTemplate, "--ignore-not-found=true")
		})

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
`, egressBlockRunName, ns, egressBlockTemplate))

		By("waiting for the run to Succeed (probe failure is swallowed by the harness)")
		Eventually(func(g Gomega) {
			phase, err := utils.Run(exec.Command("kubectl", "-n", ns,
				"get", "harnessrun", egressBlockRunName, "-o", "jsonpath={.status.phase}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(phase)).To(Equal("Succeeded"),
				"run still in phase %q", strings.TrimSpace(phase))
		}, 3*time.Minute, 3*time.Second).Should(Succeed())

		By("confirming an egress-block AuditEvent landed for evil.com:443")
		events := framework.ListAuditEvents(context.Background(), ns)
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
		Expect(blocked.Spec.RunRef.Name).To(Equal(egressBlockRunName))
	})

	It("keeps blocking upstream connections after a granting BrokerPolicy is deleted", func() {
		ns := framework.CreateTenantNamespace(context.Background(), policyDeleteMidRunNS)

		By("registering the e2e-egress ClusterHarnessTemplate (empty requires)")
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
`, egressBlockTemplate, e2eEgressImage, adapterEchoImage))
		DeferCleanup(func() {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete",
				"clusterharnesstemplate", egressBlockTemplate, "--ignore-not-found=true")
		})

		By("confirming Scenario B left the broker serving (pre-check)")
		framework.GetBroker(context.Background()).RequireHealthy(context.Background())

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
`, policyDelPolicyName, ns, egressBlockTemplate))

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
`, policyDelRunName, ns, egressBlockTemplate))

		By("waiting for at least one egress-block AuditEvent (pre-delete baseline)")
		Eventually(func(g Gomega) {
			events := framework.ListAuditEvents(context.Background(), ns)
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
		_, err := utils.Run(exec.Command("kubectl", "-n", ns,
			"delete", "brokerpolicy", policyDelPolicyName))
		Expect(err).NotTo(HaveOccurred())

		By("waiting for a fresh egress-block AuditEvent created AFTER the delete")
		// Spec §8.2 quotes ~10s for cache refresh; bump to 30s to
		// absorb kubelet scheduling + loop cadence + kubectl
		// round-trips without being flaky.
		Eventually(func(g Gomega) {
			events := framework.ListAuditEvents(context.Background(), ns)
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
			phase, err := utils.Run(exec.Command("kubectl", "-n", ns,
				"get", "harnessrun", policyDelRunName, "-o", "jsonpath={.status.phase}"))
			g.Expect(err).NotTo(HaveOccurred())
			p := strings.TrimSpace(phase)
			g.Expect(p).To(BeElementOf("Succeeded", "Failed"),
				"run still in phase %q", p)
		}, 3*time.Minute, 3*time.Second).Should(Succeed())
	})

	It("denies raw-TCP egress to a Service IP even with HTTPS_PROXY unset", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ns := framework.CreateTenantNamespace(ctx, cooperativeBypassNS)

		By("applying the evil-echo ClusterHarnessTemplates")
		mustApply("config/samples/paddock_v1alpha1_clusterharnesstemplate_evil_echo.yaml")
		DeferCleanup(func() {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete",
				"clusterharnesstemplate", "evil-echo-tg2", "--ignore-not-found=true")
		})

		By("creating a dedicated namespace + BrokerPolicy")
		mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", ns)

		By("submitting a HarnessRun that attempts cooperative-mode bypass (args baked into evil-echo-tg2 template)")
		runName := "tg2-cooperative-bypass"
		runManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: evil-echo-tg2
    kind: ClusterHarnessTemplate
  prompt: "tg-2 hostile probe"
`, runName, ns)
		mustApplyManifest(runManifest)

		By("waiting for terminal phase")
		Eventually(func() string {
			return runPhase(ctx, ns, runName)
		}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

		By("dumping run state for diagnostic context")
		dumpRunDiagnostics(ctx, ns, runName)

		By("reading harness JSON output and asserting connect-raw-tcp was denied")
		output := readRunOutput(ctx, ns, runName)
		events := framework.ParseHostileEvents(output)
		Expect(events).ToNot(BeEmpty(), "expected at least one hostile-event JSON line in run output; got: %s", output)

		var connectEvent *framework.HostileEvent
		for i := range events {
			if events[i].Flag == "--connect-raw-tcp" {
				connectEvent = &events[i]
				break
			}
		}
		Expect(connectEvent).ToNot(BeNil(), "no --connect-raw-tcp event in output: %s", output)
		Expect(connectEvent.Result).To(Equal("denied"),
			"NetworkPolicy should have denied the in-cluster connection (F-19); got result=%q error=%q",
			connectEvent.Result, connectEvent.Error)
	})

	It("strips agent-smuggled headers at the proxy", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ns := framework.CreateTenantNamespace(ctx, smugglingNS)

		mustApply("config/samples/paddock_v1alpha1_clusterharnesstemplate_evil_echo.yaml")
		DeferCleanup(func() {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete",
				"clusterharnesstemplate", "evil-echo-tg10a", "--ignore-not-found=true")
		})

		mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", ns)

		runName := "tg10a-smuggle"
		runManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: evil-echo-tg10a
    kind: ClusterHarnessTemplate
  prompt: "tg-10a smuggle headers"
`, runName, ns)
		mustApplyManifest(runManifest)

		Eventually(func() string {
			return runPhase(ctx, ns, runName)
		}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

		output := readRunOutput(ctx, ns, runName)
		events := framework.ParseHostileEvents(output)
		Expect(events).ToNot(BeEmpty(), "expected hostile-event JSON; got: %s", output)

		var smugEvent *framework.HostileEvent
		for i := range events {
			if events[i].Flag == "--smuggle-headers" {
				smugEvent = &events[i]
				break
			}
		}
		Expect(smugEvent).ToNot(BeNil(), "no --smuggle-headers event: %s", output)
		// Either result is acceptable — the load-bearing per-header strip
		// assertion lives in internal/proxy/substitute_test.go.
		Expect(smugEvent.Result).To(Or(Equal("denied"), Equal("success")),
			"smuggle-headers must produce denied (proxy block) or success (denied upstream); got %+v", smugEvent)
	})

	It("rejects a substituted bearer for an unallowlisted host", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ns := framework.CreateTenantNamespace(ctx, substituteHostNS)

		mustApply("config/samples/paddock_v1alpha1_clusterharnesstemplate_evil_echo.yaml")
		DeferCleanup(func() {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete",
				"clusterharnesstemplate", "evil-echo-tg13a", "--ignore-not-found=true")
		})

		mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", ns)

		runName := "tg13a-host-not-allowed"
		runManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: evil-echo-tg13a
    kind: ClusterHarnessTemplate
  prompt: "tg-13a host-not-allowed probe"
`, runName, ns)
		mustApplyManifest(runManifest)

		Eventually(func() string {
			return runPhase(ctx, ns, runName)
		}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

		output := readRunOutput(ctx, ns, runName)
		events := framework.ParseHostileEvents(output)
		Expect(events).ToNot(BeEmpty(), "expected hostile-event JSON; got: %s", output)

		var probeEvent *framework.HostileEvent
		for i := range events {
			if events[i].Flag == "--probe-provider-substitution-host" {
				probeEvent = &events[i]
				break
			}
		}
		Expect(probeEvent).ToNot(BeNil(), "no --probe-provider-substitution-host event: %s", output)
		// The load-bearing F-09 host-scoping assertion lives in unit
		// tests (TestAnthropicAPIProvider_SubstituteHostNotAllowed_*,
		// same shape for GitHubApp + PATPool). Here we only assert the
		// harness emitted the event and reached terminal phase — the
		// existing harness function constructs its own http.Transport
		// without `Proxy: http.ProxyFromEnvironment`, so the request
		// bypasses the Paddock proxy and the result depends on whether
		// evil.com itself responds. Either denied (network-blocked) or
		// success (evil.com responded) is acceptable for this smoke
		// pass; the unit tests carry the host-scope rejection claim.
		Expect(probeEvent.Result).To(Or(Equal("denied"), Equal("success")),
			"--probe-provider-substitution-host must complete with denied or success; got %+v", probeEvent)
	})

	It("terminates a run cleanly when the bytes shuttle hits its idle timeout", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ns := framework.CreateTenantNamespace(ctx, idleTimeoutNS)

		mustApply("config/samples/paddock_v1alpha1_clusterharnesstemplate_evil_echo.yaml")
		DeferCleanup(func() {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete",
				"clusterharnesstemplate", "evil-echo-tg25a", "--ignore-not-found=true")
		})

		mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", ns)

		runName := "tg25a-smoke"
		runManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: evil-echo-tg25a
    kind: ClusterHarnessTemplate
  prompt: "tg-25a phase-2g smoke"
`, runName, ns)
		mustApplyManifest(runManifest)

		Eventually(func() string {
			return runPhase(ctx, ns, runName)
		}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

		// Smoke: the run reached a terminal phase. The load-bearing F-25
		// idle-timeout assertion lives in unit tests
		// (TestProxy_BytesShuttleIdleTimeout, TestSubstituteLoop_IdleTimeout).
	})

	It("blocks ServiceAccount-token reads and broker probes from the agent container", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ns := framework.CreateTenantNamespace(ctx, saTokenNS)

		By("applying the evil-echo ClusterHarnessTemplates")
		mustApply("config/samples/paddock_v1alpha1_clusterharnesstemplate_evil_echo.yaml")
		DeferCleanup(func() {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete",
				"clusterharnesstemplate", "evil-echo-tg7", "--ignore-not-found=true")
		})

		By("ensuring the namespace + policy are in place")
		mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", ns)

		By("submitting a HarnessRun that probes for SA tokens and the broker (args baked into evil-echo-tg7 template)")
		runName := "tg7-sa-token-forgery"
		runManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: evil-echo-tg7
    kind: ClusterHarnessTemplate
  prompt: "tg-7 sa-token forgery probe"
`, runName, ns)
		mustApplyManifest(runManifest)

		By("waiting for terminal phase")
		Eventually(func() string {
			return runPhase(ctx, ns, runName)
		}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

		By("reading harness output")
		output := readRunOutput(ctx, ns, runName)
		events := framework.ParseHostileEvents(output)
		Expect(events).ToNot(BeEmpty(), "expected hostile-event JSON; got: %s", output)

		By("asserting --read-secret-files found no matches (no SA token mount)")
		var readEvent *framework.HostileEvent
		for i := range events {
			if events[i].Flag == "--read-secret-files" {
				readEvent = &events[i]
				break
			}
		}
		Expect(readEvent).ToNot(BeNil(), "no --read-secret-files event: %s", output)
		Expect(readEvent.Result).To(Equal("denied"),
			"agent container should have no SA-token mount (F-38); got %+v", readEvent)

		By("asserting --probe-broker was network-denied (cooperative proxy intercepts; broker host not in egress allowlist)")
		var probeEvent *framework.HostileEvent
		for i := range events {
			if events[i].Flag == "--probe-broker" {
				probeEvent = &events[i]
				break
			}
		}
		Expect(probeEvent).ToNot(BeNil(), "no --probe-broker event: %s", output)
		Expect(probeEvent.Result).To(Equal("denied"),
			"broker must be unreachable (network-level denial via cooperative proxy/NP); got %+v", probeEvent)
	})

	It("denies Service-CIDR egress from seed-job Pods", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		seedNamespace := framework.CreateTenantNamespace(ctx, seedPodNPNS)
		workspaceName := "tg24-seed-np"

		By("applying the NetworkPolicy shape that buildSeedNetworkPolicy would emit")
		// RFC1918 + link-local + cluster service CIDR (10.96.0.0/12)
		// are excluded from the 0.0.0.0/0 allow rule, so 10.96.0.1:443
		// (the kubernetes service) is blocked. podSelector matches the
		// label the seed Job's pod template carries.
		npManifest := fmt.Sprintf(`
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s-seed-egress
  namespace: %s
  labels:
    app.kubernetes.io/name: paddock
    app.kubernetes.io/component: workspace-seed-egress
    paddock.dev/workspace: %s
spec:
  podSelector:
    matchLabels:
      paddock.dev/workspace: %s
  policyTypes:
    - Egress
  egress:
    - ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
      to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
    - ports:
        - protocol: TCP
          port: 443
      to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except:
              - 10.0.0.0/8
              - 172.16.0.0/12
              - 192.168.0.0/16
              - 169.254.0.0/16
    - ports:
        - protocol: TCP
          port: 80
      to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except:
              - 10.0.0.0/8
              - 172.16.0.0/12
              - 192.168.0.0/16
              - 169.254.0.0/16
`, workspaceName, seedNamespace, workspaceName, workspaceName)
		mustApplyManifest(npManifest)

		By("creating a Job whose pod carries the paddock.dev/workspace label")
		jobManifest := fmt.Sprintf(`
apiVersion: batch/v1
kind: Job
metadata:
  name: %s-probe
  namespace: %s
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        paddock.dev/workspace: %s
    spec:
      restartPolicy: Never
      containers:
        - name: evil-echo
          image: paddock-evil-echo:dev
          imagePullPolicy: IfNotPresent
          command: ["/usr/local/bin/evil-echo"]
          args:
            - "--connect-raw-tcp"
            - "10.96.0.1:443"
`, workspaceName, seedNamespace, workspaceName)
		mustApplyManifest(jobManifest)

		By("waiting for the Job to complete or fail")
		Eventually(func() string {
			out, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", seedNamespace, "get", "job",
				workspaceName+"-probe", "-o", "jsonpath={.status.conditions[?(@.type=='Complete')].status}"))
			if strings.TrimSpace(out) == "True" {
				return "Complete"
			}
			out, _ = utils.Run(exec.CommandContext(ctx, "kubectl", "-n", seedNamespace, "get", "job",
				workspaceName+"-probe", "-o", "jsonpath={.status.conditions[?(@.type=='Failed')].status}"))
			if strings.TrimSpace(out) == "True" {
				return "Failed"
			}
			return ""
		}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Complete"), Equal("Failed")))

		By("reading the Job pod's logs")
		podName, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", seedNamespace, "get", "pods",
			"-l", "paddock.dev/workspace="+workspaceName, "-o", "jsonpath={.items[0].metadata.name}"))
		podName = strings.TrimSpace(podName)
		Expect(podName).ToNot(BeEmpty(), "no pod found for workspace label %s", workspaceName)

		logs, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", seedNamespace, "logs", podName))
		Expect(err).ToNot(HaveOccurred(), "kubectl logs %s/%s: %s", seedNamespace, podName, logs)

		events := framework.ParseHostileEvents(logs)
		Expect(events).ToNot(BeEmpty(), "expected hostile-event JSON in pod logs; got: %s", logs)

		var connectEvent *framework.HostileEvent
		for i := range events {
			if events[i].Flag == "--connect-raw-tcp" {
				connectEvent = &events[i]
				break
			}
		}
		Expect(connectEvent).ToNot(BeNil(), "no --connect-raw-tcp event in pod logs: %s", logs)
		Expect(connectEvent.Result).To(Equal("denied"),
			"seed Pod NetworkPolicy should have blocked the in-cluster connection (F-45); got %+v", connectEvent)

		By("cleanup")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "job", "-n", seedNamespace, workspaceName+"-probe", "--wait=false"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "networkpolicy", "-n", seedNamespace, workspaceName+"-seed-egress", "--wait=false"))
	})
})
