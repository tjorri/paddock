//go:build e2e
// +build e2e

package framework

import "testing"

// We can't unit-test GinkgoParallelProcess() without running inside
// a Ginkgo run. Verify the formatting helper directly.
func TestProcSuffix_FormatsCorrectly(t *testing.T) {
	for _, tc := range []struct {
		proc int
		want string
	}{
		{1, ""},
		{2, "-p2"},
		{4, "-p4"},
	} {
		if got := procSuffix(tc.proc); got != tc.want {
			t.Errorf("procSuffix(%d) = %q, want %q", tc.proc, got, tc.want)
		}
	}
}
