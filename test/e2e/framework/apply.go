//go:build e2e
// +build e2e

package framework

import (
	"fmt"
	"os"
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

// ApplyManifestFile applies the YAML file at path using `kubectl apply
// --server-side --force-conflicts -f <path>`. Server-side apply makes
// concurrent applies of the same file from parallel workers (-p) merge
// cleanly instead of racing on the client-side apply's annotation-based
// reconciliation. The command runs from the project root (so relative
// paths such as "config/samples/…" resolve correctly). Fails the spec
// on error.
func ApplyManifestFile(path string) {
	ginkgo.GinkgoHelper()
	cmd := exec.Command("kubectl", "apply", "--server-side", "--force-conflicts", "-f", path)
	cmd.Dir = projectDir()
	out, err := cmd.CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"kubectl apply -f %s: %s", path, out)
}

// ApplyManifestFileToNamespace applies the YAML file at path into the given
// namespace using `kubectl -n <ns> apply --server-side --force-conflicts -f
// <path>`. Same parallelism rationale as ApplyManifestFile. Runs from the
// project root. Fails the spec on error.
func ApplyManifestFileToNamespace(path, ns string) {
	ginkgo.GinkgoHelper()
	cmd := exec.Command("kubectl", "-n", ns, "apply", "--server-side", "--force-conflicts", "-f", path)
	cmd.Dir = projectDir()
	out, err := cmd.CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"kubectl -n %s apply -f %s: %s", ns, path, out)
}

// projectDir returns the repository root so file-path based kubectl commands
// resolve relative paths correctly regardless of which directory the test
// binary was launched from.
func projectDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return strings.ReplaceAll(wd, "/test/e2e", "")
}
