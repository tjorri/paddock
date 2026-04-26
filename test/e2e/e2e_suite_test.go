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
	"sync"
	"testing"

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
	By("building and loading all images in parallel")
	// Build + kind-load all 9 images concurrently. Each `make image-*`
	// is independent (separate Dockerfile, no shared writable state);
	// Docker serializes builds around its daemon but interleaves
	// layer caches across them. `kind load docker-image` self-locks
	// against the kind CLI so the load step naturally serializes
	// without explicit coordination. Wall-clock saving on a warm
	// cache: ~30-50% of the build phase vs sequential.
	buildAndLoadAll([]buildJob{
		{managerImage, []string{"docker-build", fmt.Sprintf("IMG=%s", managerImage)}},
		{echoImage, []string{"image-echo"}},
		{adapterEchoImage, []string{"image-adapter-echo"}},
		{collectorImage, []string{"image-collector"}},
		{brokerImage, []string{"image-broker"}},
		{proxyImage, []string{"image-proxy"}},
		{iptablesInitImage, []string{"image-iptables-init"}},
		{e2eEgressImage, []string{"image-e2e-egress"}},
		{paddockEvilEchoImage, []string{"image-evil-echo"}},
	})

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
	// Suite-level controller-manager teardown. Runs after both
	// Describe AfterAlls have drained their tenant state, so the
	// controller is still alive while finalizers run and only this
	// AfterSuite removes the controller itself.
	By("undeploying the controller-manager (suite-level)")
	_, _ = utils.Run(exec.Command("make", "undeploy", "ignore-not-found=true"))

	By("uninstalling CRDs (suite-level)")
	_, _ = utils.Run(exec.Command("make", "uninstall", "ignore-not-found=true"))

	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}
})

// buildJob describes a single image build + kind-load.
type buildJob struct {
	image       string
	makeTargets []string
}

// buildAndLoadE runs `make <targets>` then kind-loads the resulting
// image, returning any error rather than asserting via Gomega. Used by
// buildAndLoadAll's goroutine fan-out where Gomega expectations would
// not be safely captured outside the test goroutine.
func buildAndLoadE(image string, makeTargets []string) error {
	cmd := exec.Command("make", makeTargets...)
	if _, err := utils.Run(cmd); err != nil {
		return fmt.Errorf("build %s via %v: %w", image, makeTargets, err)
	}
	if err := utils.LoadImageToKindClusterWithName(image); err != nil {
		return fmt.Errorf("load %s into Kind: %w", image, err)
	}
	return nil
}

// buildAndLoadAll runs every job's build + kind-load concurrently and
// aggregates errors. Fails the suite if any job errors.
func buildAndLoadAll(jobs []buildJob) {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	wg.Add(len(jobs))
	for _, j := range jobs {
		go func(j buildJob) {
			defer wg.Done()
			if err := buildAndLoadE(j.image, j.makeTargets); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(j)
	}
	wg.Wait()
	for _, err := range errs {
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
	}
}
