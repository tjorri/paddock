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
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/e2e/framework"
	"paddock.dev/paddock/test/utils"
)

// Issue #79 regression: prior to the fix, HarnessRuns whose template
// has non-empty requires emitted per-run policies that Cilium silently
// ignored on two paths — (A) the apiserver allow used an ipBlock
// against the kubernetes Service ClusterIP, but Cilium classifies
// host-network destinations as "kube-apiserver" / "remote-node"
// entities not as CIDRs, so the rule didn't match; (B) the iptables
// transparent-redirect path sends agent traffic to 127.0.0.1, but no
// loopback toCIDR rule was emitted, so Cilium dropped it.
//
// Every existing e2e spec uses templates with empty requires (which
// short-circuits per-run policy emission), so the regression class was
// uncovered. This Describe submits a run against a template with
// requires.egress + requires.credentials, then asserts the per-run
// CiliumNetworkPolicy carries both fixes.
//
// Skips cleanly when the target cluster has no Cilium installation
// (e.g. a stock kindnet cluster), so `go test -run` outside the
// paddock-test-e2e cluster doesn't false-fail.
var _ = Describe("cilium-aware network policy", Ordered, func() {
	const (
		ns               = "cilium-compat-e2e"
		runName          = "compat-demo"
		templateName     = "cilium-compat-echo"
		brokerPolicyName = "cilium-compat-allow"
		credSecretName   = "cilium-compat-cred"
	)

	BeforeAll(func() {
		// Skip if Cilium isn't installed. The cilium-config ConfigMap
		// is created by every Cilium install (helm chart, cilium-cli,
		// the kind-up.sh path) and its absence is a reliable signal
		// that this cluster runs a non-Cilium CNI.
		out, err := utils.Run(exec.Command("kubectl", "-n", "kube-system",
			"get", "configmap", "cilium-config", "-o", "name"))
		if err != nil || !strings.Contains(out, "cilium-config") {
			Skip("cilium_compat: cluster has no Cilium installation; skipping " +
				"(run on a Cilium-enabled Kind cluster, e.g. via make setup-test-e2e)")
		}

		By("creating the cilium-compat namespace")
		_, err = utils.Run(exec.Command("kubectl", "create", "ns", ns))
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		// Best-effort teardown — the suite-level AfterSuite drains any
		// surviving paddock CRs cluster-wide before `make undeploy`.
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", ns, "--wait=false"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "clusterharnesstemplate",
			templateName, "--ignore-not-found"))
	})

	It("emits a CiliumNetworkPolicy with loopback-allow and toEntities for the apiserver", func(ctx SpecContext) {
		By("creating the credential Secret")
		_, err := utils.Run(exec.Command("kubectl", "-n", ns,
			"create", "secret", "generic", credSecretName,
			"--from-literal=token=test-token-not-real"))
		Expect(err).NotTo(HaveOccurred())

		By("registering a ClusterHarnessTemplate with non-empty requires")
		// Echo agent never makes outbound calls — declaring
		// requires.egress (example.com:443) just gives the controller
		// something to render into the per-run policy without taking a
		// dependency on a real public destination.
		// ClusterHarnessTemplate + requires.egress are not expressible
		// via TemplateBuilder; keep raw YAML.
		framework.ApplyYAML(fmt.Sprintf(`
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
  requires:
    credentials:
      - name: TEST_TOKEN
    egress:
      - host: example.com
        ports: [443]
  defaults:
    timeout: 60s
  workspace:
    required: true
    mountPath: /workspace
`, templateName, echoImage, adapterEchoImage))

		By("applying a BrokerPolicy granting the credential + egress")
		// BrokerPolicy egress grants are not expressible via PolicyBuilder;
		// keep raw YAML.
		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: %s
  namespace: %s
spec:
  appliesToTemplates: ["%s"]
  grants:
    credentials:
      - name: TEST_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: %s
            key: token
          deliveryMode:
            inContainer:
              accepted: true
              reason: "E2E cilium-compat regression exercises a raw credential plumbed into the run container."
    egress:
      - host: example.com
        ports: [443]
`, brokerPolicyName, ns, templateName, credSecretName))

		By("submitting the HarnessRun")
		run := framework.NewRun(ns, templateName).
			WithName(runName).
			WithPrompt("hello cilium-compat e2e").
			WithClusterScopedTemplate().
			Submit(ctx)

		By("waiting for phase=Succeeded")
		run.WaitForPhase(ctx, "Succeeded", 3*time.Minute)

		By("asserting the per-run CiliumNetworkPolicy was emitted")
		// CNP CRDs are present on the test cluster (BeforeAll skipped
		// otherwise) so the controller's CNP path is taken.
		cnpYAML, err := utils.Run(exec.Command("kubectl", "-n", ns,
			"get", "ciliumnetworkpolicy", runName+"-egress", "-o", "yaml"))
		Expect(err).NotTo(HaveOccurred(),
			"expected ciliumnetworkpolicy/%s-egress to exist", runName)

		// Covers Issue #79 B-FIX: iptables-redirected agent traffic to loopback
		// must remain allowed by the per-run CiliumNetworkPolicy.
		Expect(cnpYAML).To(ContainSubstring("127.0.0.0/8"),
			"CNP must include loopback toCIDR rule (Issue #79 B-FIX)")

		// Covers Issue #79 A-FIX: avoid ipBlock against host-network apiserver.
		Expect(cnpYAML).To(ContainSubstring("kube-apiserver"),
			"CNP must include toEntities: kube-apiserver (Issue #79 A-FIX)")
		Expect(cnpYAML).To(ContainSubstring("remote-node"),
			"CNP must include toEntities: remote-node (Issue #79 A-FIX defensive)")

		By("asserting the standard NetworkPolicy is NOT emitted on Cilium clusters")
		// On a Cilium cluster the controller takes the CNP path
		// exclusively — the standard NetworkPolicy must not also be
		// rendered, otherwise its stricter ipBlock rules would silently
		// re-introduce the regression alongside the CNP.
		out, _ := utils.Run(exec.Command("kubectl", "-n", ns,
			"get", "networkpolicy", runName+"-egress",
			"-o", "name", "--ignore-not-found"))
		Expect(strings.TrimSpace(out)).To(BeEmpty(),
			"standard NetworkPolicy should not be emitted on Cilium clusters; got %q", out)
	})
})
