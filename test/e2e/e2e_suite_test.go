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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/utils"
)

// Image tags used by the e2e suite. All are built fresh at the start
// of the run and loaded into the target Kind cluster. The ":dev" tag
// matches the literals hard-coded in config/manager/manager.yaml +
// config/broker/deployment.yaml so `make deploy` wires the cluster
// together without a kustomize image override.
const (
	managerImage         = "paddock-manager:dev"
	echoImage            = "paddock-echo:dev"
	adapterEchoImage     = "paddock-adapter-echo:dev"
	collectorImage       = "paddock-collector:dev"
	brokerImage          = "paddock-broker:dev"
	proxyImage           = "paddock-proxy:dev"
	iptablesInitImage    = "paddock-iptables-init:dev"
	e2eEgressImage       = "paddock-e2e-egress:dev"
	paddockEvilEchoImage = "paddock-evil-echo:dev"
)

var (
	skipCertManagerInstall        = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	isCertManagerAlreadyInstalled = false
)

// TestE2E runs the end-to-end suite. Expects Kind installed and a
// cluster named $KIND_CLUSTER (default "paddock-test-e2e") usable.
// The Makefile's test-e2e target wires setup + teardown.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting paddock e2e suite\n")
	RunSpecs(t, "paddock e2e suite")
}

var _ = BeforeSuite(func() {
	// Builds run sequentially. An earlier attempt to fan out via
	// goroutines was reverted: on a 2-vCPU CI runner, 9 concurrent
	// Docker builds saturate the CPU and disk I/O, making the
	// build phase ~75% slower than sequential (CI BeforeSuite
	// climbed from ~150s to ~261s). The local laptop savings were
	// only ~12s (2-3% of suite wall-clock) — not worth the CI
	// regression. See also: tjorri/paddock PR #34 CI history.
	By("building and loading paddock-manager")
	buildAndLoad(managerImage, []string{"docker-build", fmt.Sprintf("IMG=%s", managerImage)})

	By("building and loading paddock-echo + adapter-echo + collector")
	buildAndLoad(echoImage, []string{"image-echo"})
	buildAndLoad(adapterEchoImage, []string{"image-adapter-echo"})
	buildAndLoad(collectorImage, []string{"image-collector"})

	By("building and loading broker + proxy + iptables-init + e2e-egress (v0.3)")
	buildAndLoad(brokerImage, []string{"image-broker"})
	buildAndLoad(proxyImage, []string{"image-proxy"})
	buildAndLoad(iptablesInitImage, []string{"image-iptables-init"})
	buildAndLoad(e2eEgressImage, []string{"image-e2e-egress"})

	By("building and loading paddock-evil-echo (hostile harness)")
	buildAndLoad(paddockEvilEchoImage, []string{"image-evil-echo"})

	if !skipCertManagerInstall {
		By("checking if cert-manager is already installed")
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed())
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "CertManager already installed; skipping\n")
		}
	}

	// Suite-level controller-manager deploy. Both top-level Ordered
	// Describes share this single install+deploy+rollout, instead of
	// each one tearing down + reinstalling in its BeforeAll. Saves
	// ~3-4 minutes of wall-clock time per suite run.
	//
	// Per-Describe state isolation is via tenant namespaces (each
	// Describe owns its own namespace prefix); cluster-scoped
	// resources are also disjoint by name. Each Describe's AfterAll
	// drains its own state with the controller still alive so
	// finalizers reconcile correctly. Suite teardown (AfterSuite)
	// runs `make undeploy` + `make uninstall` after both Describes
	// are done.
	By("installing CRDs (suite-level)")
	_, err := utils.Run(exec.Command("make", "install"))
	Expect(err).NotTo(HaveOccurred(), "make install")

	By("deploying the controller-manager (suite-level)")
	_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage)))
	Expect(err).NotTo(HaveOccurred(), "make deploy")

	By("waiting for the controller-manager to roll out (suite-level)")
	_, err = utils.Run(exec.Command("kubectl", "-n", "paddock-system",
		"rollout", "status", "deploy/paddock-controller-manager", "--timeout=180s"))
	Expect(err).NotTo(HaveOccurred(), "rollout status")
	// Note: rollout status returning Ready does NOT guarantee the
	// webhook is reachable — the Endpoints object is populated
	// before kube-proxy finishes programming the ClusterIP rules,
	// so the first ~hundreds of milliseconds of "Ready" still fail
	// webhook calls with "connection refused". applyFromYAML in the
	// suite's Describes handles that race with a targeted retry loop.
})

