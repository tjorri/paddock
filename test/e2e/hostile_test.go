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

		// The earlier "paddock v0.1-v0.3 pipeline" Ordered Describe's
		// AfterAll runs `make undeploy` + `make uninstall`, so by the
		// time we get here the CRDs and controller are gone. Re-install
		// them here. Mirrors the existing pipeline's BeforeAll pattern.
		By("installing CRDs")
		_, err := utils.Run(exec.Command("make", "install"))
		Expect(err).NotTo(HaveOccurred())

		By("deploying the controller-manager")
		_, err = utils.Run(exec.Command("make", "deploy", "IMG=paddock-manager:dev"))
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the controller-manager to roll out")
		_, err = utils.Run(exec.Command("kubectl", "-n", "paddock-system",
			"rollout", "status", "deploy/paddock-controller-manager", "--timeout=180s"))
		Expect(err).NotTo(HaveOccurred())

		// Cluster-scoped templates only need to be applied once. Each
		// scenario gets its own namespace + BrokerPolicy.
		mustApply("config/samples/paddock_v1alpha1_clusterharnesstemplate_evil_echo.yaml")
	})

	AfterAll(func() {
		// Best-effort cleanup. CI tears down the cluster anyway.
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", hostileNamespace, "--ignore-not-found", "--wait=false"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "clusterharnesstemplate", "evil-echo-tg2", "--ignore-not-found"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "clusterharnesstemplate", "evil-echo-tg7", "--ignore-not-found"))
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
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", seedNamespace, "--wait=false"))
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

// readRunOutput returns the concatenated text from the run's output
// ConfigMap. The collector sidecar writes evil-echo's stdout there.
func readRunOutput(ctx context.Context, namespace, name string) string {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "get", "configmap",
		name+"-out", "-o", "jsonpath={.data}"))
	if err != nil {
		// Fallback: read pod logs directly. Slower path but
		// resilient when the output ConfigMap shape isn't guaranteed.
		jobName, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "get", "harnessrun", name,
			"-o", "jsonpath={.status.jobName}"))
		jobName = strings.TrimSpace(jobName)
		if jobName != "" {
			podName, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "get", "pods",
				"-l", "job-name="+jobName, "-o", "jsonpath={.items[0].metadata.name}"))
			podName = strings.TrimSpace(podName)
			if podName != "" {
				logs, _ := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "logs",
					podName, "-c", "agent"))
				return logs
			}
		}
		return ""
	}
	// jsonpath={.data} returns map[string]string serialised; tests want
	// the values concatenated. Re-fetch as JSON and join.
	jsonOut, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace, "get", "configmap",
		name+"-out", "-o", "json"))
	if err != nil {
		return out
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &cm); err != nil {
		return out
	}
	parts := make([]string, 0, len(cm.Data))
	for _, v := range cm.Data {
		parts = append(parts, v)
	}
	return strings.Join(parts, "\n")
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
