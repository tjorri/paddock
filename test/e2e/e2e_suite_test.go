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
})

var _ = AfterSuite(func() {
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}
})

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
