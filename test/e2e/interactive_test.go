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

// Package e2e — Interactive HarnessRun lifecycle + shell scenarios.
//
// These specs exercise Stage A (broker wiring) + Stage B (interactive
// adapter-echo + stay-alive harness) of the Interactive HarnessRun MVP:
//
//   - Lifecycle: create an Interactive HarnessRun, wait for Phase=Running,
//     POST a prompt to the broker over port-forward+SA-token, assert 202,
//     wait for the max-lifetime watchdog to cancel the run, and assert
//     an interactive-run-terminated audit event with reason=max-lifetime.
//   - Shell: open the broker's /v1/runs/.../shell WebSocket, send
//     `echo hello`, assert "hello" appears in the response.
//
// Each spec owns its own HarnessRun against a shared template + policy so
// the lifecycle spec's max-lifetime cancellation does not race the shell
// spec's pod-exec session. Diagnostics on failure mirror hostile_test.go.

package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/coder/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/tools/clientcmd"

	paddockbroker "github.com/tjorri/paddock/internal/paddocktui/broker"
	"github.com/tjorri/paddock/test/e2e/framework"
	"github.com/tjorri/paddock/test/utils"
)

const (
	interactiveTpl          = "interactive-stub"
	interactiveSA           = "interactive-runner"
	interactivePolicy       = "interactive-allow"
	interactiveRunLifecycle = "stub-run-lifecycle"
	interactiveRunShell     = "stub-run-shell"
)

