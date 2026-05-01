//go:build e2e
// +build e2e

package framework

import "testing"

func TestGinkgoProcessSuffix_ReturnsEmptyByDefault(t *testing.T) {
	if got := GinkgoProcessSuffix(); got != "" {
		t.Fatalf("GinkgoProcessSuffix() = %q, want empty", got)
	}
}
