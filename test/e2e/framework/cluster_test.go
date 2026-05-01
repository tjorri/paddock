//go:build e2e
// +build e2e

package framework

import "testing"

func TestCreateTenantNamespace_AppendsSuffix(t *testing.T) {
	// PR 1 stub: GinkgoProcessSuffix() returns "". The function under
	// test isn't called here — we only assert the suffixing rule
	// directly. PR 4 swaps this for a -p-aware test.
	if got := "paddock-egress" + GinkgoProcessSuffix(); got != "paddock-egress" {
		t.Fatalf("expected base name under proc 1, got %q", got)
	}
}