var _ = Describe("interactive run lifecycle", Ordered, Label("interactive"), func() {
	var (
		ns          string
		token       string
		brokerPort  int
		stopForward func()
	)

	BeforeAll(func(ctx SpecContext) {
		ns = framework.CreateTenantNamespace(ctx, "paddock-test-interactive")

		// ServiceAccount the e2e runner authenticates as when calling the
		// broker. The broker only checks token audience + namespace match;
		// no extra RBAC is required for the SA itself.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s`, interactiveSA, ns))

		// HarnessTemplate with interactive support. Short maxLifetime so
		// the lifecycle spec's watchdog fires within the suite budget;
		// the shell spec overrides it via interactiveOverrides so its
		// pod survives the test. The 50s/60s ratio keeps the webhook's
		// idleTimeout<=maxLifetime + detachTimeout<=maxLifetime invariants
		// satisfied.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: %s
  namespace: %s
spec:
  harness: echo
  image: %s
  command: ["/usr/local/bin/paddock-echo"]
  runtime:
    image: %s
  interactive:
    mode: per-prompt-process
    idleTimeout: 50s
    detachIdleTimeout: 50s
    detachTimeout: 50s
    maxLifetime: 60s
  defaults:
    timeout: 5m
  workspace:
    required: true
    mountPath: /workspace`, interactiveTpl, ns, echoImage, adapterEchoImage))

		// BrokerPolicy granting runs.interact + runs.shell against the
		// stub template. Shell command is /bin/sh because the alpine-
		// based echo harness has /bin/sh but no /bin/bash (the broker's
		// default). target=agent so the WS exec lands in the harness
		// container, where the harness's `sleep infinity` loop keeps
		// the pod alive.
		framework.NewBrokerPolicy(ns, interactivePolicy, interactiveTpl).
			GrantInteract().
			GrantShell("agent", "/bin/sh", "-c", "echo hello && sleep 1").
			Apply(ctx)

		// Broker SA-token + port-forward. Both shared by every It in
		// this Describe.
		token = framework.CreateBrokerToken(ctx, ns, interactiveSA)
		brokerPort, stopForward = framework.GetBroker(ctx).PortForward(ctx)
	})

	AfterAll(func() {
		if stopForward != nil {
			stopForward()
		}
	})

	It("cancels a Bound run when its max-lifetime elapses", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctx, ns, interactiveRunLifecycle)
			}
		})

		run := framework.NewRun(ns, interactiveTpl).
			WithName(interactiveRunLifecycle).
			WithMode("Interactive").
			WithPrompt("hello-from-init").
			Submit(ctx)

		By("waiting for Phase=Running")
		run.WaitForPhase(ctx, "Running", 2*time.Minute)

		By("posting a prompt to the broker /v1/runs/.../prompts")
		url := fmt.Sprintf("https://127.0.0.1:%d/v1/runs/%s/%s/prompts",
			brokerPort, ns, interactiveRunLifecycle)
		// Eventually because Phase=Running fires before the pod's PodIP
		// is necessarily populated (init containers can still be
		// running) — the broker's adapter resolver returns 502 "no
		// ready pod" until the kubelet has finished pod startup.
		// 60s is comfortable headroom; the typical settle is a few s.
		var lastStatus int
		var lastBody string
		attempt := 0
		Eventually(func() int {
			attempt++
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
				strings.NewReader(`{"text":"hello-from-test"}`))
			if err != nil {
				fmt.Fprintf(GinkgoWriter, "  [/prompts attempt %d] NewRequest err: %v\n", attempt, err)
				return 0
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := framework.GetBroker(ctx).HTTPClient().Do(req)
			if err != nil {
				fmt.Fprintf(GinkgoWriter, "  [/prompts attempt %d] Do err: %v\n", attempt, err)
				return 0
			}
			lastStatus = resp.StatusCode
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			lastBody = string(bodyBytes)
			_ = resp.Body.Close()
			fmt.Fprintf(GinkgoWriter, "  [/prompts attempt %d] status=%d body=%s\n",
				attempt, lastStatus, lastBody)
			return resp.StatusCode
		}, 60*time.Second, 2*time.Second).Should(Equal(http.StatusAccepted),
			"broker /prompts last_status=%d body=%s", lastStatus, lastBody)

		By("waiting for the max-lifetime watchdog to cancel the run")
		// maxLifetime=60s; the watchdog runs on the controller's
		// reconcile cadence, so allow a generous budget for the
		// cancel + finaliser settle.
		run.WaitForPhase(ctx, "Cancelled", 3*time.Minute)

		By("asserting an interactive-run-terminated audit event with reason=max-lifetime")
		Eventually(func() bool {
			return framework.FindAuditEvent(ctx, ns, interactiveRunLifecycle,
				"interactive-run-terminated", "max-lifetime") != nil
		}, 60*time.Second, 2*time.Second).Should(BeTrue())
	})

	It("/v1/runs/.../shell streams a working agent container", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctx, ns, interactiveRunShell)
				// Explicit broker logs by deployment ref — the label
				// selector form sometimes returns nothing in this
				// suite. Dumps last 100 lines, includes prior runs.
				dumpCtx, dumpCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer dumpCancel()
				out, _ := utils.Run(exec.CommandContext(dumpCtx, "kubectl",
					"-n", "paddock-system", "logs",
					"deploy/paddock-broker", "--tail=100"))
				fmt.Fprintf(GinkgoWriter, "--- broker pod logs (deploy ref) ---\n%s\n", out)
			}
			delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer delCancel()
			_, _ = utils.Run(exec.CommandContext(delCtx, "kubectl", "-n", ns,
				"delete", "harnessrun", interactiveRunShell, "--ignore-not-found", "--wait=false"))
		})

		// Override maxLifetime to 5m so the watchdog doesn't cancel
		// the run mid-shell.
		shellRun := framework.NewRun(ns, interactiveTpl).
			WithName(interactiveRunShell).
			WithMode("Interactive").
			WithPrompt("shell-test").
			WithMaxLifetime(5 * time.Minute).
			Submit(ctx)

		By("waiting for Phase=Running on the shell run")
		shellRun.WaitForPhase(ctx, "Running", 2*time.Minute)

		By("waiting for the run pod to reach PodPhase=Running")
		// HarnessRun.Phase=Running fires from Job.Active, but the
		// pod may not yet be scheduled to a node — pods/exec needs
		// pod.Spec.NodeName populated.
		Eventually(func() string {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", ns,
				"get", "pods", "-l", "paddock.dev/run="+interactiveRunShell,
				"-o", "jsonpath={.items[0].status.phase}"))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(out)
		}, 2*time.Minute, 2*time.Second).Should(Equal("Running"))

		By("dialing /v1/runs/.../shell over WebSocket")
		wsURL := fmt.Sprintf("wss://127.0.0.1:%d/v1/runs/%s/%s/shell",
			brokerPort, ns, interactiveRunShell)
		dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
		defer cancelDial()
		conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
			Subprotocols: []string{"paddock.shell.v1"},
			HTTPClient:   framework.GetBroker(ctx).HTTPClient(),
			HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + token}},
		})
		Expect(err).NotTo(HaveOccurred(), "dial /shell")
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") }()

		By("sending `echo hello` and reading until 'hello' appears")
		Expect(conn.Write(ctx, websocket.MessageBinary, []byte("echo hello\n"))).To(Succeed())

		var got []byte
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
			_, msg, rErr := conn.Read(readCtx)
			readCancel()
			if rErr != nil {
				if errors.Is(rErr, context.DeadlineExceeded) {
					// Brief read timeout — keep looping until the outer
					// deadline. Other errors (peer close, network drop)
					// abort the spec with the bytes captured so far.
					continue
				}
				Fail(fmt.Sprintf("ws read: %v; got so far: %q", rErr, string(got)))
			}
			got = append(got, msg...)
			if strings.Contains(string(got), "hello") {
				return // pass
			}
		}
		Fail(fmt.Sprintf("never received 'hello' from shell within budget; got: %q", string(got)))
	})
})

// Supervisor-mode interactive specs (spec 2026-05-02-interactive-adapter-as-proxy §8,
// testing layer 3): exercise the full broker → adapter → UDS → supervisor →
// fake-CLI → UDS → adapter → broker WS path. The fake-claude harness mimics
// the stream-json contract without the install + API call, so CI runs without
// external network or API budget.
//
// Each It owns its own template + run so the supervisor and per-prompt
// state machines stay isolated. Both specs share the namespace + SA so the
// broker-token and namespace plumbing is set up once.
const (
	supervisorSA               = "supervisor-runner"
	supervisorPolicy           = "supervisor-allow"
	supervisorTplPersistent    = "supervisor-persistent"
	supervisorTplPerPrompt     = "supervisor-per-prompt"
	supervisorRunPersistent    = "supervisor-run-persistent"
	supervisorRunPerPrompt     = "supervisor-run-per-prompt"
	supervisorPolicyPersistent = "supervisor-allow-persistent"
	supervisorPolicyPerPrompt  = "supervisor-allow-per-prompt"
)

var _ = Describe("interactive run via supervisor", Ordered, Label("interactive"), func() {
	var ns string

	BeforeAll(func(ctx SpecContext) {
		ns = framework.CreateTenantNamespace(ctx, "paddock-supervisor-e2e")

		// ServiceAccount the broker client authenticates as. The broker
		// only checks audience + namespace; no extra RBAC is required
		// beyond what the default service-account-issuer grants.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s`, supervisorSA, ns))

		// Persistent-process template. The supervisor + fake-claude
		// pair clean up cleanly on /end (supervisor closes stdin to
		// fake-claude, fake-claude's read loop exits, supervisor's
		// cmd.Wait returns), so the run reaches Succeeded naturally —
		// no max-lifetime watchdog dependency.
		//
		// idleTimeout/detachIdleTimeout/detachTimeout/maxLifetime kept
		// short so any flake (e.g. /end never wired through) hits the
		// watchdog inside the suite budget. The annotation matches
		// spec.interactive.mode so the webhook accepts the run.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: %s
  namespace: %s
  annotations:
    paddock.dev/runtime-interactive-modes: "persistent-process,per-prompt-process"
spec:
  harness: claude-code-fake
  image: %s
  command: ["/usr/local/bin/paddock-claude-code-fake"]
  runtime:
    image: %s
  interactive:
    mode: persistent-process
    idleTimeout: 50s
    detachIdleTimeout: 50s
    detachTimeout: 50s
    maxLifetime: 90s
  defaults:
    timeout: 5m
  workspace:
    required: true
    mountPath: /workspace`, supervisorTplPersistent, ns, claudeCodeFakeImage, adapterClaudeCodeImage))

		// Per-prompt-process template. Same shape, different
		// interactive.mode — the controller wires PADDOCK_INTERACTIVE_MODE
		// onto the supervisor + adapter containers so each turn spawns
		// a fresh fake-claude process.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: %s
  namespace: %s
  annotations:
    paddock.dev/runtime-interactive-modes: "persistent-process,per-prompt-process"
spec:
  harness: claude-code-fake
  image: %s
  command: ["/usr/local/bin/paddock-claude-code-fake"]
  runtime:
    image: %s
  interactive:
    mode: per-prompt-process
    idleTimeout: 50s
    detachIdleTimeout: 50s
    detachTimeout: 50s
    maxLifetime: 90s
  defaults:
    timeout: 5m
  workspace:
    required: true
    mountPath: /workspace`, supervisorTplPerPrompt, ns, claudeCodeFakeImage, adapterClaudeCodeImage))

		// Per-template BrokerPolicy granting runs.interact. Two
		// policies (one per template) so each spec's admission check is
		// independent.
		framework.NewBrokerPolicy(ns, supervisorPolicyPersistent, supervisorTplPersistent).
			GrantInteract().
			Apply(ctx)
		framework.NewBrokerPolicy(ns, supervisorPolicyPerPrompt, supervisorTplPerPrompt).
			GrantInteract().
			Apply(ctx)
	})

	// runSupervisorRoundTrip is the shared driver for both modes. Each
	// call submits a HarnessRun against the named template, opens the
	// broker stream via paddockbroker.Client, posts a prompt, asserts a
	// stream-json frame returns, ends the run, and asserts the run
	// reaches Succeeded. The events.jsonl assertion is via
	// HarnessRun.status.recentEvents (populated by the collector
	// flushing the workspace events.jsonl into the output ConfigMap),
	// which is the test surface the framework exposes for events.
	runSupervisorRoundTrip := func(ctx context.Context, runName, templateName string) {
		By("submitting an Interactive HarnessRun against the supervisor template")
		run := framework.NewRun(ns, templateName).
			WithName(runName).
			WithMode("Interactive").
			WithPrompt(`{"role":"user","content":"hello supervisor"}`).
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

		By("constructing a paddockbroker.Client via New")
		bCtx, bCancel := context.WithTimeout(ctx, 30*time.Second)
		bc, err := paddockbroker.New(bCtx, paddockbroker.Options{
			Service:                 "paddock-broker",
			Namespace:               "paddock-system",
			Port:                    8443,
			ServiceAccount:          supervisorSA,
			ServiceAccountNamespace: ns,
			Source:                  restCfg,
			CASecretName:            "broker-serving-cert",
			CASecretNamespace:       "paddock-system",
		})
		bCancel()
		Expect(err).NotTo(HaveOccurred(), "paddockbroker.New")
		defer func() { _ = bc.Close() }()

		By("opening the broker stream")
		ch, err := bc.Open(ctx, ns, runName)
		Expect(err).NotTo(HaveOccurred(), "broker.Open")

		By("posting a prompt to the broker /v1/runs/.../prompts via paddockbroker.Submit")
		// The stream-json input the prompt body is serialized into is
		// passed verbatim to the supervisor's data UDS, which pipes it
		// to fake-claude's stdin. fake-claude echoes back as
		// {"type":"assistant","message":<input>}\n. The adapter
		// converts each line and forwards to the broker stream.
		//
		// Submit may transiently fail with HTTP 502 while the adapter
		// is warming up (the agent container's run.sh + supervisor
		// take a few seconds to bind UDS, during which the adapter
		// dial-with-backoff is still connecting). Retry on 502 until
		// the broker accepts the prompt; ErrTurnInFlight (409) means
		// a previous attempt's turn is still recorded — also a
		// successful exit.
		var submittedSeq int32
		Eventually(func(g Gomega) {
			seq, sErr := bc.Submit(ctx, ns, runName, `{"role":"user","content":"ping"}`)
			if paddockbroker.IsTurnInFlight(sErr) {
				return // a previous attempt got in; treat as success
			}
			g.Expect(sErr).NotTo(HaveOccurred())
			submittedSeq = seq
		}, 60*time.Second, 1*time.Second).Should(Succeed())
		GinkgoWriter.Printf("submitted prompt seq=%d\n", submittedSeq)

		By("waiting for at least one stream-json frame within 30s")
		// Generous budget: the supervisor + adapter UDS round-trip is
		// sub-second once the connections are up, but pod warm-up +
		// adapter dial-with-backoff can take a few seconds on a busy
		// CI node. 30s is comfortable headroom over the typical settle.
		select {
		case f, ok := <-ch:
			Expect(ok).To(BeTrue(), "stream channel closed before any frame arrived")
			Expect(f.Type).NotTo(BeEmpty(),
				"received a frame but Type is empty: %+v", f)
			GinkgoWriter.Printf("received StreamFrame type=%q data=%s\n", f.Type, string(f.Data))
		case <-time.After(30 * time.Second):
			Fail("no StreamFrame arrived within 30s")
		}

		By("ending the run via paddockbroker.End")
		endCtx, endCancel := context.WithTimeout(ctx, 30*time.Second)
		defer endCancel()
		Expect(bc.End(endCtx, ns, runName, "test-complete")).To(Succeed(), "broker.End")

		By("waiting for the run to reach Succeeded (supervisor exits cleanly on /end)")
		// Termination chain for a supervisor-driven run on /end:
		//
		//   broker /end → adapter ctlMessage{"action":"end"} →
		//   supervisor closes stdin to fake-claude → fake-claude
		//   read loop exits → supervisor cmd.Wait returns →
		//   agent container exits 0 → Job completes → run = Succeeded.
		//
		// Cancelled is a tolerated outcome: if /end races the
		// max-lifetime watchdog (or some other path), the run may
		// land Cancelled instead. Either is a clean termination.
		run.WaitForPhaseIn(ctx, []string{"Succeeded", "Cancelled"}, 3*time.Minute)

		By("asserting status.recentEvents contains a fake-harness 'assistant' frame")
		// The adapter-claude-code converter emits a Message-type
		// PaddockEvent for every "assistant" stream frame, and the
		// collector flushes the workspace events.jsonl into the
		// output ConfigMap which the controller projects into
		// status.recentEvents. Thus a single recentEvents entry of
		// type=Message is sufficient evidence the full path worked.
		//
		// status.recentEvents is updated on a reconcile that fires
		// after the collector flushes the ConfigMap — Eventually
		// covers the small post-Succeeded settle window.
		Eventually(func(g Gomega) {
			status := run.Status(ctx)
			g.Expect(status.RecentEvents).NotTo(BeEmpty(),
				"expected at least one recentEvent; status=%+v", status)
		}, 60*time.Second, 2*time.Second).Should(Succeed())
	}

	It("completes a persistent-process round-trip", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctx, ns, supervisorRunPersistent)
			}
			delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer delCancel()
			_, _ = utils.Run(exec.CommandContext(delCtx, "kubectl", "-n", ns,
				"delete", "harnessrun", supervisorRunPersistent, "--ignore-not-found", "--wait=false"))
		})
		runSupervisorRoundTrip(ctx, supervisorRunPersistent, supervisorTplPersistent)
	})

	It("completes a per-prompt-process round-trip", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctx, ns, supervisorRunPerPrompt)
			}
			delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer delCancel()
			_, _ = utils.Run(exec.CommandContext(delCtx, "kubectl", "-n", ns,
				"delete", "harnessrun", supervisorRunPerPrompt, "--ignore-not-found", "--wait=false"))
		})
		runSupervisorRoundTrip(ctx, supervisorRunPerPrompt, supervisorTplPerPrompt)
	})
})
