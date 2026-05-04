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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tjorri/paddock/test/e2e/framework"
	"github.com/tjorri/paddock/test/utils"
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

	// homePersistScriptTemplate is the shell script executed by the home-persist
	// HarnessTemplate. __SENTINEL__ is replaced with homePersistSentinel via
	// strings.Replace before passing to the builder. The %s sequences in the
	// shell printf calls are literal shell directives, not Go format verbs.
	homePersistScriptTemplate = `set -eu
: "${PADDOCK_RAW_PATH:=/paddock/raw/out}"
: "${E2E_HOME_PHASE:=write}"
: "${E2E_SENTINEL:=__SENTINEL__}"
mkdir -p "$(dirname "$PADDOCK_RAW_PATH")"
: >"$PADDOCK_RAW_PATH"
if [ "$E2E_HOME_PHASE" = "write" ]; then
  mkdir -p "$HOME"
  printf '%s\n' "$E2E_SENTINEL" >"$HOME/.persisted"
  summary="wrote HOME sentinel"
else
  if [ -f "$HOME/.persisted" ]; then
    content=$(cat "$HOME/.persisted")
    summary="read HOME sentinel: $content"
  else
    summary="HOME_FILE_MISSING"
  fi
fi
printf '{"kind":"message","text":"home-persist: %s"}\n' "$summary" >>"$PADDOCK_RAW_PATH"
printf '{"kind":"result","summary":"%s","filesChanged":0}\n' "$summary" >>"$PADDOCK_RAW_PATH"
if [ -n "${PADDOCK_RESULT_PATH:-}" ]; then
  mkdir -p "$(dirname "$PADDOCK_RESULT_PATH")"
  printf '{"pullRequests":[],"filesChanged":0,"summary":"%s","artifacts":[]}\n' \
    "$summary" >"$PADDOCK_RESULT_PATH"
fi`
)

var _ = Describe("workspace seeding", Label("smoke"), func() {
	It("clones every seed repo into its own subdir and writes the manifest", func(ctx SpecContext) {
		ns := framework.CreateTenantNamespace(ctx, multiTenantNamespace)

		By("creating a Workspace with two public repos")
		ws := framework.NewWorkspace(ns, multiWorkspace).
			WithStorage("100Mi").
			WithSeedRepo(multiRepoAURL, multiRepoAPath, 1).
			WithSeedRepo(multiRepoBURL, multiRepoBPath, 1).
			Apply(ctx)

		By("waiting for the Workspace to reach phase=Active")
		ws.WaitForActive(ctx, 3*time.Minute)

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

	// Per-process suffixed namespace; captured from CreateTenantNamespace
	// so under -p the spec body uses the same name BeforeAll created.
	var ns string

	BeforeAll(func(ctx SpecContext) {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		// Clean slate in case a prior interrupted run left debris.
		base := framework.TenantNamespace(homePersistNS)
		_, _ = utils.Run(exec.CommandContext(cleanCtx, "kubectl",
			"delete", "ns", base,
			"--ignore-not-found", "--wait=true", "--timeout=60s"))

		ns = framework.CreateTenantNamespace(ctx, homePersistNS)

		// Namespaced HarnessTemplate. The command overrides paddock-echo's
		// entrypoint with a shell script that reads E2E_HOME_PHASE and
		// performs either a write or a read against $HOME. The script writes
		// echo-runtime-compatible events to PADDOCK_RAW_PATH and a result
		// JSON to PADDOCK_RESULT_PATH so the runtime processes them normally.
		script := strings.Replace(homePersistScriptTemplate, "__SENTINEL__", homePersistSentinel, 1)
		framework.NewHarnessTemplate(ns, homePersistTemplate).
			WithImage(echoImage).
			WithCommand("/bin/sh", "-c", script).
			WithRuntime(runtimeEchoImage).
			WithDefaultTimeout("120s").
			Apply(ctx)

		// Explicit named Workspace so both runs share the same PVC and
		// therefore the same $HOME directory.
		ws := framework.NewWorkspace(ns, homePersistWorkspace).
			WithStorage("100Mi").
			Apply(ctx)

		// Wait for the Workspace to reach Active before submitting runs.
		ws.WaitForActive(ctx, 2*time.Minute)
	})

	AfterAll(func() {
		if os.Getenv("KEEP_E2E_RUN") == "1" {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", ns,
			"--wait=false", "--ignore-not-found=true"))

		if framework.WaitForNamespaceGone(context.Background(), ns, 90*time.Second) {
			return
		}
		fmt.Fprintf(GinkgoWriter,
			"WARNING: namespace %s stuck in Terminating after 90s — force-clearing finalizers\n",
			ns)
		framework.ForceClearFinalizers(context.Background(), ns)
		framework.WaitForNamespaceGone(context.Background(), ns, 20*time.Second)
	})

	It("writes a sentinel into $HOME on a Batch run", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		By("submitting the write HarnessRun")
		run := framework.NewRun(ns, homePersistTemplate).
			WithName(homePersistRunWrite).
			WithWorkspace(homePersistWorkspace).
			WithPrompt("write HOME sentinel").
			WithEnv("E2E_HOME_PHASE", "write").
			Submit(ctx)

		By("waiting for the write run to reach Succeeded")
		run.WaitForPhase(ctx, "Succeeded", 2*time.Minute)
	})

	It("reads the sentinel back on a subsequent Batch run sharing the Workspace", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		By("submitting the read HarnessRun against the same workspace")
		run := framework.NewRun(ns, homePersistTemplate).
			WithName(homePersistRunRead).
			WithWorkspace(homePersistWorkspace).
			WithPrompt("read HOME sentinel").
			WithEnv("E2E_HOME_PHASE", "read").
			Submit(ctx)

		By("waiting for the read run to reach Succeeded")
		run.WaitForPhase(ctx, "Succeeded", 2*time.Minute)

		By("verifying the read run's output summary contains the written sentinel")
		// The runtime writes the harness's result.json summary into the
		// output ConfigMap (<run>-out) under key "phase"="Completed" and
		// the run's status.outputs.summary field. Poll status.outputs
		// rather than the ConfigMap — same poll we'd use for events.
		Eventually(func(g Gomega) {
			runStatus := run.Status(ctx)
			g.Expect(runStatus.Outputs).NotTo(BeNil(),
				"status.outputs not yet populated")
			g.Expect(runStatus.Outputs.Summary).To(ContainSubstring(homePersistSentinel),
				"expected sentinel %q in outputs.summary %q",
				homePersistSentinel, runStatus.Outputs.Summary)
		}, 90*time.Second, 2*time.Second).Should(Succeed())
	})
})
