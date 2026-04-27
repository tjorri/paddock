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

package v1alpha1

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateExternalHost_BaselineRules(t *testing.T) {
	cases := []struct {
		name        string
		host        string
		wantErr     bool
		errContains string
	}{
		{name: "plain dns", host: "api.example.com", wantErr: false},
		{name: "leading wildcard", host: "*.example.com", wantErr: false},
		{name: "empty", host: "", wantErr: true, errContains: "Required"},
		{name: "interior wildcard", host: "api.*.example.com", wantErr: true, errContains: "wildcard"},
		{name: "trailing wildcard", host: "example.*", wantErr: true, errContains: "wildcard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateExternalHost(field.NewPath("host"), tc.host)
			gotErr := len(errs) > 0
			if gotErr != tc.wantErr {
				t.Fatalf("validateExternalHost(%q): got errs=%v want err=%v", tc.host, errs, tc.wantErr)
			}
			if tc.wantErr && tc.errContains != "" && !strings.Contains(errs.ToAggregate().Error(), tc.errContains) {
				t.Fatalf("validateExternalHost(%q): err=%q does not contain %q", tc.host, errs.ToAggregate().Error(), tc.errContains)
			}
		})
	}
}
