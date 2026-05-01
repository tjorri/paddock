//go:build e2e
// +build e2e

package framework

import (
	"context"
	"strings"

	"github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

// CreateBrokerToken mints a 10-minute SA token bound to the broker
// audience for the named ServiceAccount. Used by interactive specs
// that hit the broker API directly via Broker.PortForward.
func CreateBrokerToken(ctx context.Context, ns, sa string) string {
	ginkgo.GinkgoHelper()
	out, err := RunCmd(ctx, "kubectl", "-n", ns, "create", "token", sa,
		"--audience=paddock-broker", "--duration=10m")
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "create token: %s", out)
	return strings.TrimSpace(out)
}
