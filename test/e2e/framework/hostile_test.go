//go:build e2e
// +build e2e

package framework

import (
	"strings"
	"testing"
)

func TestPATPoolFixtureManifest_Renders(t *testing.T) {
	got := PATPoolFixtureManifest("paddock-t2-revoke", "tg11", 2)
	for _, want := range []string{
		"namespace: paddock-t2-revoke",
		"name: tg11-pool",
		"ghp_fake_tg11_00",
		"ghp_fake_tg11_01",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered manifest missing %q\nfull output:\n%s", want, got)
		}
	}
}
