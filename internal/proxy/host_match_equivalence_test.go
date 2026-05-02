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

package proxy

import (
	"testing"

	"github.com/tjorri/paddock/internal/policy"
)

// TestHostMatchEquivalence guards the broker-side and proxy-side
// host-match implementations from drifting. The proxy retains a
// deliberate "*" catch-all that the policy version lacks; that
// asymmetry is asserted separately so its removal would surface as a
// test failure (not silent drift).
//
// Full proxy-side consolidation onto policy.EgressHostMatches is
// XC-03; this test is the equivalence guard that lets that landing
// happen safely.
func TestHostMatchEquivalence(t *testing.T) {
	t.Parallel()

	// Each pair is (pattern, host). The two implementations must agree
	// on every case below.
	agreeCases := []struct {
		pattern string
		host    string
	}{
		{"example.com", "example.com"},
		{"example.com", "other.com"},
		{"Example.COM", "example.com"},
		{"*.example.com", "api.example.com"},
		{"*.example.com", "example.com"}, // apex — neither matches
		{"*.example.com", "a.b.example.com"},
		{"*.example.com", "other.com"},
		{"foo.com", "bar.com"},
		{"", ""},
		{"example.com", ""},
		{"", "example.com"},
	}
	for _, tc := range agreeCases {
		got := hostMatches(tc.pattern, tc.host)
		want := policy.EgressHostMatches(tc.pattern, tc.host)
		if got != want {
			t.Errorf("disagreement on (pattern=%q, host=%q): proxy=%v policy=%v",
				tc.pattern, tc.host, got, want)
		}
	}

	// Asymmetry: the proxy's "*" is a catch-all; policy does not have
	// one. If you remove the catch-all from proxy.hostMatches, this
	// branch fires — that's intentional, not a regression. Update the
	// test along with the deletion.
	if !hostMatches("*", "anything.com") {
		t.Errorf("proxy.hostMatches lost its * catch-all — update XC-03 plan if intentional")
	}
	if policy.EgressHostMatches("*", "anything.com") {
		t.Errorf("policy.EgressHostMatches now matches *; the asymmetry is gone — drop the asymmetry assertion in this test")
	}
}
