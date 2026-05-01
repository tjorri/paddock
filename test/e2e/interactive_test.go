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
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/coder/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/e2e/framework"
	"paddock.dev/paddock/test/utils"
)

const (
	interactiveNS           = "paddock-test-interactive"
	interactiveTpl          = "interactive-stub"
	interactiveSA           = "interactive-runner"
	interactivePolicy       = "interactive-allow"
	interactiveRunLifecycle = "stub-run-lifecycle"
	interactiveRunShell     = "stub-run-shell"
)

var _ = Describe("Interactive HarnessRun lifecycle", Ordered, func() {
	var (
		token       string
		brokerPort  int
		stopForward func()
	)

	BeforeAll(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Clean slate. Per-Describe namespace; AfterAll deletes it again.
		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", interactiveNS, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		mustCreateNamespace(interactiveNS)

		// ServiceAccount the e2e runner authenticates as when calling the
		// broker. The broker only checks token audience + namespace match;
		// no extra RBAC is required for the SA itself.
		mustApplyManifest(fmt.Sprintf(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s`, interactiveSA, interactiveNS))

		// HarnessTemplate with interactive support. Short maxLifetime so
		// the lifecycle spec's watchdog fires within the suite budget;
		// the shell spec overrides it via interactiveOverrides so its
		// pod survives the test. The 50s/60s ratio keeps the webhook's
		// idleTimeout<=maxLifetime + detachTimeout<=maxLifetime invariants
		// satisfied.
		mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: %s
  namespace: %s
spec:
  harness: echo
  image: %s
  command: ["/usr/local/bin/paddock-echo"]
  eventAdapter:
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
    mountPath: /workspace`, interactiveTpl, interactiveNS, echoImage, adapterEchoImage))

		// BrokerPolicy granting runs.interact + runs.shell against the
		// stub template. Shell command is /bin/sh because the alpine-
		// based echo harness has /bin/sh but no /bin/bash (the broker's
		// default). target=agent so the WS exec lands in the harness
		// container, where the harness's `sleep infinity` loop keeps
		// the pod alive.
		mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: %s
  namespace: %s
spec:
  appliesToTemplates: ["%s"]
  grants:
    runs:
      interact: true
      shell:
        target: agent
        command: ["/bin/sh", "-c", "echo hello && sleep 1"]`, interactivePolicy, interactiveNS, interactiveTpl))

		// Broker SA-token + port-forward. Both shared by every It in
		// this Describe.
		token = createBrokerToken(ctx, interactiveNS, interactiveSA)
		brokerPort, stopForward = framework.GetBroker(ctx).PortForward(ctx)
	})

	AfterAll(func() {
		if stopForward != nil {
			stopForward()
		}
		if os.Getenv("KEEP_E2E_RUN") == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", interactiveNS, "--ignore-not-found", "--wait=true", "--timeout=60s"))
	})

	It("reaches Running, accepts a prompt via /v1/runs/.../prompts, and is cancelled by the max-lifetime watchdog", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpRunDiagnostics(ctx, interactiveNS, interactiveRunLifecycle)
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
    name: %s
    kind: HarnessTemplate
  mode: Interactive
  prompt: "hello-from-init"`, interactiveRunLifecycle, interactiveNS, interactiveTpl))

		By("waiting for Phase=Running")
		Eventually(func() string { return runPhase(ctx, interactiveNS, interactiveRunLifecycle) },
			2*time.Minute, 2*time.Second).Should(Equal("Running"))

		By("posting a prompt to the broker /v1/runs/.../prompts")
		url := fmt.Sprintf("https://127.0.0.1:%d/v1/runs/%s/%s/prompts",
			brokerPort, interactiveNS, interactiveRunLifecycle)
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
		Eventually(func() string { return runPhase(ctx, interactiveNS, interactiveRunLifecycle) },
			3*time.Minute, 5*time.Second).Should(Equal("Cancelled"))

		By("asserting an interactive-run-terminated audit event with reason=max-lifetime")
		Eventually(func() bool {
			return framework.FindAuditEvent(ctx, interactiveNS, interactiveRunLifecycle,
				"interactive-run-terminated", "max-lifetime") != nil
		}, 60*time.Second, 2*time.Second).Should(BeTrue())
	})

	It("opens a shell over /v1/runs/.../shell and runs echo hello", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpRunDiagnostics(ctx, interactiveNS, interactiveRunShell)
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
			_, _ = utils.Run(exec.CommandContext(delCtx, "kubectl", "-n", interactiveNS,
				"delete", "harnessrun", interactiveRunShell, "--ignore-not-found", "--wait=false"))
		})

		// Override maxLifetime to 5m so the watchdog doesn't cancel
		// the run mid-shell.
		mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: %s
    kind: HarnessTemplate
  mode: Interactive
  prompt: "shell-test"
  interactiveOverrides:
    maxLifetime: 5m`, interactiveRunShell, interactiveNS, interactiveTpl))

		By("waiting for Phase=Running on the shell run")
		Eventually(func() string { return runPhase(ctx, interactiveNS, interactiveRunShell) },
			2*time.Minute, 2*time.Second).Should(Equal("Running"))

		By("waiting for the run pod to reach PodPhase=Running")
		// HarnessRun.Phase=Running fires from Job.Active, but the
		// pod may not yet be scheduled to a node — pods/exec needs
		// pod.Spec.NodeName populated.
		Eventually(func() string {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", interactiveNS,
				"get", "pods", "-l", "paddock.dev/run="+interactiveRunShell,
				"-o", "jsonpath={.items[0].status.phase}"))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(out)
		}, 2*time.Minute, 2*time.Second).Should(Equal("Running"))

		By("dialing /v1/runs/.../shell over WebSocket")
		wsURL := fmt.Sprintf("wss://127.0.0.1:%d/v1/runs/%s/%s/shell",
			brokerPort, interactiveNS, interactiveRunShell)
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

// createBrokerToken issues a fresh SA token for `sa` in `namespace` with
// audience=paddock-broker. The duration is 10m which comfortably covers
// every It in this Describe.
func createBrokerToken(ctx context.Context, namespace, sa string) string {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace,
		"create", "token", sa,
		"--audience=paddock-broker",
		"--duration=10m"))
	Expect(err).NotTo(HaveOccurred(), "kubectl create token: %s", out)
	return strings.TrimSpace(out)
}
