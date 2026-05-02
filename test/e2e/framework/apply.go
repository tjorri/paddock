//go:build e2e
// +build e2e

package framework

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

// ApplyYAML feeds a YAML manifest to `kubectl apply -f -`. Retries
// for up to 30 s on the documented webhook-readiness race: the
// controller's rollout-status returns Ready before kube-proxy
// finishes programming the ClusterIP rules for the webhook
// Endpoints, so the first ~hundreds of ms of "Ready" still fail
// admission with "connection refused" / "no endpoints available".
func ApplyYAML(yaml string) {
	ginkgo.GinkgoHelper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(yaml)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		if !isRetriableApplyErr(string(out)) || time.Now().After(deadline) {
			gomega.Expect(err).NotTo(gomega.HaveOccurred(),
				"kubectl apply failed: %s\nyaml:\n%s", out, yaml)
			return
		}
		_, _ = fmt.Fprintf(ginkgo.GinkgoWriter,
			"kubectl apply transient error, retrying: %s\n", strings.TrimSpace(string(out)))
		time.Sleep(2 * time.Second)
	}
}

// ApplyYAMLToNamespace applies a YAML manifest with `-n <ns>`. Same
// retry semantics as ApplyYAML.
func ApplyYAMLToNamespace(yaml, ns string) {
	ginkgo.GinkgoHelper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		cmd := exec.Command("kubectl", "-n", ns, "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(yaml)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		if !isRetriableApplyErr(string(out)) || time.Now().After(deadline) {
			gomega.Expect(err).NotTo(gomega.HaveOccurred(),
				"kubectl -n %s apply failed: %s\nyaml:\n%s", ns, out, yaml)
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func isRetriableApplyErr(output string) bool {
	o := strings.ToLower(output)
	return strings.Contains(o, "connection refused") ||
		strings.Contains(o, "no endpoints available") ||
		strings.Contains(o, "context deadline exceeded") ||
		strings.Contains(o, "failed to call webhook")
}
