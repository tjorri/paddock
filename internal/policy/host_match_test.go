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

package policy

import "testing"

func TestAnyHostMatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		grants   []string
		required string
		want     bool
	}{
		{"empty grants", nil, "example.com", false},
		{"empty grants slice", []string{}, "example.com", false},
		{"exact literal match", []string{"example.com"}, "example.com", true},
		{"exact literal mismatch", []string{"example.com"}, "other.com", false},
		{"case-insensitive match", []string{"Example.COM"}, "example.com", true},
		{"wildcard subdomain match", []string{"*.example.com"}, "api.example.com", true},
		{"wildcard does not match apex", []string{"*.example.com"}, "example.com", false},
		{"wildcard multi-label match", []string{"*.example.com"}, "a.b.example.com", true},
		{"second grant matches", []string{"a.com", "b.com"}, "b.com", true},
		{"trim whitespace on grant", []string{" example.com "}, "example.com", true},
		{"trim whitespace on required", []string{"example.com"}, " example.com ", true},
		{"unrelated grant", []string{"foo.com"}, "bar.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := AnyHostMatches(tc.grants, tc.required)
			if got != tc.want {
				t.Fatalf("AnyHostMatches(%v, %q) = %v, want %v",
					tc.grants, tc.required, got, tc.want)
			}
		})
	}
}
