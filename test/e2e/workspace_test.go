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
	// Multi-repo workspace seeding constants
	multiTenantNamespace = "paddock-multi-e2e"
	multiWorkspace       = "multi"
	multiDebugPod        = "multi-debug"
	multiRepoAURL        = "https://github.com/octocat/Hello-World.git"
	multiRepoBURL        = "https://github.com/octocat/Spoon-Knife.git"
	multiRepoAPath       = "hello"
	multiRepoBPath       = "spoon"

	// HOME persistence constants
	homePersistNS        = "paddock-home-persist-e2e"
	homePersistTemplate  = "home-persist-echo"
	homePersistWorkspace = "home-persist-ws"
	homePersistRunWrite  = "home-persist-write"
	homePersistRunRead   = "home-persist-read"
	homePersistSentinel  = "paddock-home-e2e-sentinel-v1"
)

var _ = Describe("workspace seeding", func() {
	It("clones every seed repo into its own subdir and writes the manifest", func(ctx SpecContext) {
		ns := framework.CreateTenantNamespace(ctx, multiTenantNamespace)

		By("creating a Workspace with two public repos")
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  storage:
    size: 100Mi
  seed:
    repos:
      - url: %s
        path: %s
        depth: 1
      - url: %s
        path: %s
        depth: 1
`, multiWorkspace, ns, multiRepoAURL, multiRepoAPath, multiRepoBURL, multiRepoBPath))

		By("waiting for the Workspace to reach phase=Active")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "-n", ns,
				"get", "workspace", multiWorkspace, "-o", "jsonpath={.status.phase}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("Active"),
				"workspace still in phase %q", strings.TrimSpace(out))
		}, 3*time.Minute, 3*time.Second).Should(Succeed())

		By("verifying the seed Job emitted an init container per repo")
		initNames, err := utils.Run(exec.Command("kubectl", "-n", ns,
			"get", "job", multiWorkspace+"-seed",
			"-o", "jsonpath={.spec.template.spec.initContainers[*].name}"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Fields(strings.TrimSpace(initNames))).To(ConsistOf("repo-0", "repo-1"))

		By("launching a debug Pod that mounts the PVC and prints the layout")
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    fsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: inspect
      image: busybox:1.36
      command:
        - sh
        - -c
        - |
          set -eu
          echo '===MANIFEST==='
          cat /workspace/.paddock/repos.json
          echo '===HELLO==='
          ls /workspace/%s
          echo '===SPOON==='
          ls /workspace/%s
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: ws
          mountPath: /workspace
  volumes:
    - name: ws
      persistentVolumeClaim:
        claimName: ws-%s
`, multiDebugPod, ns, multiRepoAPath, multiRepoBPath, multiWorkspace))

		By("waiting for the debug pod to Succeed")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "-n", ns,
				"get", "pod", multiDebugPod, "-o", "jsonpath={.status.phase}"))
			g.Expect(err).NotTo(HaveOccurred())
			phase := strings.TrimSpace(out)
			g.Expect(phase).To(Equal("Succeeded"), "debug pod phase=%q", phase)
		}, 90*time.Second, 2*time.Second).Should(Succeed())

		By("verifying the manifest and directory layout")
		logs, err := utils.Run(exec.Command("kubectl", "-n", ns,
			"logs", multiDebugPod))
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(ContainSubstring("===MANIFEST==="))
		Expect(logs).To(ContainSubstring(`"url": "` + multiRepoAURL + `"`))
		Expect(logs).To(ContainSubstring(`"url": "` + multiRepoBURL + `"`))
		Expect(logs).To(ContainSubstring(`"path": "` + multiRepoAPath + `"`))
		Expect(logs).To(ContainSubstring(`"path": "` + multiRepoBPath + `"`))
		// Hello-World repo contains README; both clones should
		// leave a real working tree with a .git dir.
		Expect(logs).To(ContainSubstring("===HELLO==="))
		Expect(logs).To(ContainSubstring("README"))
		Expect(logs).To(ContainSubstring("===SPOON==="))
	})
})

var _ = Describe("workspace persistence", Ordered, func() {
	// The suite-level BeforeSuite installs CRDs and deploys the
	// controller-manager; this Describe only manages its own tenant
	// namespace. AfterAll drains tenant state while the controller is
	// still alive (same teardown pattern as e2e_test.go).

	BeforeAll(func(ctx SpecContext) {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		// Clean slate in case a prior interrupted run left debris.
		_, _ = utils.Run(exec.CommandContext(cleanCtx, "kubectl",
			"delete", "ns", homePersistNS,
			"--ignore-not-found", "--wait=true", "--timeout=60s"))

		framework.CreateTenantNamespace(ctx, homePersistNS)

		// Namespaced HarnessTemplate. The command overrides paddock-echo's
		// entrypoint with a shell script that reads E2E_HOME_PHASE and
		// performs either a write or a read against $HOME. The script writes
		// echo-adapter-compatible events to PADDOCK_RAW_PATH and a result
		// JSON to PADDOCK_RESULT_PATH so the collector processes them normally.
		//
		// Single-quoted heredoc avoids any variable expansion in the YAML
		// string literal; the variables are evaluated inside the container.
		framework.ApplyYAML(fmt.Sprintf(`
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
		framework.ApplyYAML(fmt.Sprintf(`
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

	It("writes a sentinel into $HOME on a Batch run", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		By("submitting the write HarnessRun")
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
  prompt: "write HOME sentinel"
  extraEnv:
    - name: E2E_HOME_PHASE
      value: write
`, homePersistRunWrite, homePersistNS, homePersistTemplate, homePersistWorkspace))

		By("waiting for the write run to reach Succeeded")
		Eventually(func() string {
			return framework.RunPhase(ctx, homePersistNS, homePersistRunWrite)
		}, 2*time.Minute, 2*time.Second).Should(Equal("Succeeded"))
	})

	It("reads the sentinel back on a subsequent Batch run sharing the Workspace", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		By("submitting the read HarnessRun against the same workspace")
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
  prompt: "read HOME sentinel"
  extraEnv:
    - name: E2E_HOME_PHASE
      value: read
`, homePersistRunRead, homePersistNS, homePersistTemplate, homePersistWorkspace))

		By("waiting for the read run to reach Succeeded")
		Eventually(func() string {
			return framework.RunPhase(ctx, homePersistNS, homePersistRunRead)
		}, 2*time.Minute, 2*time.Second).Should(Equal("Succeeded"))

		By("verifying the read run's output summary contains the written sentinel")
		// The collector writes the harness's result.json summary into the
		// output ConfigMap (<run>-out) under key "phase"="Completed" and
		// the run's status.outputs.summary field. Poll status.outputs
		// rather than the ConfigMap — same poll we'd use for events.
		var runStatus framework.HarnessRunStatus
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
