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

// Package e2e — Unified-runtime transcript layout assertions.
//
// These specs lock in the post-Task 12 contract that the runtime
// sidecar persists every PaddockEvent to /workspace/.paddock/runs/
// <run-name>/events.jsonl on the workspace PVC, alongside metadata.json
// describing the run, and that the same byte stream surfaces on
// `kubectl logs <pod> -c runtime` for parity with the file. See
// docs/superpowers/specs/2026-05-03-unified-runtime-design.md §6, §8.
//
// The supervisor + fake-claude harness drives multi-prompt traffic
// through the broker → runtime → UDS → fake-CLI → UDS → runtime →
// transcript path so PromptSubmitted (input) and Result (turn-terminal)
// events both get exercised.

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
	"k8s.io/client-go/tools/clientcmd"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	paddockbroker "github.com/tjorri/paddock/internal/paddocktui/broker"
	"github.com/tjorri/paddock/test/e2e/framework"
	"github.com/tjorri/paddock/test/utils"
)

const (
	transcriptSA      = "transcript-runner"
	transcriptTpl     = "transcript-fake"
	transcriptPolicy  = "transcript-allow"
	transcriptRunName = "transcript-multi"
	transcriptWorkspc = "transcript-ws"
	transcriptPromptN = 3
	transcriptArchDir = "/workspace/.paddock/runs"
)

