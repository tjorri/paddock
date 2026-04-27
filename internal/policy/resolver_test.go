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

import (
	"testing"
)

func TestAppliesToTemplate(t *testing.T) {
	cases := []struct {
		name      string
		selectors []string
		template  string
		want      bool
	}{
		{name: "exact match", selectors: []string{"claude-code"}, template: "claude-code", want: true},
		{name: "exact non-match", selectors: []string{"claude-code"}, template: "openai-coder", want: false},
		{name: "catch-all", selectors: []string{"*"}, template: "anything", want: true},
		{name: "prefix glob match", selectors: []string{"claude-code-*"}, template: "claude-code-prod", want: true},
		{name: "prefix glob non-match", selectors: []string{"claude-code-*"}, template: "openai-coder", want: false},
		{name: "suffix glob match", selectors: []string{"*-prod"}, template: "claude-code-prod", want: true},
		{name: "suffix glob non-match", selectors: []string{"*-prod"}, template: "claude-code-dev", want: false},
		{name: "single-char glob match", selectors: []string{"claude-code-?"}, template: "claude-code-1", want: true},
		{name: "single-char glob non-match", selectors: []string{"claude-code-?"}, template: "claude-code-12", want: false},
		{name: "char-class match", selectors: []string{"[abc]-prod"}, template: "a-prod", want: true},
		{name: "any selector wins", selectors: []string{"foo", "claude-*"}, template: "claude-code", want: true},
		{name: "malformed pattern silently does not match", selectors: []string{"claude-code-["}, template: "claude-code-x", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AppliesToTemplate(tc.selectors, tc.template)
			if got != tc.want {
				t.Fatalf("AppliesToTemplate(%v, %q) = %v, want %v", tc.selectors, tc.template, got, tc.want)
			}
		})
	}
}

func TestValidateAppliesToTemplate(t *testing.T) {
	cases := []struct {
		name     string
		selector string
		wantErr  bool
	}{
		{name: "exact", selector: "claude-code", wantErr: false},
		{name: "catch-all", selector: "*", wantErr: false},
		{name: "prefix glob", selector: "claude-code-*", wantErr: false},
		{name: "suffix glob", selector: "*-prod", wantErr: false},
		{name: "single-char", selector: "claude-?", wantErr: false},
		{name: "char-class", selector: "[abc]-prod", wantErr: false},
		{name: "malformed open bracket", selector: "claude-[", wantErr: true},
		{name: "malformed dangling escape", selector: `claude-\`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAppliesToTemplate(tc.selector)
			gotErr := err != nil
			if gotErr != tc.wantErr {
				t.Fatalf("ValidateAppliesToTemplate(%q) err=%v wantErr=%v", tc.selector, err, tc.wantErr)
			}
		})
	}
}
