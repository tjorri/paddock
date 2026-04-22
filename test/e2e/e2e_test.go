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
	"encoding/json"
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
	})

	AfterAll(func() {
		// Leave the ns/run behind when KEEP_E2E_RUN=1 so a contributor
		// can poke at the cluster state post-failure.
		if os.Getenv("KEEP_E2E_RUN") == "1" {
			return
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", runNamespace, "--wait=false"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "clusterharnesstemplate", clusterTemplateName, "--ignore-not-found=true"))
		_, _ = utils.Run(exec.Command("make", "undeploy", "ignore-not-found=true"))
		_, _ = utils.Run(exec.Command("make", "uninstall", "ignore-not-found=true"))
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
})

// applyFromYAML pipes the given YAML into `kubectl apply -f -`.
func applyFromYAML(yaml string) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "kubectl apply failed for:\n%s", yaml)
}
