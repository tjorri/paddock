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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
			mustApplyManifest(framework.PATPoolFixtureManifest(t2Namespace, "t2-revoke", 2))

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
				return framework.IssuedLeaseCount(ctx, t2Namespace, runName)
			}, 90*time.Second, 2*time.Second).Should(BeNumerically(">=", 1))

			By("recording the current PATPool leased count from broker metrics")
			leasedBefore := framework.GetBroker(ctx).Metric(ctx, "paddock_broker_patpool_leased")

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
				return framework.GetBroker(ctx).Metric(ctx, "paddock_broker_patpool_leased")
			}, 30*time.Second, 2*time.Second).Should(BeNumerically("<", leasedBefore),
				"PATPool leased count did not decrease after run delete; lease was not revoked")
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
			pod := framework.GetBroker(ctx).PodName(ctx)
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

			httpClient := framework.GetBroker(ctx).HTTPClient()
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
