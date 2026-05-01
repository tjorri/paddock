//go:build e2e

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

// Package e2e — HOME persistence across Batch runs in the same Workspace.
//
// Verifies that $HOME (= <workspace-mountPath>/.home) is durable across
// consecutive Batch HarnessRuns that share a named Workspace. The lock
// guarantee introduced by the HOME-from-PVC feature (Tasks 1-5 of
// feat/paddock-tui-interactive): whatever a run writes to $HOME must still
// be there when the next run in the same workspace starts.
//
// Test shape:
//  1. Run "write" — a Batch run that writes a sentinel file at $HOME/.persisted.
//  2. Run "read"  — a second Batch run against the same Workspace that reads
//     $HOME/.persisted and surfaces the content in its result summary.
//  3. Assert both runs reach Succeeded, and that the "read" run's output
//     summary contains the sentinel written by the "write" run.
//
// The harness image used is paddock-echo (Alpine + sh). Its entrypoint is
// overridden via spec.command so the shell script can exercise HOME directly.
// The script stays echo-adapter-compatible (writes PADDOCK_RAW_PATH events +
// PADDOCK_RESULT_PATH JSON) so the collector sidecar processes them normally.
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

	"paddock.dev/paddock/test/e2e/framework"
	"paddock.dev/paddock/test/utils"
)

const (
	homePersistNS        = "paddock-home-persist-e2e"
	homePersistTemplate  = "home-persist-echo"
	homePersistWorkspace = "home-persist-ws"
	homePersistRunWrite  = "home-persist-write"
	homePersistRunRead   = "home-persist-read"
	homePersistSentinel  = "paddock-home-e2e-sentinel-v1"
)

