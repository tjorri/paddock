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

// Package e2e — TUI broker client drives an Interactive HarnessRun.
//
// Verifies that paddockbroker.Client (the TUI's programmatic broker
// interface) can:
//   - open the broker's WebSocket stream for an Interactive run,
//   - receive at least one StreamFrame within a reasonable window, and
//   - cleanly end the run via client.End, driving it to a terminal phase.
//
// The test provisions its own namespace, HarnessTemplate, Workspace,
// BrokerPolicy, and HarnessRun so it is fully isolated from the other
// Interactive specs in interactive_test.go. AfterAll cleans up every
// resource so the cluster is left in a clean state.
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
	"k8s.io/client-go/tools/clientcmd"

	paddockbroker "paddock.dev/paddock/internal/paddocktui/broker"
	"paddock.dev/paddock/test/e2e/framework"
	"paddock.dev/paddock/test/utils"
)

const (
	tuiE2ENS        = "paddock-tui-e2e"
	tuiE2ETpl       = "tui-echo-interactive"
	tuiE2EWorkspace = "tui-e2e-ws"
	tuiE2ESA        = "tui-e2e-runner"
	tuiE2EPolicy    = "tui-e2e-allow"
	tuiE2ERun       = "tui-int-run"
)

var _ = Describe("interactive run via TUI client", Ordered, func() {
	BeforeAll(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Clean slate in case a prior interrupted run left debris.
		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", tuiE2ENS,
			"--ignore-not-found", "--wait=true", "--timeout=60s"))

		framework.CreateNamespace(ctx, tuiE2ENS)

		// ServiceAccount the broker client authenticates as. The broker
		// only checks audience + namespace; no extra RBAC on the SA itself
		// is required beyond what the default service-account-issuer grants.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s`, tuiE2ESA, tuiE2ENS))

		// HarnessTemplate with per-prompt-process interactive mode.
		//
		// Timing rationale (mirrors interactive_test.go's stub template):
		// the echo adapter's /end is a no-op stub — it can't kill the
		// agent container's `sleep infinity` across PID namespaces — so
		// the controller-side watchdog is the only thing that drives the
		// run to a terminal phase. Setting maxLifetime: 60s ensures the
		// max-lifetime watchdog fires within the test's 180s terminal
		// wait. The 50s idle/detach values keep the webhook invariants
		// idleTimeout<=maxLifetime + detachTimeout<=maxLifetime satisfied.
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
    mountPath: /workspace`, tuiE2ETpl, tuiE2ENS, echoImage, adapterEchoImage))

		// BrokerPolicy granting runs.interact against the template.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: %s
  namespace: %s
spec:
  appliesToTemplates: ["%s"]
  grants:
    runs:
      interact: true`, tuiE2EPolicy, tuiE2ENS, tuiE2ETpl))

		// Named Workspace so the run has a PVC to mount.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  storage:
    size: 100Mi`, tuiE2EWorkspace, tuiE2ENS))

		// Wait for the Workspace to reach Active before submitting runs.
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", tuiE2ENS,
				"get", "workspace", tuiE2EWorkspace,
				"-o", "jsonpath={.status.phase}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("Active"),
				"workspace still in phase %q", strings.TrimSpace(out))
		}, 2*time.Minute, 3*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		if os.Getenv("KEEP_E2E_RUN") == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", tuiE2ENS,
			"--wait=false", "--ignore-not-found=true"))
		if framework.WaitForNamespaceGone(context.Background(), tuiE2ENS, 90*time.Second) {
			return
		}
		fmt.Fprintf(GinkgoWriter,
			"WARNING: namespace %s stuck in Terminating after 90s — force-clearing finalizers\n",
			tuiE2ENS)
		framework.ForceClearFinalizers(context.Background(), tuiE2ENS)
		framework.WaitForNamespaceGone(context.Background(), tuiE2ENS, 20*time.Second)
	})

	It("TUI broker client drives a Bound interactive run end-to-end", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctx, tuiE2ENS, tuiE2ERun)
			}
			// Best-effort cleanup of the run so AfterAll's namespace
			// delete has one less finaliser to chase.
			delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer delCancel()
			_, _ = utils.Run(exec.CommandContext(delCtx, "kubectl", "-n", tuiE2ENS,
				"delete", "harnessrun", tuiE2ERun, "--ignore-not-found", "--wait=false"))
		})

		By("submitting an Interactive HarnessRun")
		// No interactiveOverrides — the template's 60s maxLifetime is the
		// load-bearing knob for this test (see template comment above).
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: %s
    kind: HarnessTemplate
  workspaceRef: %s
  mode: Interactive
  prompt: "hello-tui-e2e"`, tuiE2ERun, tuiE2ENS, tuiE2ETpl, tuiE2EWorkspace))

		By("waiting for Phase=Running")
		Eventually(func() string { return framework.RunPhase(ctx, tuiE2ENS, tuiE2ERun) },
			3*time.Minute, 2*time.Second).Should(Equal("Running"))

		By("building a rest.Config from the current kubeconfig")
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules,
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		Expect(err).NotTo(HaveOccurred(), "build rest.Config from kubeconfig")

		By("constructing a paddockbroker.Client via New")
		// The broker client opens a SPDY port-forward to the broker Pod
		// and pins the cluster-issued CA. 30s sub-context gives enough
		// time for pod resolution + tunnel ready before giving up.
		bCtx, bCancel := context.WithTimeout(ctx, 30*time.Second)
		bc, err := paddockbroker.New(bCtx, paddockbroker.Options{
			Service:                 "paddock-broker",
			Namespace:               "paddock-system",
			Port:                    8443,
			ServiceAccount:          tuiE2ESA,
			ServiceAccountNamespace: tuiE2ENS,
			Source:                  restCfg,
			CASecretName:            "broker-serving-cert",
			CASecretNamespace:       "paddock-system",
		})
		bCancel()
		Expect(err).NotTo(HaveOccurred(), "paddockbroker.New")
		defer func() { _ = bc.Close() }()

		By("opening the broker stream")
		ch, err := bc.Open(ctx, tuiE2ENS, tuiE2ERun)
		Expect(err).NotTo(HaveOccurred(), "broker.Open")

		By("waiting for at least one StreamFrame within 2 minutes")
		// Per-prompt-process echo adapter emits frames in response to the
		// initial prompt supplied in spec.prompt. The timeout covers both
		// the pod warm-up window and the stream dial+first-frame round-trip.
		select {
		case f, ok := <-ch:
			Expect(ok).To(BeTrue(), "stream channel closed before any frame arrived")
			Expect(f.Type).NotTo(BeEmpty(),
				"received a frame but Type is empty: %+v", f)
			GinkgoWriter.Printf("received StreamFrame type=%q data=%s\n", f.Type, string(f.Data))
		case <-time.After(2 * time.Minute):
			Fail("no StreamFrame arrived within 2 minutes")
		}

		By("ending the run via client.End")
		// Allow up to 30s for the End RPC to reach the broker.
		endCtx, endCancel := context.WithTimeout(ctx, 30*time.Second)
		defer endCancel()
		Expect(bc.End(endCtx, tuiE2ENS, tuiE2ERun, "test-complete")).To(Succeed(), "broker.End")

		By("waiting for the run to reach a terminal phase (Cancelled or Succeeded)")
		// Termination chain for an echo-adapter Interactive run:
		//
		//   bc.End → broker /end (audit + forward) → adapter /end (no-op
		//   stub) → harness keeps sleeping → 60s max-lifetime watchdog
		//   fires → controller deletes Job → run reaches Cancelled.
		//
		// The echo adapter's /end can't kill the harness across PID
		// namespaces, so the watchdog is the actual termination
		// mechanism. The 3-minute deadline gives the watchdog (60s
		// max-lifetime + Job teardown + reconcile) generous head room
		// over the budget interactive_test.go already proves works.
		Eventually(func() string { return framework.RunPhase(ctx, tuiE2ENS, tuiE2ERun) },
			3*time.Minute, 3*time.Second).Should(
			Or(Equal("Cancelled"), Equal("Succeeded")),
			"run did not reach a terminal phase")
	})
})
