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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/utils"
)

const (
	hostileTemplateName = "evil-echo"
	hostilePolicyName   = "evil-echo-policy"

	// In-cluster RFC1918 IP that should be excluded by F-19's NP fix.
	// Picked for stability: 10.244.0.1 is typically the first pod-CIDR
	// IP, present in most Kind clusters.
	rfc1918ProbeTarget = "10.244.0.1:443"
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
		// Cluster-scoped template only needs to be applied once. Each
		// scenario gets its own namespace + BrokerPolicy.
		mustApply("config/samples/paddock_v1alpha1_clusterharnesstemplate_evil_echo.yaml")
	})

	AfterAll(func() {
		// Best-effort cleanup. CI tears down the cluster anyway.
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", hostileNamespace, "--ignore-not-found", "--wait=false"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "clusterharnesstemplate", hostileTemplateName, "--ignore-not-found"))
	})

	Context("F-19: per-run NetworkPolicy denies cooperative-mode bypass to in-cluster IPs", func() {
		It("blocks raw-TCP from agent to RFC1918 even when HTTPS_PROXY is unset (TG-2)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			By("creating a dedicated namespace + BrokerPolicy")
			mustCreateNamespace(hostileNamespace)
			mustApplyToNamespace("config/samples/paddock_v1alpha1_brokerpolicy_evil_echo.yaml", hostileNamespace)

			By("submitting a HarnessRun that attempts cooperative-mode bypass")
			runName := "tg2-cooperative-bypass"
			runManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: %s
    kind: ClusterHarnessTemplate
  prompt: "tg-2 hostile probe"
  args: ["--bypass-proxy-env", "--connect-raw-tcp", "%s"]
`, runName, hostileNamespace, hostileTemplateName, rfc1918ProbeTarget)
			mustApplyManifest(runManifest)

			By("waiting for terminal phase")
			Eventually(func() string {
				return runPhase(ctx, hostileNamespace, runName)
			}, 4*time.Minute, 5*time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

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
		_ = errors.New("no run output available")
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

// silence unused-os import if we add OS calls later
var _ = os.Getenv
