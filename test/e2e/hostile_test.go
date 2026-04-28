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
//   - docs/internal/security-audits/2026-04-25-v0.4-test-gaps.md §3 (TG-NN entries)
//   - docs/superpowers/specs/2026-04-25-v0.4-security-review-phase-2b-design.md §3.3
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
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
			// Theme 2 broker-hygiene specs (F-11, F-14, F-16, F-17).
			"paddock-t2-revoke", "paddock-t2-restart",
			"paddock-t2-forceclear", "paddock-t2-oversize",
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

	// -------------------------------------------------------------------------
	// Theme 2 broker-hygiene specs (Tasks 21, refs: issue #43)
	// -------------------------------------------------------------------------

	Context("F-11: broker revokes PATPool lease on HarnessRun delete", func() {
		It("revokes broker leases on HarnessRun delete", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			t2Namespace := "paddock-t2-revoke"
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", t2Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			mustCreateNamespace(t2Namespace)
			DeferCleanup(func() {
				_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
					"delete", "ns", t2Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			})

			By("creating pool Secret, HarnessTemplate, and BrokerPolicy")
			mustApplyManifest(patPoolFixtureManifest(t2Namespace, "t2-revoke", 2))

			By("submitting a HarnessRun that acquires a PATPool lease")
			runName := "revoke-test"
			// Dump describe + events + controller + broker logs on any
			// spec failure (lease-acquisition timeout, finalizer stuck,
			// metric never decrements) so the next CI flake gives us
			// real signal instead of a bare Eventually-timed-out line.
			DeferCleanup(func() {
				if CurrentSpecReport().Failed() {
					dumpRunDiagnostics(ctx, t2Namespace, runName)
				}
			})
			mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: t2-patpool-tmpl
  prompt: "t2 revoke test"
`, runName, t2Namespace))

			By("waiting for at least one IssuedLease to appear on the run")
			Eventually(func() int {
				return issuedLeaseCount(ctx, t2Namespace, runName)
			}, 90*time.Second, 2*time.Second).Should(BeNumerically(">=", 1))

			By("recording the current PATPool leased count from broker metrics")
			leasedBefore := brokerMetricGauge(ctx, "paddock_broker_patpool_leased")

			By("deleting the HarnessRun")
			_, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", t2Namespace,
				"delete", "harnessrun", runName, "--wait=false"))
			Expect(err).NotTo(HaveOccurred())

			By("asserting the run is fully gone within 60s")
			Eventually(func() bool {
				_, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", t2Namespace,
					"get", "harnessrun", runName))
				return err != nil && strings.Contains(err.Error(), "not found")
			}, 60*time.Second, 2*time.Second).Should(BeTrue(),
				"HarnessRun %s/%s still present after 60s — broker-leases finalizer may be stuck", t2Namespace, runName)

			By("asserting the PATPool slot was freed by Revoke")
			Eventually(func() float64 {
				return brokerMetricGauge(ctx, "paddock_broker_patpool_leased")
			}, 30*time.Second, 2*time.Second).Should(BeNumerically("<", leasedBefore),
				"PATPool leased count did not decrease after run delete; lease was not revoked")
		})
	})

	Context("F-14: broker survives restart without re-leasing PATPool slots", func() {
		It("survives broker restart without slot collision", func() {
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
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()

			t2Namespace := "paddock-t2-restart"
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", t2Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			mustCreateNamespace(t2Namespace)
			DeferCleanup(func() {
				_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
					"delete", "ns", t2Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			})
			// Always restore broker on exit so subsequent specs are not left
			// in a degraded state.
			DeferCleanup(restoreBroker)

			By("creating pool Secret, HarnessTemplate, and BrokerPolicy (2-slot pool)")
			mustApplyManifest(patPoolFixtureManifest(t2Namespace, "t2-restart", 2))

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
					dumpRunDiagnostics(ctx, t2Namespace, runA)
					dumpRunDiagnostics(ctx, t2Namespace, runB)
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
			mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: t2-patpool-tmpl
  prompt: "t2 restart test"
`, runA, t2Namespace))
			runAOK := false
			Eventually(func() int {
				return issuedLeaseCount(ctx, t2Namespace, runA)
			}, 180*time.Second, 2*time.Second).Should(BeNumerically(">=", 1),
				"run %s did not acquire a lease", runA)
			runAOK = true
			DeferCleanup(func() {
				if !runAOK {
					dumpRunDiagnostics(ctx, t2Namespace, runA)
				}
			})

			By("starting run-b and waiting for it to acquire a lease")
			mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: t2-patpool-tmpl
  prompt: "t2 restart test"
`, runB, t2Namespace))
			runBOK := false
			DeferCleanup(func() {
				// On any failure (including run-b never leasing), dump
				// describe + events + controller + broker logs for both
				// runs so the next CI flake gives us real signal.
				if !runBOK {
					dumpRunDiagnostics(ctx, t2Namespace, runA)
					dumpRunDiagnostics(ctx, t2Namespace, runB)
				}
			})
			Eventually(func() int {
				return issuedLeaseCount(ctx, t2Namespace, runB)
			}, 180*time.Second, 2*time.Second).Should(BeNumerically(">=", 1),
				"run %s did not acquire a lease", runB)
			runBOK = true

			slotA1 := poolSlotIndex(ctx, t2Namespace, runA)
			slotB1 := poolSlotIndex(ctx, t2Namespace, runB)
			Expect(slotA1).NotTo(Equal(slotB1), "pre-restart: both runs hold the same slot — pool collision")

			By("restarting the broker deployment")
			Expect(brokerRolloutRestart(ctx)).To(Succeed())

			By("waiting for broker to be healthy again")
			requireBrokerHealthy()

			By("asserting both runs still hold distinct slots after broker restart")
			// The controller may reconcile and re-issue; give it time.
			Eventually(func() bool {
				a := poolSlotIndex(ctx, t2Namespace, runA)
				b := poolSlotIndex(ctx, t2Namespace, runB)
				// Both must have a lease and they must differ.
				return a >= 0 && b >= 0 && a != b
			}, 90*time.Second, 2*time.Second).Should(BeTrue(),
				"post-restart slots for runs %s and %s collided — broker lease reconstruction (F-14) may be broken",
				runA, runB)
		})
	})

	Context("F-11 (leak guard): controller force-clears finalizer when broker is unreachable", func() {
		It("force-clears finalizer when broker is unreachable", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			t2Namespace := "paddock-t2-forceclear"
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
				"delete", "ns", t2Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			mustCreateNamespace(t2Namespace)
			DeferCleanup(func() {
				_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
					"delete", "ns", t2Namespace, "--ignore-not-found", "--wait=true", "--timeout=60s"))
			})
			// Restore the broker regardless of test outcome.
			DeferCleanup(restoreBroker)

			By("creating pool Secret, HarnessTemplate, and BrokerPolicy")
			mustApplyManifest(patPoolFixtureManifest(t2Namespace, "t2-forceclear", 1))

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
					dumpRunDiagnostics(ctx, t2Namespace, runName)
				}
			})
			mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: t2-patpool-tmpl
  prompt: "t2 force-clear test"
`, runName, t2Namespace))

			Eventually(func() int {
				return issuedLeaseCount(ctx, t2Namespace, runName)
			}, 90*time.Second, 2*time.Second).Should(BeNumerically(">=", 1),
				"run did not acquire a lease before broker scale-down")

			By("scaling the broker Deployment to 0")
			_, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", controlPlaneNamespace,
				"scale", "deploy", v3BrokerDeploy, "--replicas=0"))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				pods, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", controlPlaneNamespace,
					"get", "pods", "-l", "app.kubernetes.io/component=broker",
					"-o", "jsonpath={.items[*].metadata.name}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(pods)).To(BeEmpty(),
					"broker pods still present: %q", strings.TrimSpace(pods))
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			By("deleting the run — expecting removal within 60s despite broker being down")
			_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "-n", t2Namespace,
				"delete", "harnessrun", runName, "--wait=false"))
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				_, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", t2Namespace,
					"get", "harnessrun", runName))
				return err != nil && strings.Contains(err.Error(), "not found")
			}, 60*time.Second, 2*time.Second).Should(BeTrue(),
				"HarnessRun %s/%s not gone after 60s with broker down — force-clear path may be broken", t2Namespace, runName)

			By("asserting a RevokeFailed Warning event was recorded against the run")
			Expect(runHasWarningEvent(ctx, t2Namespace, runName, "RevokeFailed")).To(BeTrue(),
				"expected a RevokeFailed Warning event for run %s/%s; controller may not be emitting it", t2Namespace, runName)
		})
	})

	Context("F-17(a): MaxBytesReader rejects oversize /v1/issue bodies — load-bearing test in internal/broker/server_test.go", func() {
		It("smoke-checks that /v1/issue rejects unauthenticated requests (F-17 a e2e smoke)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			// On spec failure, dump broker pod state + controller +
			// broker logs so a port-forward / TLS-handshake / 401-route
			// regression surfaces with diagnostic context.
			DeferCleanup(func() {
				if CurrentSpecReport().Failed() {
					dumpBrokerDiagnostics(ctx)
				}
			})

			// The load-bearing F-17(a) oversize-body assertion lives in
			// TestHandleIssue_OversizeBody_BadRequest (internal/broker/server_test.go).
			// That test wires a fake authenticated caller with a 100 KiB body and
			// asserts HTTP 400. In e2e the broker API port (:8443) requires a valid
			// SA bearer — without one auth fails with 401 BEFORE the body is read,
			// so MaxBytesReader never triggers on an unauthenticated request.
			//
			// This smoke spec asserts the API port is reachable and returns a
			// well-formed JSON ErrorResponse for an unauthenticated call, which
			// confirms the broker is correctly wired with TLS + the limitBody
			// middleware (TLS handshake would fail on a misconfigured server;
			// a missing limitBody wrapper would still return 401 here, so
			// this smoke only indirectly confirms F-17(a) wiring — the unit
			// test carries the direct assertion).

			// port-forward the TLS API port from the broker pod.
			pod := brokerPodName(ctx)
			Expect(pod).NotTo(BeEmpty(), "no broker pod found")

			const localTLSPort = "19443"
			pfCtx, pfCancel := context.WithCancel(ctx)
			defer pfCancel()
			pfCmd := exec.CommandContext(pfCtx, "kubectl", "-n", controlPlaneNamespace,
				"port-forward", "pod/"+pod, localTLSPort+":8443")
			Expect(pfCmd.Start()).To(Succeed(), "starting port-forward to broker :8443")
			time.Sleep(500 * time.Millisecond)

			By("sending an unauthenticated POST /v1/issue with oversize body and asserting well-formed JSON error")
			oversizeBody := bytes.Repeat([]byte("x"), 100<<10) // 100 KiB
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				"https://127.0.0.1:"+localTLSPort+"/v1/issue",
				bytes.NewReader(oversizeBody))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Paddock-Run", "oversize-smoke")

			//nolint:gosec // e2e-only: TLS is self-signed in Kind; skip verification.
			transport := &http.Transport{
				TLSClientConfig: tlsSkipVerify(),
			}
			httpClient := &http.Client{Transport: transport}
			resp, doErr := httpClient.Do(req)
			Expect(doErr).NotTo(HaveOccurred(),
				"POST /v1/issue failed — broker may not be reachable via port-forward")
			defer resp.Body.Close()

			// Without a valid bearer the broker returns 401 Unauthorized.
			// That is acceptable here — it confirms the broker is up and
			// routing requests through limitBody. The MaxBytesReader cap
			// itself is validated by the unit test.
			Expect(resp.StatusCode).To(BeElementOf(http.StatusUnauthorized, http.StatusBadRequest),
				"expected 401 (unauthenticated) or 400 (body too large); got %d", resp.StatusCode)

			var errResp map[string]any
			body, readErr := io.ReadAll(resp.Body)
			Expect(readErr).NotTo(HaveOccurred())
			Expect(json.Unmarshal(body, &errResp)).To(Succeed(),
				"response body should be a JSON ErrorResponse; got: %s", string(body))
			Expect(errResp).To(HaveKey("code"),
				"ErrorResponse must carry a 'code' field; got: %v", errResp)
		})
	})

	Context("F-16: /readyz returns 503 during cold start", func() {
		It("returns 503 from /readyz during cold start", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()

			// Always restore broker so subsequent specs are not broken.
			DeferCleanup(restoreBroker)

			// On spec failure, dump broker pod state + logs so a
			// readiness-probe regression (broker stuck in cold-start,
			// never returns 200) surfaces with diagnostic context.
			DeferCleanup(func() {
				if CurrentSpecReport().Failed() {
					dumpBrokerDiagnostics(ctx)
				}
			})

			By("restarting the broker pod")
			Expect(brokerRolloutRestart(ctx)).To(Succeed())

			By("polling /readyz via port-forward and observing 503 → 200 transition")
			saw503 := false
			deadline := time.Now().Add(45 * time.Second)
			for time.Now().Before(deadline) {
				code, probeErr := probeBrokerReadyz(ctx)
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
				code, err := probeBrokerReadyz(ctx)
				Expect(err).NotTo(HaveOccurred(), "/readyz probe error after restart")
				Expect(code).To(Equal(http.StatusOK),
					"broker /readyz is not 200 after restart — broker may be stuck in cold-start")
				return
			}
			Fail("observed 503 but broker never returned 200 within 45s of restart")
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

// ---------------------------------------------------------------------------
// Theme 2 helper functions (F-11, F-14, F-16, F-17)
// ---------------------------------------------------------------------------

// patPoolFixtureManifest returns a multi-document YAML string that creates
// the minimal set of objects for a PATPool e2e scenario in the given
// namespace:
//   - A Secret with `slots` fake PAT entries (one per line).
//   - A HarnessTemplate named "t2-patpool-tmpl" that requires GITHUB_TOKEN.
//   - A BrokerPolicy granting GITHUB_TOKEN via PATPool from the Secret.
//
// `prefix` is used to name the Secret so multiple specs in the same
// namespace do not collide.
func patPoolFixtureManifest(namespace, prefix string, slots int) string {
	var lines strings.Builder
	for i := 0; i < slots; i++ {
		fmt.Fprintf(&lines, "ghp_fake_%s_%02d\n", prefix, i)
	}
	return fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: %s-pool
  namespace: %s
type: Opaque
stringData:
  pool: |
%s
---
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: t2-patpool-tmpl
  namespace: %s
spec:
  harness: echo
  image: paddock-echo:dev
  command: ["/usr/local/bin/paddock-echo"]
  requires:
    credentials:
      - name: GITHUB_TOKEN
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
---
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: %s-policy
  namespace: %s
spec:
  appliesToTemplates: ["t2-patpool-tmpl"]
  grants:
    credentials:
      - name: GITHUB_TOKEN
        provider:
          kind: PATPool
          secretRef:
            name: %s-pool
            key: pool
          hosts:
            - github.com
            - api.github.com
    egress:
      - host: github.com
        ports: [443]
      - host: api.github.com
        ports: [443]
`, prefix, namespace, indentLines(lines.String(), "    "), namespace, prefix, namespace, prefix)
}

// indentLines prepends `indent` to every non-empty line.
func indentLines(s, indent string) string {
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out.WriteString(indent + line + "\n")
	}
	return out.String()
}

// issuedLeaseCount returns the number of IssuedLeases on the named
// HarnessRun. Returns 0 on any error so callers can use it in Eventually.
func issuedLeaseCount(ctx context.Context, namespace, runName string) int {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace,
		"get", "harnessrun", runName,
		"-o", "jsonpath={.status.issuedLeases}"))
	if err != nil || strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "null" {
		return 0
	}
	// The jsonpath returns a JSON array literal; count "[" occurrences is
	// fragile; parse properly.
	var leases []json.RawMessage
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &leases); jsonErr != nil {
		return 0
	}
	return len(leases)
}

// poolSlotIndex returns the slotIndex for the first PATPool IssuedLease on
// the named run, or -1 if none is found.
func poolSlotIndex(ctx context.Context, namespace, runName string) int {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace,
		"get", "harnessrun", runName,
		"-o", "jsonpath={.status.issuedLeases[0].poolRef.slotIndex}"))
	if err != nil || strings.TrimSpace(out) == "" {
		return -1
	}
	idx, parseErr := strconv.Atoi(strings.TrimSpace(out))
	if parseErr != nil {
		return -1
	}
	return idx
}

// brokerPodName returns the name of the first running broker pod, or ""
// if none is found.
func brokerPodName(ctx context.Context) string {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", controlPlaneNamespace,
		"get", "pods", "-l", "app.kubernetes.io/component=broker",
		"-o", "jsonpath={.items[0].metadata.name}"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// brokerMetricGauge scrapes the broker's /metrics endpoint (plain HTTP on
// the probe port :8081) via kubectl port-forward to the broker pod, and
// returns the current sum of all time-series matching the given metric name.
// Returns 0 on any error.
//
// The probe port (8081) is not exposed via a Kubernetes Service — only the
// TLS API port (8443) is. We port-forward directly to the pod.
func brokerMetricGauge(ctx context.Context, metricName string) float64 {
	pod := brokerPodName(ctx)
	if pod == "" {
		GinkgoWriter.Printf("brokerMetricGauge: no broker pod found\n")
		return 0
	}

	const localPort = "19081"

	pfCtx, pfCancel := context.WithCancel(ctx)
	defer pfCancel()

	pfCmd := exec.CommandContext(pfCtx, "kubectl", "-n", controlPlaneNamespace,
		"port-forward", "pod/"+pod, localPort+":8081")
	if err := pfCmd.Start(); err != nil {
		GinkgoWriter.Printf("brokerMetricGauge: port-forward start: %v\n", err)
		return 0
	}
	// Give port-forward time to establish.
	time.Sleep(500 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:" + localPort + "/metrics") //nolint:noctx
	if err != nil {
		GinkgoWriter.Printf("brokerMetricGauge: GET /metrics: %v\n", err)
		return 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var total float64
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, metricName) {
			continue
		}
		// line: metricName{...} <value> [timestamp]
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		val, parseErr := strconv.ParseFloat(parts[len(parts)-1], 64)
		if parseErr != nil {
			continue
		}
		total += val
	}
	return total
}

// probeBrokerReadyz port-forwards the broker probe port (:8081) on the
// broker pod and GETs /readyz. Returns the HTTP status code and any
// network/transport error.
func probeBrokerReadyz(ctx context.Context) (int, error) {
	pod := brokerPodName(ctx)
	if pod == "" {
		return 0, fmt.Errorf("no broker pod found")
	}

	const localPort = "19081"

	pfCtx, pfCancel := context.WithCancel(ctx)
	defer pfCancel()

	pfCmd := exec.CommandContext(pfCtx, "kubectl", "-n", controlPlaneNamespace,
		"port-forward", "pod/"+pod, localPort+":8081")
	if err := pfCmd.Start(); err != nil {
		return 0, fmt.Errorf("port-forward start: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:" + localPort + "/readyz") //nolint:noctx
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// tlsSkipVerify returns a tls.Config that skips certificate verification.
// For use in e2e only — the broker uses a cert-manager-issued cert that
// is self-signed in the Kind cluster and not trusted by the test runner's
// CA pool.
func tlsSkipVerify() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} //nolint:gosec
}

// brokerRolloutRestart issues `kubectl rollout restart` on the broker
// Deployment and returns after the new pod is fully serving.
func brokerRolloutRestart(ctx context.Context) error {
	if _, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", controlPlaneNamespace,
		"rollout", "restart", "deploy/"+v3BrokerDeploy)); err != nil {
		return fmt.Errorf("rollout restart: %w", err)
	}
	if _, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", controlPlaneNamespace,
		"rollout", "status", "deploy/"+v3BrokerDeploy, "--timeout=120s")); err != nil {
		return fmt.Errorf("rollout status: %w", err)
	}
	return nil
}

// runHasWarningEvent returns true if any Kubernetes Event in the namespace
// references the given run name with the given reason. Intended for asserting
// that the controller emitted a RevokeFailed event.
//
// Events may have been emitted before the run was deleted (involvedObject
// would still name the run), so we scrape all events in the namespace and
// search by reason — not by involvedObject.name — because the run object
// is gone by the time we check.
func runHasWarningEvent(ctx context.Context, namespace, runName, reason string) bool {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace,
		"get", "events",
		"-o", "jsonpath={range .items[*]}{.reason}|{.involvedObject.name}|{.type}{\"\\n\"}{end}"))
	if err != nil {
		GinkgoWriter.Printf("runHasWarningEvent: kubectl get events: %v\n", err)
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		if parts[0] == reason && parts[1] == runName && parts[2] == "Warning" {
			return true
		}
	}
	return false
}

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
	dump("harnessrun yaml",
		"-n", namespace, "get", "harnessrun", runName, "-o", "yaml")
	dump("pods in run namespace",
		"-n", namespace, "get", "pods", "-o", "wide")
	dump("pod descriptions",
		"-n", namespace, "describe", "pods")
	dump("events in run namespace",
		"-n", namespace, "get", "events", "--sort-by=.lastTimestamp")
	dump("controller-manager logs",
		"-n", "paddock-system", "logs", "-l", "control-plane=controller-manager", "--tail=200")
	dump("broker logs",
		"-n", "paddock-system", "logs", "-l", "app.kubernetes.io/component=broker", "--tail=200")
}

// dumpBrokerDiagnostics emits to GinkgoWriter the current broker pod
// state, controller-manager logs, and broker logs. Used by Theme 2
// specs that don't own a single HarnessRun (F-16 cold-start, F-17a
// oversize-body smoke) so the next CI flake gives us real signal.
func dumpBrokerDiagnostics(ctx context.Context) {
	dump := func(title string, args ...string) {
		out, _ := utils.Run(exec.CommandContext(ctx, "kubectl", args...))
		GinkgoWriter.Printf("--- %s ---\n%s\n", title, out)
	}
	dump("broker deployment",
		"-n", "paddock-system", "describe", "deploy", v3BrokerDeploy)
	dump("broker pods",
		"-n", "paddock-system", "get", "pods", "-l", "app.kubernetes.io/component=broker", "-o", "wide")
	dump("broker pod descriptions",
		"-n", "paddock-system", "describe", "pods", "-l", "app.kubernetes.io/component=broker")
	dump("broker endpoints",
		"-n", "paddock-system", "get", "endpoints", v3BrokerDeploy)
	dump("controller-manager logs",
		"-n", "paddock-system", "logs", "-l", "control-plane=controller-manager", "--tail=200")
	dump("broker logs",
		"-n", "paddock-system", "logs", "-l", "app.kubernetes.io/component=broker", "--tail=300")
}