var _ = AfterSuite(func() {
	// Drain every paddock custom resource cluster-wide BEFORE
	// undeploying the controller. Per-Describe AfterAll cleanup is
	// best-effort and has historically missed namespaces (e.g.
	// hostile_test.go's per-spec paddock-hostile-tgXX namespaces use
	// --wait=false). If any CR survives into `make undeploy`, the
	// owning controller is gone before its finalizer can run, the
	// namespace pins in Terminating, and the CRD delete in
	// `make uninstall` blocks indefinitely — hanging AfterSuite past
	// Ginkgo's timeout.
	//
	// Drain-then-undeploy guarantees finalizers reconcile while the
	// controller is alive; `make undeploy` then has nothing left to
	// wait on, and `make uninstall` removes the now-empty CRDs cleanly.
	By("draining paddock CRs cluster-wide before controller teardown")
	drainPaddockResources()

	By("undeploying the controller-manager (suite-level)")
	_, _ = utils.Run(exec.Command("make", "undeploy", "ignore-not-found=true"))

	By("uninstalling CRDs (suite-level)")
	_, _ = utils.Run(exec.Command("make", "uninstall", "ignore-not-found=true"))

	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}
})

// drainPaddockResources deletes every paddock CR cluster-wide with
// --wait so finalizers run while the controller is still alive.
// Idempotent: per-Describe AfterAlls usually cover their own state;
// this is the safety net that catches any namespace they missed.
//
// Order: HarnessRun first (its finalizer drives Workspace
// activeRunRef clearance), then Workspace, then the rest. Other CRs
// have no inter-finalizer dependencies — order among them is for
// reading-order clarity only.
//
// Per-CR --timeout=60s + outer runWithTimeout=90s means a single
// stuck finalizer caps drain cost rather than dragging the whole
// AfterSuite past Ginkgo's deadline. Force-clear fallback fires per
// surviving CR with a loud warning so a regression in a finalizer
// loop can't hide behind a green AfterSuite.
func drainPaddockResources() {
	type drainTarget struct {
		kind       string // plural.fqdn so kubectl resolves unambiguously
		namespaced bool
	}
	targets := []drainTarget{
		{"harnessruns.paddock.dev", true},
		{"workspaces.paddock.dev", true},
		{"brokerpolicies.paddock.dev", true},
		{"harnesstemplates.paddock.dev", true},
		{"auditevents.paddock.dev", true},
		{"clusterharnesstemplates.paddock.dev", false},
	}

	for _, t := range targets {
		args := []string{"delete", t.kind, "--all", "--ignore-not-found=true",
			"--wait=true", "--timeout=60s"}
		if t.namespaced {
			args = append(args, "-A")
		}
		runWithTimeout(90*time.Second, "kubectl", args...)
	}

	// Belt-and-braces: any namespaced CR still present after the
	// targeted delete means its finalizer didn't converge — emit a
	// loud warning and force-clear so AfterSuite still completes
	// (the cluster is about to be torn down anyway).
	forceClearSurvivingPaddockCRs()
}

// forceClearSurvivingPaddockCRs nulls out finalizers on any
// HarnessRun/Workspace that survived the targeted drain. Mirrors
// the per-namespace forceClearFinalizers fallback used by
// e2e_test.go's AfterAll, but cluster-wide and AfterSuite-scoped.
//
// Emits a WARNING on every survivor so a regression in the
// controller's reconcile-delete loop is visible in CI logs even
// when the suite reports green.
func forceClearSurvivingPaddockCRs() {
	for _, kind := range []string{"harnessruns", "workspaces"} {
		out, err := utils.Run(exec.Command("kubectl", "get", kind, "-A",
			"-o", "jsonpath={range .items[*]}{.metadata.namespace} {.metadata.name}{\"\\n\"}{end}",
			"--ignore-not-found"))
		if err != nil || strings.TrimSpace(out) == "" {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			parts := strings.Fields(line)
			if len(parts) != 2 {
				continue
			}
			ns, name := parts[0], parts[1]
			_, _ = fmt.Fprintf(GinkgoWriter,
				"WARNING: %s %s/%s survived AfterSuite drain — force-clearing finalizers; "+
					"investigate the controller's reconcile-delete loop\n", kind, ns, name)
			runWithTimeout(10*time.Second, "kubectl", "-n", ns, "patch", kind, name,
				"--type=merge", "-p", `{"metadata":{"finalizers":null}}`)
		}
	}
}

// buildAndLoad runs `make <targets>` then kind-loads the resulting
// image. Fails the suite on either step so BeforeSuite reports the
// upstream failure cause.
func buildAndLoad(image string, makeTargets []string) {
	cmd := exec.Command("make", makeTargets...)
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "build %s via %v", image, makeTargets)
	ExpectWithOffset(1, utils.LoadImageToKindClusterWithName(image)).To(Succeed(),
		"load %s into Kind", image)
}
