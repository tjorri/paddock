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
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/e2e/framework"
	"paddock.dev/paddock/test/utils"
)

const (
	admissionGitSchemeNS = "paddock-admission-git-scheme"
	admissionPolicyRejNS = "paddock-admission-policy-rejected"
)

var _ = Describe("admission webhook", func() {
	It("rejects a Workspace seed with an unsupported URL scheme", func(ctx SpecContext) {
		ns := framework.CreateTenantNamespace(ctx, admissionGitSchemeNS)
		By("attempting to create a Workspace whose seed repo URL uses git://")
		yaml := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: ws-bad-scheme
  namespace: %s
spec:
  storage:
    size: 100Mi
  seed:
    repos:
      - url: git://github.com/foo/bar.git
        path: foo
        depth: 1
`, ns)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(yaml)
		out, err := cmd.CombinedOutput()
		Expect(err).To(HaveOccurred(), "expected admission to reject git:// URL")
		Expect(string(out)).To(ContainSubstring("https:// or ssh://"),
			"webhook error message should name the allowlist; got: %s", out)
	})

	It("emits a policy-rejected AuditEvent on rejected admission", func(ctx SpecContext) {
		ns := framework.CreateTenantNamespace(ctx, admissionPolicyRejNS)

		tctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		invalidName := "f32-invalid-spec"
		By("submitting a HarnessRun with an invalid spec (no prompt or promptFrom)")
		invalidManifest := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: any
`, invalidName, ns)

		cmd := exec.CommandContext(tctx, "kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(invalidManifest)
		out, err := utils.Run(cmd)
		Expect(err).To(HaveOccurred(),
			"admission must reject HarnessRun without prompt/promptFrom; got: %s", out)
		Expect(out).To(ContainSubstring("prompt"),
			"rejection diagnostic must mention the missing prompt field; got: %s", out)

		By("asserting a policy-rejected AuditEvent landed in the namespace")
		Eventually(func() int {
			out, _ := utils.Run(exec.CommandContext(tctx, "kubectl", "-n", ns,
				"get", "auditevents",
				"-l", "paddock.dev/kind=policy-rejected,paddock.dev/run="+invalidName,
				"--no-headers",
				"-o", "name"))
			return strings.Count(out, "auditevent")
		}, 30*time.Second, 2*time.Second).Should(BeNumerically(">=", 1),
			"expected >=1 policy-rejected AuditEvent for the invalid HarnessRun")

		By("verifying the AuditEvent's spec.decision is denied")
		out, err = utils.Run(exec.CommandContext(tctx, "kubectl", "-n", ns,
			"get", "auditevents",
			"-l", "paddock.dev/kind=policy-rejected,paddock.dev/run="+invalidName,
			"-o", "jsonpath={.items[0].spec.decision}"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal("denied"))
	})
})