var _ = Describe("unified-runtime transcript layout", Ordered, Label("interactive"), func() {
	var (
		ns      string
		podName string
	)

	BeforeAll(func(ctx SpecContext) {
		ns = framework.CreateTenantNamespace(ctx, "paddock-transcript-e2e")

		// Service account the broker client authenticates as. Broker only
		// checks audience + namespace; no extra RBAC required.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s`, transcriptSA, ns))

		// Persistent-process supervisor template. Long enough maxLifetime
		// to drive 3 prompts plus terminal /end without the watchdog
		// firing mid-test.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: %s
  namespace: %s
  annotations:
    paddock.dev/runtime-interactive-modes: "persistent-process"
spec:
  harness: claude-code-fake
  image: %s
  command: ["/usr/local/bin/paddock-claude-code-fake"]
  runtime:
    image: %s
  interactive:
    mode: persistent-process
    idleTimeout: 4m
    detachIdleTimeout: 4m
    detachTimeout: 4m
    maxLifetime: 6m
  defaults:
    timeout: 8m
  workspace:
    required: true
    mountPath: /workspace`, transcriptTpl, ns, claudeCodeFakeImage, runtimeClaudeCodeImage))

		framework.NewBrokerPolicy(ns, transcriptPolicy, transcriptTpl).
			GrantInteract().
			Apply(ctx)

		// Named workspace so the runtime has a PVC to land the archive
		// directory on. Capacity is generous for the multi-prompt
		// transcript even though the actual bytes are tiny.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  storage:
    size: 100Mi`, transcriptWorkspc, ns))

		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", ns,
				"get", "workspace", transcriptWorkspc,
				"-o", "jsonpath={.status.phase}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("Active"),
				"workspace still in phase %q", strings.TrimSpace(out))
		}, 2*time.Minute, 3*time.Second).Should(Succeed())
	})

	// Single It carries the full multi-prompt round-trip; the three
	// transcript assertions below are gated on its success because
	// they all read the same on-disk archive the round-trip writes.
	It("persists a multi-prompt round-trip transcript", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctx, ns, transcriptRunName)
			}
			delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer delCancel()
			_, _ = utils.Run(exec.CommandContext(delCtx, "kubectl", "-n", ns,
				"delete", "harnessrun", transcriptRunName,
				"--ignore-not-found", "--wait=false"))
		})

		By("submitting an Interactive HarnessRun against the persistent-process template")
		// Initial prompt is consumed by the supervisor at startup; the
		// 3 prompts asserted on are submitted via paddockbroker below.
		// PromptSubmitted events in the transcript include the initial
		// one, so the multi-prompt assertion budget is >=3 rather than
		// ==3 (we count >=3 on both PromptSubmitted and Result).
		run := framework.NewRun(ns, transcriptTpl).
			WithName(transcriptRunName).
			WithMode("Interactive").
			WithWorkspace(transcriptWorkspc).
			WithPrompt(`{"role":"user","content":"warmup"}`).
			Submit(ctx)

		By("waiting for Phase=Running")
		run.WaitForPhase(ctx, "Running", 3*time.Minute)

		By("building a rest.Config from the current kubeconfig")
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules,
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		Expect(err).NotTo(HaveOccurred(), "build rest.Config from kubeconfig")

		By("constructing a paddockbroker.Client")
		bCtx, bCancel := context.WithTimeout(ctx, 30*time.Second)
		bc, err := paddockbroker.New(bCtx, paddockbroker.Options{
			Service:                 "paddock-broker",
			Namespace:               "paddock-system",
			Port:                    8443,
			ServiceAccount:          transcriptSA,
			ServiceAccountNamespace: ns,
			Source:                  restCfg,
			CASecretName:            "broker-serving-cert",
			CASecretNamespace:       "paddock-system",
		})
		bCancel()
		Expect(err).NotTo(HaveOccurred(), "paddockbroker.New")
		defer func() { _ = bc.Close() }()

		By("opening the broker stream")
		ch, err := bc.Open(ctx, ns, transcriptRunName)
		Expect(err).NotTo(HaveOccurred(), "broker.Open")

		// Drain frames in a goroutine until the run ends; collecting them
		// is incidental — the load-bearing assertion is the on-disk
		// transcript at the end. The drain prevents the channel from
		// filling and back-pressuring the broker stream.
		framesDone := make(chan struct{})
		go func() {
			defer close(framesDone)
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-ch:
					if !ok {
						return
					}
				}
			}
		}()

		By(fmt.Sprintf("submitting %d prompts and waiting for each turn to drain", transcriptPromptN))
		// Submit serializes against the broker's CurrentTurnSeq gate.
		// Each fake-claude turn ends with a result frame, which fires
		// the runtime's OnTurnComplete and clears the gate so the next
		// prompt is accepted. Eventually wraps Submit so the (rare)
		// 502 during pod warm-up window gets retried; ErrTurnInFlight
		// is treated as a successful queueing of a prior attempt.
		for i := 0; i < transcriptPromptN; i++ {
			text := fmt.Sprintf(`{"role":"user","content":"prompt-%d"}`, i+1)
			Eventually(func(g Gomega) {
				_, sErr := bc.Submit(ctx, ns, transcriptRunName, text)
				if paddockbroker.IsTurnInFlight(sErr) {
					return // a previous attempt got in; keep going
				}
				g.Expect(sErr).NotTo(HaveOccurred(),
					"Submit failed on prompt %d", i+1)
			}, 90*time.Second, 1*time.Second).Should(Succeed())
			GinkgoWriter.Printf("submitted prompt %d\n", i+1)
		}

		// Capture the pod name before any teardown; we'll exec into the
		// runtime container while the run is still active (BEFORE End)
		// because End drives the pod to a terminal phase and `kubectl
		// exec` is rejected against Succeeded/Failed pods.
		podName = run.PodName(ctx)
		Expect(podName).NotTo(BeEmpty(), "PodName empty")

		// --- Assertion 1: events.jsonl carries every prompt + result ---
		By("reading /workspace/.paddock/runs/<run>/events.jsonl from the runtime container (pre-End)")
		eventsPath := fmt.Sprintf("%s/%s/events.jsonl", transcriptArchDir, transcriptRunName)
		var transcriptText string
		Eventually(func(g Gomega) {
			out, err := framework.RunCmd(ctx, "kubectl", "-n", ns,
				"exec", podName, "-c", "runtime", "--",
				"cat", eventsPath)
			g.Expect(err).NotTo(HaveOccurred(),
				"kubectl exec cat events.jsonl: %v", err)
			g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty(),
				"events.jsonl empty: %q", out)
			transcriptText = out
		}, 60*time.Second, 2*time.Second).Should(Succeed())

		// --- Assertion 2 prep: kubectl logs while pod is still running ---
		By("capturing `kubectl logs <pod> -c runtime` (pre-End)")
		logsBeforeEnd, err := framework.RunCmd(ctx, "kubectl", "-n", ns,
			"logs", podName, "-c", "runtime")
		Expect(err).NotTo(HaveOccurred(), "kubectl logs runtime")

		// --- Assertion 3 prep: metadata.json captured while pod runs ---
		By("reading /workspace/.paddock/runs/<run>/metadata.json (pre-End)")
		metaPath := fmt.Sprintf("%s/%s/metadata.json", transcriptArchDir, transcriptRunName)
		metaText, err := framework.RunCmd(ctx, "kubectl", "-n", ns,
			"exec", podName, "-c", "runtime", "--",
			"cat", metaPath)
		Expect(err).NotTo(HaveOccurred(),
			"kubectl exec cat metadata.json: %v", err)

		By("ending the run via paddockbroker.End")
		endCtx, endCancel := context.WithTimeout(ctx, 30*time.Second)
		defer endCancel()
		Expect(bc.End(endCtx, ns, transcriptRunName, "transcript-test")).
			To(Succeed(), "broker.End")

		By("waiting for the run to reach a terminal phase (Succeeded or Cancelled)")
		run.WaitForPhaseIn(ctx, []string{"Succeeded", "Cancelled"}, 4*time.Minute)

		// Stop the frame drain — the channel will close on its own once
		// the broker tears down the WS, but waiting bounds the goroutine
		// for cleanup hygiene.
		select {
		case <-framesDone:
		case <-time.After(15 * time.Second):
			GinkgoWriter.Printf("frame drain still running 15s after End; proceeding\n")
		}

		var prompts, results int
		for _, line := range strings.Split(transcriptText, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var ev paddockv1alpha1.PaddockEvent
			Expect(json.Unmarshal([]byte(line), &ev)).To(Succeed(),
				"events.jsonl line is not a PaddockEvent: %q", line)
			Expect(ev.SchemaVersion).To(Equal("1"),
				"event schemaVersion: %q", ev.SchemaVersion)
			switch ev.Type {
			case paddockv1alpha1.PaddockEventTypePromptSubmitted:
				prompts++
			case paddockv1alpha1.PaddockEventTypeResult:
				results++
			}
		}
		Expect(prompts).To(BeNumerically(">=", transcriptPromptN),
			"expected >=%d PromptSubmitted events; got %d (transcript=%q)",
			transcriptPromptN, prompts, transcriptText)
		Expect(results).To(BeNumerically(">=", transcriptPromptN),
			"expected >=%d Result events; got %d (transcript=%q)",
			transcriptPromptN, results, transcriptText)

		// --- Assertion 2: kubectl logs runtime mirrors the file ---
		By("verifying `kubectl logs <pod> -c runtime` matches events.jsonl byte stream")
		// kubectl-logs is allowed to add a trailing newline; compare on
		// the trimmed JSONL line set rather than byte-for-byte to stay
		// tolerant. Each line should be present in both surfaces. We
		// captured logs pre-End above for the same reason as the file
		// read — the pod's terminal phase blocks kubectl exec/logs.
		fileLines := splitNonEmptyLines(transcriptText)
		logLines := splitNonEmptyLines(logsBeforeEnd)
		Expect(len(logLines)).To(BeNumerically(">=", len(fileLines)),
			"runtime stdout has fewer lines (%d) than events.jsonl (%d)",
			len(logLines), len(fileLines))
		// Every events.jsonl line must appear (in order) in the logs.
		// We allow the logs to have additional bookkeeping lines around
		// startup, but the JSONL prefix where transcripts live should
		// match line-for-line.
		fileIdx := 0
		for _, ll := range logLines {
			if fileIdx >= len(fileLines) {
				break
			}
			if ll == fileLines[fileIdx] {
				fileIdx++
			}
		}
		Expect(fileIdx).To(Equal(len(fileLines)),
			"events.jsonl line %d %q not found in runtime logs in order",
			fileIdx, safeIndex(fileLines, fileIdx))

		// --- Assertion 3: metadata.json is well-formed ---
		// We use the metaText we captured pre-End above for the same
		// reason as the file/log reads — terminal phase blocks
		// kubectl exec. The completion fields (completedAt, exitStatus)
		// are only set after agent shutdown so we don't assert on them
		// here; the start-time fields are sufficient to verify the
		// archive layout.
		By("verifying the metadata.json captured pre-End is well-formed")

		var meta struct {
			SchemaVersion string `json:"schemaVersion"`
			Run           struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"run"`
			Workspace string `json:"workspace"`
			Mode      string `json:"mode"`
			StartedAt string `json:"startedAt"`
		}
		Expect(json.Unmarshal([]byte(metaText), &meta)).To(Succeed(),
			"metadata.json is not valid JSON: %q", metaText)
		Expect(meta.SchemaVersion).To(Equal("1"))
		Expect(meta.Run.Name).To(Equal(transcriptRunName))
		Expect(meta.Run.Namespace).To(Equal(ns))
		Expect(meta.Workspace).To(Equal(transcriptWorkspc))
		Expect(meta.Mode).To(Equal("Interactive"))
		_, tsErr := time.Parse(time.RFC3339, meta.StartedAt)
		Expect(tsErr).NotTo(HaveOccurred(),
			"startedAt %q is not RFC3339", meta.StartedAt)
	})
})

// splitNonEmptyLines splits on \n and drops empty/whitespace lines.
func splitNonEmptyLines(s string) []string {
	out := make([]string, 0, strings.Count(s, "\n")+1)
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// safeIndex returns lines[i] or a placeholder if i is out of range.
func safeIndex(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return "<out-of-range>"
	}
	return lines[i]
}
