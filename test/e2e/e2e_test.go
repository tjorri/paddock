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
	controlPlaneNamespace = "paddock-system"
	runNamespace          = "paddock-e2e"
	clusterTemplateName   = "echo-e2e"
	runName               = "echo-1"

	// Multi-repo seeding test. Canonical tiny public repos that have
	// been stable for a decade — shallow clones take <1s. Pinned paths
	// give the assertions something concrete to check.
	multiNamespace = "paddock-multi-e2e"
	multiWorkspace = "multi"
	multiDebugPod  = "multi-debug"
	multiRepoAURL  = "https://github.com/octocat/Hello-World.git"
	multiRepoBURL  = "https://github.com/octocat/Spoon-Knife.git"
	multiRepoAPath = "hello"
	multiRepoBPath = "spoon"
)

// paddockEvent mirrors the serialised PaddockEvent — the e2e package
// stays decoupled from the api module's typed client to keep the
// build surface small.
type paddockEvent struct {
	SchemaVersion string            `json:"schemaVersion"`
	Timestamp     string            `json:"ts"`
	Type          string            `json:"type"`
	Summary       string            `json:"summary,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
}

type harnessRunStatus struct {
	Phase        string         `json:"phase"`
	JobName      string         `json:"jobName"`
	WorkspaceRef string         `json:"workspaceRef"`
	RecentEvents []paddockEvent `json:"recentEvents"`
	Outputs      *struct {
		Summary      string `json:"summary"`
		FilesChanged int    `json:"filesChanged"`
	} `json:"outputs"`
}

var _ = Describe("paddock v0.1 pipeline", Ordered, func() {
	BeforeAll(func() {
		By("installing CRDs")
		_, err := utils.Run(exec.Command("make", "install"))
		Expect(err).NotTo(HaveOccurred())

		By("deploying the controller-manager")
		_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage)))
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the controller-manager to roll out")
		_, err = utils.Run(exec.Command("kubectl", "-n", controlPlaneNamespace,
			"rollout", "status", "deploy/paddock-controller-manager", "--timeout=180s"))
		Expect(err).NotTo(HaveOccurred())
		// Note: rollout status returning Ready does NOT guarantee the
		// webhook is reachable — the Endpoints object is populated
		// before kube-proxy finishes programming the ClusterIP rules,
		// so the first ~hundreds of milliseconds of "Ready" still fail
		// webhook calls with "connection refused". applyFromYAML below
		// handles that race with a targeted retry loop.
	})

	AfterAll(func() {
		// Leave the ns/run behind when KEEP_E2E_RUN=1 so a contributor
		// can poke at the cluster state post-failure.
		if os.Getenv("KEEP_E2E_RUN") == "1" {
			return
		}
		// Teardown is best-effort: kind delete cluster obliterates
		// everything anyway. Wrap each step in a bounded context so a
		// hung `kubectl delete` (e.g. a finalizer that won't clear)
		// can't eat the 10-minute ginkgo suite budget and hide the
		// preceding pass/fail result.
		runWithTimeout(10*time.Second, "kubectl", "delete", "ns", runNamespace, "--wait=false")
		runWithTimeout(10*time.Second, "kubectl", "delete", "ns", multiNamespace, "--wait=false")
		runWithTimeout(10*time.Second, "kubectl", "delete", "clusterharnesstemplate", clusterTemplateName, "--ignore-not-found=true")
		runWithTimeout(90*time.Second, "make", "undeploy", "ignore-not-found=true")
		runWithTimeout(90*time.Second, "make", "uninstall", "ignore-not-found=true")
	})

	AfterEach(func() {
		spec := CurrentSpecReport()
		if !spec.Failed() {
			return
		}
		// Collect evidence for post-mortem — keep the output tight so
		// CI logs stay readable when something breaks.
		By("dumping controller-manager logs")
		if logs, err := utils.Run(exec.Command("kubectl", "-n", controlPlaneNamespace,
			"logs", "-l", "control-plane=controller-manager", "--tail=200")); err == nil {
			fmt.Fprintln(GinkgoWriter, "--- controller logs ---\n"+logs)
		}
		By("dumping run namespace events")
		if evts, err := utils.Run(exec.Command("kubectl", "-n", runNamespace,
			"get", "events", "--sort-by=.lastTimestamp")); err == nil {
			fmt.Fprintln(GinkgoWriter, "--- events ---\n"+evts)
		}
		By("dumping run pods")
		if pods, err := utils.Run(exec.Command("kubectl", "-n", runNamespace, "get", "pods", "-o", "wide")); err == nil {
			fmt.Fprintln(GinkgoWriter, "--- pods ---\n"+pods)
		}
	})

	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	Context("echo harness", func() {
		It("drives a HarnessRun to Succeeded with events and outputs populated", func() {
			By("creating the run namespace")
			_, err := utils.Run(exec.Command("kubectl", "create", "ns", runNamespace))
			Expect(err).NotTo(HaveOccurred())

			By("applying the echo ClusterHarnessTemplate")
			applyFromYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata:
  name: %s
spec:
  harness: echo
  image: %s
  command: ["/usr/local/bin/paddock-echo"]
  eventAdapter:
    image: %s
  defaults:
    timeout: 60s
  workspace:
    required: true
    mountPath: /workspace
`, clusterTemplateName, echoImage, adapterEchoImage))

			By("submitting a HarnessRun (ephemeral workspace, inline prompt)")
			applyFromYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: %s
    kind: ClusterHarnessTemplate
  prompt: "hello from paddock e2e"
