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

package broker

import "testing"

func TestParseServiceAccountSubject(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		input     string
		wantNS    string
		wantSA    string
		wantError bool
	}{
		{
			name:   "well-formed",
			input:  "system:serviceaccount:my-ns:my-sa",
			wantNS: "my-ns", wantSA: "my-sa",
		},
		{
			name:      "missing prefix",
			input:     "alice",
			wantError: true,
		},
		{
			name:      "empty after prefix",
			input:     "system:serviceaccount:",
			wantError: true,
		},
		{
			name:      "single component (missing SA)",
			input:     "system:serviceaccount:my-ns",
			wantError: true,
		},
		{
			name:      "empty namespace",
			input:     "system:serviceaccount::my-sa",
			wantError: true,
		},
		{
			name:      "empty SA",
			input:     "system:serviceaccount:my-ns:",
			wantError: true,
		},
		{
			// Documented current behaviour: SplitN(":", 2) preserves
			// any further colons in the SA name. Kubernetes SA names
			// can't contain ':' per RFC 1123, but the parser is lenient.
			name:   "extra colons preserved in SA component",
			input:  "system:serviceaccount:my-ns:foo:bar",
			wantNS: "my-ns", wantSA: "foo:bar",
		},
		{
			name:      "empty input",
			input:     "",
			wantError: true,
		},
		{
			name:      "prefix only",
			input:     "system:serviceaccount",
			wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotNS, gotSA, err := parseServiceAccountSubject(tc.input)
			if tc.wantError {
				if err == nil {
					t.Fatalf("parseServiceAccountSubject(%q) returned (%q, %q, nil); want error",
						tc.input, gotNS, gotSA)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseServiceAccountSubject(%q) error: %v", tc.input, err)
			}
			if gotNS != tc.wantNS || gotSA != tc.wantSA {
				t.Fatalf("parseServiceAccountSubject(%q) = (%q, %q); want (%q, %q)",
					tc.input, gotNS, gotSA, tc.wantNS, tc.wantSA)
			}
		})
	}
}

func TestHasAudience(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  []string
		want string
		ok   bool
	}{
		{name: "exact match", got: []string{"paddock-broker"}, want: "paddock-broker", ok: true},
		{name: "match in list", got: []string{"a", "paddock-broker", "b"}, want: "paddock-broker", ok: true},
		{name: "mismatch", got: []string{"other-audience"}, want: "paddock-broker", ok: false},
		{name: "empty list", got: nil, want: "paddock-broker", ok: false},
		{name: "empty list slice", got: []string{}, want: "paddock-broker", ok: false},
		{name: "case-sensitive (no match)", got: []string{"Paddock-Broker"}, want: "paddock-broker", ok: false},
		{name: "empty want against empty list", got: nil, want: "", ok: false},
		{name: "empty want against list containing empty", got: []string{""}, want: "", ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := hasAudience(tc.got, tc.want)
			if got != tc.ok {
				t.Fatalf("hasAudience(%v, %q) = %v; want %v", tc.got, tc.want, got, tc.ok)
			}
		})
	}
}
