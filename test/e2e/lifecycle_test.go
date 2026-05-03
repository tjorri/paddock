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
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tjorri/paddock/test/e2e/framework"
	"github.com/tjorri/paddock/test/utils"
)

const (
	echoTenantNamespace = "paddock-echo"
	echoTemplateName    = "echo-e2e"
	echoRunName         = "echo-1"
)

var _ = Describe("harness lifecycle", Label("smoke"), func() {
	It("completes a Batch run end-to-end with events and outputs", func(ctx SpecContext) {
		ns := framework.CreateTenantNamespace(ctx, echoTenantNamespace)

		By("applying the echo HarnessTemplate")
		framework.NewHarnessTemplate(ns, echoTemplateName).
			WithImage(echoImage).
			WithCommand("/usr/local/bin/paddock-echo").
			WithRuntime(adapterEchoImage).
			Apply(ctx)

		By("submitting a HarnessRun (ephemeral workspace, inline prompt)")
		run := framework.NewRun(ns, echoTemplateName).
			WithName(echoRunName).
			WithPrompt("hello from paddock e2e").
			Submit(ctx)

		By("waiting for phase=Succeeded")
		run.WaitForPhase(ctx, "Succeeded", 2*time.Minute)
		status := run.Status(ctx)

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
		out, err := utils.Run(exec.Command("kubectl", "-n", ns,
			"get", "cm", echoRunName+"-out", "-o", "jsonpath={.data.phase}"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal("Completed"))

		By("verifying the per-run collector RBAC was provisioned")
		_, err = utils.Run(exec.Command("kubectl", "-n", ns,
			"get", "serviceaccount", echoRunName+"-collector"))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "-n", ns,
			"get", "role", echoRunName+"-collector"))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "-n", ns,
			"get", "rolebinding", echoRunName+"-collector"))
		Expect(err).NotTo(HaveOccurred())

		By("verifying the Pod ran the agent + 2 native sidecars, all exited cleanly")
		podOut, err := utils.Run(exec.Command("kubectl", "-n", ns,
			"get", "pods", "-l", "paddock.dev/run="+echoRunName,
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