`, runName, runNamespace, clusterTemplateName))

			By("waiting for phase=Succeeded")
			var status harnessRunStatus
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "-n", runNamespace,
					"get", "harnessrun", runName, "-o", "jsonpath={.status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(BeEmpty())
				g.Expect(json.Unmarshal([]byte(out), &status)).To(Succeed())
				g.Expect(status.Phase).To(Equal("Succeeded"),
					"run still in phase %q", status.Phase)
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("verifying status.recentEvents came through the adapter + collector")
			Expect(status.RecentEvents).To(HaveLen(4),
				"expected the 4 deterministic echo events; got %+v", status.RecentEvents)
			types := make([]string, len(status.RecentEvents))
			for i, ev := range status.RecentEvents {
				types[i] = ev.Type
				Expect(ev.SchemaVersion).To(Equal("1"), "event[%d] schemaVersion", i)
				Expect(ev.Timestamp).NotTo(BeEmpty(), "event[%d] timestamp", i)
			}
			Expect(types).To(Equal([]string{"Message", "ToolUse", "Message", "Result"}))
			Expect(status.RecentEvents[2].Summary).To(ContainSubstring("hello from paddock e2e"),
				"the echoed prompt should appear in the 3rd event summary")

			By("verifying status.outputs.summary came from result.json")
			Expect(status.Outputs).NotTo(BeNil())
			Expect(status.Outputs.Summary).To(ContainSubstring("echoed"))

			By("verifying the output ConfigMap reached phase=Completed")
			out, err := utils.Run(exec.Command("kubectl", "-n", runNamespace,
				"get", "cm", runName+"-out", "-o", "jsonpath={.data.phase}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(Equal("Completed"))

			By("verifying the per-run collector RBAC was provisioned")
			_, err = utils.Run(exec.Command("kubectl", "-n", runNamespace,
				"get", "serviceaccount", runName+"-collector"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "-n", runNamespace,
				"get", "role", runName+"-collector"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "-n", runNamespace,
				"get", "rolebinding", runName+"-collector"))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Pod ran the agent + 2 native sidecars, all exited cleanly")
			podOut, err := utils.Run(exec.Command("kubectl", "-n", runNamespace,
				"get", "pods", "-l", "paddock.dev/run="+runName,
				"-o", "jsonpath={.items[0].status.containerStatuses[*].state.terminated.exitCode}"+
					";{.items[0].status.initContainerStatuses[*].state.terminated.exitCode}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(podOut).To(ContainSubstring("0"),
				"at least one terminated container; got %q", podOut)
			// Every exit code should be 0 (space or semicolon separated).
			for _, code := range strings.FieldsFunc(podOut, func(r rune) bool {
				return r == ' ' || r == ';'
			}) {
				Expect(code).To(Equal("0"),
					"non-zero container exit code: %q", podOut)
			}
		})
	})

	Context("multi-repo workspace seeding", func() {
		It("clones every repo to its own subdir and writes /workspace/.paddock/repos.json", func() {
			By("creating the multi-repo namespace")
			_, err := utils.Run(exec.Command("kubectl", "create", "ns", multiNamespace))
			Expect(err).NotTo(HaveOccurred())

			By("creating a Workspace with two public repos")
			applyFromYAML(fmt.Sprintf(`
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
`, multiWorkspace, multiNamespace, multiRepoAURL, multiRepoAPath, multiRepoBURL, multiRepoBPath))

			By("waiting for the Workspace to reach phase=Active")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "-n", multiNamespace,
					"get", "workspace", multiWorkspace, "-o", "jsonpath={.status.phase}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("Active"),
					"workspace still in phase %q", strings.TrimSpace(out))
			}, 3*time.Minute, 3*time.Second).Should(Succeed())

			By("verifying the seed Job emitted an init container per repo")
			initNames, err := utils.Run(exec.Command("kubectl", "-n", multiNamespace,
				"get", "job", multiWorkspace+"-seed",
				"-o", "jsonpath={.spec.template.spec.initContainers[*].name}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(strings.TrimSpace(initNames))).To(ConsistOf("repo-0", "repo-1"))

			By("launching a debug Pod that mounts the PVC and prints the layout")
			applyFromYAML(fmt.Sprintf(`
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
`, multiDebugPod, multiNamespace, multiRepoAPath, multiRepoBPath, multiWorkspace))

			By("waiting for the debug pod to Succeed")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "-n", multiNamespace,
					"get", "pod", multiDebugPod, "-o", "jsonpath={.status.phase}"))
				g.Expect(err).NotTo(HaveOccurred())
				phase := strings.TrimSpace(out)
				g.Expect(phase).To(Equal("Succeeded"), "debug pod phase=%q", phase)
			}, 90*time.Second, 2*time.Second).Should(Succeed())

			By("verifying the manifest and directory layout")
			logs, err := utils.Run(exec.Command("kubectl", "-n", multiNamespace,
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
})

// applyFromYAML pipes the given YAML into `kubectl apply -f -`,
// retrying on transient webhook/apiserver errors for up to applyRetryBudget.
//
// Why retry: a freshly-rolled controller races with kube-proxy's
// Service→Pod rule programming. The Deployment's rollout-status
// returns Ready and the Endpoints object gets populated before
// kube-proxy has actually programmed the ClusterIP forwarding, which
// opens a ~hundreds-of-milliseconds window where the apiserver's
// webhook call to paddock-webhook-service:443 gets "connection
// refused". Retries close the window without swallowing real
// validation failures — see isRetriableApplyErr.
func applyFromYAML(yaml string) {
	const applyRetryBudget = 30 * time.Second
	deadline := time.Now().Add(applyRetryBudget)
	var lastErr error
	for {
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(yaml)
		_, err := utils.Run(cmd)
		if err == nil {
			return
		}
		lastErr = err
		if !isRetriableApplyErr(err) || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	ExpectWithOffset(1, lastErr).NotTo(HaveOccurred(), "kubectl apply failed for:\n%s", yaml)
}

// isRetriableApplyErr returns true only for transient webhook/apiserver
// conditions that typically resolve within a few seconds of a fresh
// deploy. Deliberately does NOT match the generic "Internal error
// occurred: failed calling webhook" prefix, which also fires for
// permanent failures (cert issues, webhook-returned errors) that
// retries can't fix.
func isRetriableApplyErr(err error) bool {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return true
	case strings.Contains(msg, "no endpoints available for service"):
		return true
	case strings.Contains(msg, "context deadline exceeded"):
		return true
	}
	return false
}

// runWithTimeout runs a command via utils.Run under a context with
// the given deadline. On timeout, SIGKILL is sent to the process and
// a one-line note is written to GinkgoWriter. Output is discarded —
// callers who need it should use utils.Run directly. Intended for
// best-effort teardown steps that must not block.
func runWithTimeout(timeout time.Duration, name string, args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := utils.Run(exec.CommandContext(ctx, name, args...))
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(GinkgoWriter, "teardown timed out after %s: %s %s\n",
			timeout, name, strings.Join(args, " "))
	}
}
