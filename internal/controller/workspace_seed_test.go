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

package controller

import "testing"

func TestIsSSHURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"ssh scheme", "ssh://git@example.com/org/repo.git", true},
		{"scp-style", "git@example.com:org/repo.git", true},
		{"scp-style with port-less host", "deploy@host:repo", true},
		{"plain https", "https://example.com/org/repo.git", false},
		{"https with user info and port", "https://user@example.com:443/org/repo.git", false},
		{"https with user info only", "https://user@example.com/org/repo.git", false},
		{"http scheme", "http://example.com/repo.git", false},
		{"git scheme", "git://example.com/repo.git", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSSHURL(tc.url); got != tc.want {
				t.Fatalf("isSSHURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