var _ = Describe("HOME persistence across Batch runs", Ordered, func() {
	// The suite-level BeforeSuite installs CRDs and deploys the
	// controller-manager; this Describe only manages its own tenant
	// namespace. AfterAll drains tenant state while the controller is
	// still alive (same teardown pattern as e2e_test.go).

	BeforeAll(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		// Clean slate in case a prior interrupted run left debris.
		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", homePersistNS,
			"--ignore-not-found", "--wait=true", "--timeout=60s"))

		mustCreateNamespace(homePersistNS)

		// Namespaced HarnessTemplate. The command overrides paddock-echo's
		// entrypoint with a shell script that reads E2E_HOME_PHASE and
		// performs either a write or a read against $HOME. The script writes
		// echo-adapter-compatible events to PADDOCK_RAW_PATH and a result
		// JSON to PADDOCK_RESULT_PATH so the collector processes them normally.
		//
		// Single-quoted heredoc avoids any variable expansion in the YAML
		// string literal; the variables are evaluated inside the container.
		mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: %s
  namespace: %s
spec:
  harness: echo
  image: %s
  command:
    - /bin/sh
    - -c
    - |
      set -eu
      : "${PADDOCK_RAW_PATH:=/paddock/raw/out}"
      : "${E2E_HOME_PHASE:=write}"
      : "${E2E_SENTINEL:=%s}"
      mkdir -p "$(dirname "$PADDOCK_RAW_PATH")"
      : >"$PADDOCK_RAW_PATH"
      if [ "$E2E_HOME_PHASE" = "write" ]; then
        mkdir -p "$HOME"
        printf '%%s\n' "$E2E_SENTINEL" >"$HOME/.persisted"
        summary="wrote HOME sentinel"
      else
        if [ -f "$HOME/.persisted" ]; then
          content=$(cat "$HOME/.persisted")
          summary="read HOME sentinel: $content"
        else
          summary="HOME_FILE_MISSING"
        fi
      fi
      printf '{"kind":"message","text":"home-persist: %%s"}\n' "$summary" >>"$PADDOCK_RAW_PATH"
      printf '{"kind":"result","summary":"%%s","filesChanged":0}\n' "$summary" >>"$PADDOCK_RAW_PATH"
      if [ -n "${PADDOCK_RESULT_PATH:-}" ]; then
        mkdir -p "$(dirname "$PADDOCK_RESULT_PATH")"
        printf '{"pullRequests":[],"filesChanged":0,"summary":"%%s","artifacts":[]}\n' \
          "$summary" >"$PADDOCK_RESULT_PATH"
      fi
  eventAdapter:
    image: %s
  defaults:
    timeout: 120s
  workspace:
    required: true
    mountPath: /workspace
`, homePersistTemplate, homePersistNS, echoImage, homePersistSentinel, adapterEchoImage))

		// Explicit named Workspace so both runs share the same PVC and
		// therefore the same $HOME directory.
		mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  storage:
    size: 100Mi
`, homePersistWorkspace, homePersistNS))

		// Wait for the Workspace to reach Active before submitting runs.
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", homePersistNS,
				"get", "workspace", homePersistWorkspace,
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
			"delete", "ns", homePersistNS,
			"--wait=false", "--ignore-not-found=true"))

		if framework.WaitForNamespaceGone(context.Background(), homePersistNS, 90*time.Second) {
			return
		}
		fmt.Fprintf(GinkgoWriter,
			"WARNING: namespace %s stuck in Terminating after 90s — force-clearing finalizers\n",
			homePersistNS)
		framework.ForceClearFinalizers(context.Background(), homePersistNS)
		framework.WaitForNamespaceGone(context.Background(), homePersistNS, 20*time.Second)
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dumpRunDiagnostics(ctx, homePersistNS, homePersistRunWrite)
		dumpRunDiagnostics(ctx, homePersistNS, homePersistRunRead)
	})

	It("write run reaches Succeeded and persists $HOME/.persisted", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		By("submitting the write HarnessRun")
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
  workspaceRef: %s
  prompt: "write HOME sentinel"
  extraEnv:
    - name: E2E_HOME_PHASE
      value: write
`, homePersistRunWrite, homePersistNS, homePersistTemplate, homePersistWorkspace))

		By("waiting for the write run to reach Succeeded")
		Eventually(func() string {
			return runPhase(ctx, homePersistNS, homePersistRunWrite)
		}, 2*time.Minute, 2*time.Second).Should(Equal("Succeeded"))
	})

	It("read run reaches Succeeded and recovers the sentinel from $HOME", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		By("submitting the read HarnessRun against the same workspace")
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
  workspaceRef: %s
  prompt: "read HOME sentinel"
  extraEnv:
    - name: E2E_HOME_PHASE
      value: read
`, homePersistRunRead, homePersistNS, homePersistTemplate, homePersistWorkspace))

		By("waiting for the read run to reach Succeeded")
		Eventually(func() string {
			return runPhase(ctx, homePersistNS, homePersistRunRead)
		}, 2*time.Minute, 2*time.Second).Should(Equal("Succeeded"))

		By("verifying the read run's output summary contains the written sentinel")
		// The collector writes the harness's result.json summary into the
		// output ConfigMap (<run>-out) under key "phase"="Completed" and
		// the run's status.outputs.summary field. Poll status.outputs
		// rather than the ConfigMap — same poll we'd use for events.
		var runStatus harnessRunStatus
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", homePersistNS,
				"get", "harnessrun", homePersistRunRead, "-o", "jsonpath={.status}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).NotTo(BeEmpty())
			g.Expect(json.Unmarshal([]byte(out), &runStatus)).To(Succeed())
			g.Expect(runStatus.Outputs).NotTo(BeNil(),
				"status.outputs not yet populated")
			g.Expect(runStatus.Outputs.Summary).To(ContainSubstring(homePersistSentinel),
				"expected sentinel %q in outputs.summary %q",
				homePersistSentinel, runStatus.Outputs.Summary)
		}, 90*time.Second, 2*time.Second).Should(Succeed())
	})
})
