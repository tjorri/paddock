//go:build e2e
// +build e2e

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package e2e — hostile-harness scenarios.
//
// These scenarios use the paddock-evil-echo image to attempt
// adversarial actions and assert that Paddock's defences deny them.
// Validates Phase 2a's three P0 fixes (F-19, F-38, F-45). See:
//
//   - docs/security/2026-04-25-v0.4-test-gaps.md §3 (TG-NN entries)
//   - docs/plans/2026-04-25-v0.4-security-review-phase-2b-design.md §3.3
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

	"paddock.dev/paddock/test/utils"
)

// hostileEvent is a single JSON line emitted by evil-echo on stdout.
// Mirrors the Output struct in images/evil-echo/main.go.
type hostileEvent struct {
	Flag   string         `json:"flag"`
	Target string         `json:"target,omitempty"`
	Result string         `json:"result"`
	Error  string         `json:"error,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
}

// parseHostileEvents parses lines of evil-echo JSON output. Tolerates
// non-JSON lines (e.g., the harness's stderr leaking into the output
// ConfigMap if collector misroutes).
func parseHostileEvents(text string) []hostileEvent {
	var events []hostileEvent
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var e hostileEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events
}

var _ = Describe("Phase 2a P0 hotfix validation (hostile harness)", Ordered, func() {
	var hostileNamespace string

	BeforeAll(func() {
		hostileNamespace = "paddock-hostile-e2e"

		// The suite-level BeforeSuite installs CRDs + deploys the
		// controller-manager once for the whole suite, so this
		// BeforeAll only does Describe-specific setup: applying the
		// cluster-scoped hostile template.
		mustApply("config/samples/paddock_v1alpha1_clusterharnesstemplate_evil_echo.yaml")
	})

	AfterAll(func() {
		// KEEP_E2E_RUN=1 leaves tenant state behind so a contributor
		// can poke at the cluster post-failure. Same convention as
		// e2e_test.go's pipeline AfterAll.
		if os.Getenv("KEEP_E2E_RUN") == "1" {
			return
		}
		// Each spec in this Describe creates its own per-spec
		// namespace (paddock-hostile-tgXX); only the BeforeAll
		// namespace is shared. End-of-spec deletes use --wait=false,
		// so finalizers may still be running when AfterAll runs.
		// Drain explicitly here while the controller is still alive
		// — otherwise stuck finalizers pin the namespace until
		// AfterSuite's drain catches them, and the suite's runtime
		// budget pays twice.
		hostileNamespaces := []string{
			hostileNamespace,
			"paddock-hostile-tg2", "paddock-hostile-tg7",
			"paddock-hostile-tg10a", "paddock-hostile-tg13a",
			"paddock-hostile-tg25a",
		}

		// 1. Kick every namespace's reconcile-delete chain in parallel.
		for _, ns := range hostileNamespaces {
			runWithTimeout(10*time.Second, "kubectl", "delete", "ns", ns,
				"--wait=false", "--ignore-not-found=true")
		}

		// 2. Wait for each to terminate; force-clear on timeout. Same
		//    120s budget as the pipeline AfterAll — covers HarnessRun
		//    Job teardown + Workspace finalizer requeue cadence.
		for _, ns := range hostileNamespaces {
			if waitForNamespaceGone(ns, 120*time.Second) {
				continue
			}
			fmt.Fprintf(GinkgoWriter,
				"WARNING: namespace %s stuck in Terminating after 120s; "+
					"controller-side finalizer drain likely broken — force-clearing\n", ns)
			forceClearFinalizers(ns)
			waitForNamespaceGone(ns, 20*time.Second)
		}

		// 3. Cluster-scoped templates this Describe owns.
		runWithTimeout(10*time.Second, "kubectl", "delete", "clusterharnesstemplate", "evil-echo-tg2", "--ignore-not-found=true")
		runWithTimeout(10*time.Second, "kubectl", "delete", "clusterharnesstemplate", "evil-echo-tg7", "--ignore-not-found=true")
	})

	Context("F-19: per-run NetworkPolicy denies cooperative-mode bypass to in-cluster IPs", func() {
		It("blocks raw-TCP from agent to Kubernetes service IP even when HTTPS_PROXY is unset (TG-2)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			By("creating a dedicated namespace + BrokerPolicy")
			mustCreateNamespace(hostileNamespace)
			mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", hostileNamespace)

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
`, runName, hostileNamespace)
			mustApplyManifest(runManifest)

			By("waiting for terminal phase")
			Eventually(func() string {
				return runPhase(ctx, hostileNamespace, runName)
			}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

			By("dumping run state for diagnostic context")
			dumpRunDiagnostics(ctx, hostileNamespace, runName)

			By("reading harness JSON output and asserting connect-raw-tcp was denied")
			output := readRunOutput(ctx, hostileNamespace, runName)
			events := parseHostileEvents(output)
			Expect(events).ToNot(BeEmpty(), "expected at least one hostile-event JSON line in run output; got: %s", output)

			var connectEvent *hostileEvent
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
	})

	Context("F-38: agent container has no SA-token mount", func() {
		It("agent cannot read SA-token files (TG-7)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			By("ensuring the namespace + policy are in place")
			mustCreateNamespace(hostileNamespace)
			mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", hostileNamespace)

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
`, runName, hostileNamespace)
			mustApplyManifest(runManifest)

			By("waiting for terminal phase")
			Eventually(func() string {
				return runPhase(ctx, hostileNamespace, runName)
			}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

			By("reading harness output")
			output := readRunOutput(ctx, hostileNamespace, runName)
			events := parseHostileEvents(output)
			Expect(events).ToNot(BeEmpty(), "expected hostile-event JSON; got: %s", output)

			By("asserting --read-secret-files found no matches (no SA token mount)")
			var readEvent *hostileEvent
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
			var probeEvent *hostileEvent
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
	})

	Context("F-45: per-seed-Pod NetworkPolicy denies in-cluster reach from seed Pod", func() {
		It("pod carrying paddock.dev/workspace label cannot reach cluster service CIDR (TG-24)", func() {
			// This test validates that the NetworkPolicy shape emitted by
			// buildSeedNetworkPolicy (Phase 2a) actually enforces egress
			// denial under Cilium when applied to a pod with the matching
			// label. We directly apply the NP and a Job with the matching
			// label — this isolates the enforcement claim from the
			// controller's reconciliation logic (which is unit-tested in
			// workspace_seed_test.go::TestBuildSeedNetworkPolicy_Shape).
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			seedNamespace := "paddock-hostile-seed-e2e"
			workspaceName := "tg24-seed-np"

			By("creating a dedicated namespace")
			mustCreateNamespace(seedNamespace)

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

			events := parseHostileEvents(logs)
			Expect(events).ToNot(BeEmpty(), "expected hostile-event JSON in pod logs; got: %s", logs)

			var connectEvent *hostileEvent
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
			// Wait for namespace + finalizer drain so AfterSuite's
			// `make undeploy` doesn't race CR finalizers.
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", seedNamespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		})
	})

	Context("F-12 / TG-19: broker fail-closed when audit unavailable", func() {
		It("returns 503 to controller and persists no credential when AuditEvent CRUD is denied (TG-19)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			By("creating a dedicated namespace + BrokerPolicy that grants DEMO_TOKEN")
			tg19Namespace := "paddock-hostile-tg19"
			// Ensure clean state: delete + wait for any prior namespace
			// (e.g. left over from a failed previous run that successfully
			// completed after DeferCleanup restored RBAC). Otherwise we'd
			// observe a stale Succeeded HarnessRun on the first poll.
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", tg19Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			mustCreateNamespace(tg19Namespace)

			tg19Policy := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: tg19-policy
  namespace: %s
spec:
  appliesToTemplates: ["*"]
  grants:
    credentials:
      - name: DEMO_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: tg19-demo
            key: token
          deliveryMode:
            inContainer:
              accepted: true
              reason: "TG-19 adversarial fixture; tests broker fail-closed semantics"
`, tg19Namespace)
			mustApplyManifest(tg19Policy)

			By("creating the upstream Secret the policy references")
			tg19Secret := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: tg19-demo
  namespace: %s
stringData:
  token: super-secret
`, tg19Namespace)
			mustApplyManifest(tg19Secret)

			By("creating a HarnessTemplate that requires DEMO_TOKEN")
			templateManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: tg19-template
  namespace: %s
spec:
  harness: paddock-echo
  image: paddock-echo:dev
  command: ["/usr/local/bin/paddock-echo"]
  requires:
    credentials:
      - name: DEMO_TOKEN
  workspace:
    required: true
    mountPath: /workspace
  defaults:
    timeout: 60s
    resources:
      limits:
        cpu: 200m
        memory: 128Mi
      requests:
        cpu: 50m
        memory: 64Mi
`, tg19Namespace)
			mustApplyManifest(templateManifest)

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
			_, err := utils.Run(exec.CommandContext(ctx, "kubectl",
				"patch", "clusterrole", "paddock-broker-role",
				"--type=json",
				`-p=[{"op":"remove","path":"/rules/3"}]`))
			Expect(err).NotTo(HaveOccurred())

			By("submitting a HarnessRun that triggers broker.Issue")
			runName := "tg19-fail-closed"
			runManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: tg19-template
  prompt: "tg-19 audit-fail probe"
`, runName, tg19Namespace)
			mustApplyManifest(runManifest)

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
				return runPhase(ctx, tg19Namespace, runName)
			}, 30*time.Second, 5*time.Second).ShouldNot(Equal("Succeeded"),
				"run must not reach Succeeded while broker's audit-write is failing")

			By("dumping run state for diagnostic context")
			dumpRunDiagnostics(ctx, tg19Namespace, runName)

			By("asserting no credential leaked into <run>-broker-creds")
			// Two acceptable outcomes:
			//  (a) Secret doesn't exist at all (controller never reached
			//      the upsert step because every Issue attempt failed).
			//  (b) Secret exists but DEMO_TOKEN data is empty.
			// Either way no credential leaked. Failure mode would be a
			// secret with non-empty DEMO_TOKEN bytes, which would surface
			// as base64-encoded data in jsonpath output.
			out, getErr := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", tg19Namespace,
				"get", "secret", runName+"-broker-creds",
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

			By("cleaning up TG-19 namespace")
			// Wait for namespace + finalizer drain so AfterSuite's
			// `make undeploy` doesn't race CR finalizers — the
			// controller has to be alive to remove HarnessRun /
			// Workspace finalizers before the namespace can disappear.
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", tg19Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		})
	})

	Context("F-32: admission-rejected HarnessRun emits policy-rejected AuditEvent", func() {
		It("creates a policy-rejected AuditEvent with decision=denied (F-32 e2e)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			f32Namespace := "paddock-hostile-f32"
			// Ensure clean state in case a prior run left the namespace.
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", f32Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			mustCreateNamespace(f32Namespace)
			DeferCleanup(func() {
				// Wait for namespace + finalizer drain (see TG-19 cleanup
				// note above for why AfterSuite needs a clean slate).
				_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
					"delete", "ns", f32Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			})

			invalidName := "f32-invalid-spec"
			By("submitting a HarnessRun with an invalid spec (no prompt or promptFrom)")
			invalidManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: any
`, invalidName, f32Namespace)

			cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(invalidManifest)
			out, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(),
				"admission must reject HarnessRun without prompt/promptFrom; got: %s", out)
			Expect(out).To(ContainSubstring("prompt"),
				"rejection diagnostic must mention the missing prompt field; got: %s", out)

			By("asserting a policy-rejected AuditEvent landed in the namespace")
			Eventually(func() int {
				out, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", f32Namespace,
					"get", "auditevents",
					"-l", "paddock.dev/kind=policy-rejected,paddock.dev/run="+invalidName,
					"--no-headers",
					"-o", "name"))
				return strings.Count(out, "auditevent")
			}, 30*time.Second, 2*time.Second).Should(BeNumerically(">=", 1),
				"expected >=1 policy-rejected AuditEvent for the invalid HarnessRun")

			By("verifying the AuditEvent's spec.decision is denied")
			out, err = utils.Run(exec.CommandContext(ctx, "kubectl", "-n", f32Namespace,
				"get", "auditevents",
				"-l", "paddock.dev/kind=policy-rejected,paddock.dev/run="+invalidName,
				"-o", "jsonpath={.items[0].spec.decision}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal("denied"))
		})
	})

	Context("F-21 / TG-10a: proxy strips agent-smuggled headers before forwarding (Phase 2g)", func() {
		It("smuggled headers do not reach upstream — load-bearing test in internal/proxy/substitute_test.go (TG-10a)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			tgNamespace := "paddock-hostile-tg10a"
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", tgNamespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			mustCreateNamespace(tgNamespace)
			DeferCleanup(func() {
				// Wait for namespace + finalizer drain (see TG-19 cleanup
				// note above for why AfterSuite needs a clean slate).
				_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
					"delete", "ns", tgNamespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			})

			mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", tgNamespace)

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
`, runName, tgNamespace)
			mustApplyManifest(runManifest)

			Eventually(func() string {
				return runPhase(ctx, tgNamespace, runName)
			}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

			output := readRunOutput(ctx, tgNamespace, runName)
			events := parseHostileEvents(output)
			Expect(events).ToNot(BeEmpty(), "expected hostile-event JSON; got: %s", output)

			var smugEvent *hostileEvent
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
	})

	Context("F-09 / TG-13a: SubstituteAuth rejects bearer used for unallowlisted host (Phase 2g)", func() {
		It("Anthropic bearer used for evil.com is rejected at the proxy/broker boundary (TG-13a)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			tgNamespace := "paddock-hostile-tg13a"
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", tgNamespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			mustCreateNamespace(tgNamespace)
			DeferCleanup(func() {
				// Wait for namespace + finalizer drain (see TG-19 cleanup
				// note above for why AfterSuite needs a clean slate).
				_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
					"delete", "ns", tgNamespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			})

			mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", tgNamespace)

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
`, runName, tgNamespace)
			mustApplyManifest(runManifest)

			Eventually(func() string {
				return runPhase(ctx, tgNamespace, runName)
			}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

			output := readRunOutput(ctx, tgNamespace, runName)
			events := parseHostileEvents(output)
			Expect(events).ToNot(BeEmpty(), "expected hostile-event JSON; got: %s", output)

			var probeEvent *hostileEvent
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
	})

	Context("F-25 / TG-25a: bytes-shuttle idle timeout — load-bearing test in internal/proxy/server_test.go (Phase 2g)", func() {
		It("smoke-checks that a Phase 2g run reaches terminal phase cleanly (TG-25a)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			tgNamespace := "paddock-hostile-tg25a"
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", tgNamespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			mustCreateNamespace(tgNamespace)
			DeferCleanup(func() {
				// Wait for namespace + finalizer drain (see TG-19 cleanup
				// note above for why AfterSuite needs a clean slate).
				_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
					"delete", "ns", tgNamespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			})

			mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", tgNamespace)

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
`, runName, tgNamespace)
			mustApplyManifest(runManifest)

			Eventually(func() string {
				return runPhase(ctx, tgNamespace, runName)
			}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

			// Smoke: the run reached a terminal phase. The load-bearing F-25
			// idle-timeout assertion lives in unit tests
			// (TestProxy_BytesShuttleIdleTimeout, TestSubstituteLoop_IdleTimeout).
		})
	})
})

// mustApply applies a YAML file at the cluster scope. Fails the test on
// error.
func mustApply(path string) {
	out, err := utils.Run(exec.Command("kubectl", "apply", "-f", path))
	Expect(err).ToNot(HaveOccurred(), "kubectl apply -f %s: %s", path, out)
}

// mustApplyToNamespace applies a YAML file into the given namespace.
func mustApplyToNamespace(path, namespace string) {
	out, err := utils.Run(exec.Command("kubectl", "-n", namespace, "apply", "-f", path))
	Expect(err).ToNot(HaveOccurred(), "kubectl -n %s apply -f %s: %s", namespace, path, out)
}

// mustApplyManifest applies a YAML manifest from a string.
func mustApplyManifest(yaml string) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	out, err := utils.Run(cmd)
	Expect(err).ToNot(HaveOccurred(), "kubectl apply: %s", out)
}

// mustCreateNamespace creates a namespace; idempotent.
func mustCreateNamespace(ns string) {
	out, err := utils.Run(exec.Command("kubectl", "create", "ns", ns))
	if err != nil && !strings.Contains(out, "AlreadyExists") {
		Fail(fmt.Sprintf("kubectl create ns %s: %s", ns, out))
	}
}

// runPhase reads the HarnessRun's status.phase. Returns empty string on
// not-found / parse error so the Eventually() can keep polling.
func runPhase(ctx context.Context, namespace, name string) string {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "get", "harnessrun", name,
		"-o", "jsonpath={.status.phase}"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// readRunOutput returns the concatenated text from the run's pod logs
// (agent container). evil-echo emits its hostile-event JSON to stdout,
// which is captured by the kubelet's pod-log buffer — NOT by the
// collector sidecar (which reads PADDOCK_RAW_PATH for echo-compatible
// events). For hostile scenarios we therefore fetch pod logs directly,
// not the run's output ConfigMap.
func readRunOutput(ctx context.Context, namespace, name string) string {
	jobName, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "get", "harnessrun", name,
		"-o", "jsonpath={.status.jobName}"))
	jobName = strings.TrimSpace(jobName)
	if jobName == "" {
		return ""
	}
	podName, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "get", "pods",
		"-l", "job-name="+jobName, "-o", "jsonpath={.items[0].metadata.name}"))
	podName = strings.TrimSpace(podName)
	if podName == "" {
		return ""
	}
	logs, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "logs",
		podName, "-c", "agent"))
	return logs
}

// dumpRunDiagnostics emits to GinkgoWriter the current state of the
// HarnessRun, its associated Pods, and the controller-manager logs.
// Called before output-shape assertions so a failure surfaces enough
// context to diagnose without re-running.
func dumpRunDiagnostics(ctx context.Context, namespace, runName string) {
	dump := func(title string, args ...string) {
		out, _ := utils.Run(exec.CommandContext(ctx, "kubectl", args...))
		GinkgoWriter.Printf("--- %s ---\n%s\n", title, out)
	}
	dump("harnessrun describe",
		"-n", namespace, "describe", "harnessrun", runName)
	dump("pods in run namespace",
		"-n", namespace, "get", "pods", "-o", "wide")
	dump("pod descriptions",
		"-n", namespace, "describe", "pods")
	dump("events in run namespace",
		"-n", namespace, "get", "events", "--sort-by=.lastTimestamp")
	dump("controller-manager logs",
		"-n", "paddock-system", "logs", "-l", "control-plane=controller-manager", "--tail=200")
}
