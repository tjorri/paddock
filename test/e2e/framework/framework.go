//go:build e2e
// +build e2e

// Package framework provides shared helpers for paddock's end-to-end
// test suite. It wraps kubectl, cert-manager, broker port-forwarding,
// HarnessRun lifecycle, and diagnostic dumps so spec bodies can stay
// focused on assertions rather than orchestration.
//
// All exported symbols are safe for concurrent use across Ginkgo
// parallel processes (`-p`) unless explicitly documented otherwise.
package framework

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
)

// GinkgoProcessSuffix returns the per-process namespace/resource
// suffix for the current Ginkgo parallel worker. Returns "" under
// -p 1 (or no -p), "-p2" under proc 2 of N, and so on. The
// empty-string return on proc 1 is intentional: it preserves
// today's resource names exactly when GINKGO_PROCS=1, which is the
// always-available debugging escape valve.
func GinkgoProcessSuffix() string {
	return procSuffix(ginkgo.GinkgoParallelProcess())
}

func procSuffix(proc int) string {
	if proc <= 1 {
		return ""
	}
	return fmt.Sprintf("-p%d", proc)
}

// TenantNamespace appends the per-process suffix to the namespace
// base name. Use this for namespaces in spec setup.
func TenantNamespace(base string) string {
	return base + GinkgoProcessSuffix()
}

// ClusterScopedName appends the per-process suffix to a cluster-
// scoped resource name, so two procs can apply two distinct
// instances without colliding.
func ClusterScopedName(base string) string {
	return base + GinkgoProcessSuffix()
}
