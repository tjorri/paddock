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

// GinkgoProcessSuffix returns the per-process namespace/resource suffix
// for the current Ginkgo parallel worker. Returns "" under -p 1 (or no
// -p), "-p2" under proc 2 of N, and so on. The empty-string return on
// proc 1 is intentional: it preserves today's resource names exactly
// when GINKGO_PROCS=1, which is the always-available debugging escape
// valve.
//
// Wired up properly in PR 4; PR 1 returns "" unconditionally so the
// signature exists for callers without changing today's namespace
// strings.
func GinkgoProcessSuffix() string {
	return ""
}
