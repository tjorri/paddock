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
	"context"
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

var _ = Describe("Phase 2a P0 hotfix validation (hostile harness)", Ordered, func() {
	BeforeAll(func() {
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
		// Theme 2 broker-hygiene specs each create their own namespace
		// via mustCreateNamespace with inline DeferCleanup; list them
		// here as a belt-and-braces drain while the controller is
		// still alive.
		hostileNamespaces := []string{
			"paddock-t2-revoke", "paddock-t2-oversize",
		}

		// 1. Kick every namespace's reconcile-delete chain in parallel.
		for _, ns := range hostileNamespaces {
			_, _ = framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete", "ns", ns,
				"--wait=false", "--ignore-not-found=true")
		}

		// 2. Wait for each to terminate; force-clear on timeout. Same
		//    120s budget as the pipeline AfterAll — covers HarnessRun
		//    Job teardown + Workspace finalizer requeue cadence.
		for _, ns := range hostileNamespaces {
			if framework.WaitForNamespaceGone(context.Background(), ns, 120*time.Second) {
				continue
			}
			fmt.Fprintf(GinkgoWriter,
				"WARNING: namespace %s stuck in Terminating after 120s; "+
					"controller-side finalizer drain likely broken — force-clearing\n", ns)
			framework.ForceClearFinalizers(context.Background(), ns)
			framework.WaitForNamespaceGone(context.Background(), ns, 20*time.Second)
		}

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
		"-n", "paddock-system", "describe", "deploy", framework.BrokerDeployName)
	dump("broker pods",
		"-n", "paddock-system", "get", "pods", "-l", "app.kubernetes.io/component=broker", "-o", "wide")
	dump("broker pod descriptions",
		"-n", "paddock-system", "describe", "pods", "-l", "app.kubernetes.io/component=broker")
	dump("broker endpoints",
		"-n", "paddock-system", "get", "endpoints", framework.BrokerDeployName)
	dump("controller-manager logs",
		"-n", "paddock-system", "logs", "-l", "control-plane=controller-manager", "--tail=200")
	dump("broker logs",
		"-n", "paddock-system", "logs", "-l", "app.kubernetes.io/component=broker", "--tail=300")
}
