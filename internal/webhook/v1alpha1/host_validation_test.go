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

func TestValidateExternalHost_ClusterInternalDenied(t *testing.T) {
	denied := []string{
		"localhost",
		"kubernetes",
		"kubernetes.default",
		"kubernetes.default.svc",
		"kubernetes.default.svc.cluster.local",
		"svc",
		"cluster.local",
		"svc.cluster.local",
		"paddock-broker.paddock-system.svc",
		"paddock-broker.paddock-system.svc.cluster.local",
		"foo.bar.cluster.local",
		"*.svc",
		"*.svc.cluster.local",
		"*.cluster.local",
	}
	for _, h := range denied {
		t.Run(h, func(t *testing.T) {
			errs := validateExternalHost(field.NewPath("host"), h)
			if len(errs) == 0 {
				t.Fatalf("validateExternalHost(%q): expected rejection, got none", h)
			}
			if !strings.Contains(errs.ToAggregate().Error(), "cluster-internal") {
				t.Fatalf("validateExternalHost(%q): err=%q missing cluster-internal hint", h, errs.ToAggregate().Error())
			}
		})
	}
}

func TestValidateExternalHost_IPLiteralsDenied(t *testing.T) {
	denied := []string{
		"127.0.0.1",
		"10.0.0.1",
		"169.254.169.254",
		"192.0.2.1",
		"::1",
		"[::1]",
		"2001:db8::1",
	}
	for _, h := range denied {
		t.Run(h, func(t *testing.T) {
			errs := validateExternalHost(field.NewPath("host"), h)
			if len(errs) == 0 {
				t.Fatalf("validateExternalHost(%q): expected rejection, got none", h)
			}
			if !strings.Contains(errs.ToAggregate().Error(), "IP literal") {
				t.Fatalf("validateExternalHost(%q): err=%q missing IP-literal hint", h, errs.ToAggregate().Error())
			}
		})
	}
}

func TestValidateExternalHost_BareWildcards(t *testing.T) {
	cases := []struct {
		host string
		hint string
	}{
		{"*", "catch-all"},
		{"*.", "wildcard"},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			errs := validateExternalHost(field.NewPath("host"), tc.host)
			if len(errs) == 0 {
				t.Fatalf("validateExternalHost(%q): expected rejection, got none", tc.host)
			}
			if !strings.Contains(errs.ToAggregate().Error(), tc.hint) {
				t.Fatalf("validateExternalHost(%q): err=%q missing %q", tc.host, errs.ToAggregate().Error(), tc.hint)
			}
		})
	}
}

func TestValidateExternalHost_PortInHostDenied(t *testing.T) {
	denied := []string{
		"api.example.com:443",
		"api.example.com:8080",
		"*.example.com:443",
	}
	for _, h := range denied {
		t.Run(h, func(t *testing.T) {
			errs := validateExternalHost(field.NewPath("host"), h)
			if len(errs) == 0 {
				t.Fatalf("validateExternalHost(%q): expected rejection, got none", h)
			}
			if !strings.Contains(errs.ToAggregate().Error(), "must not contain a port") {
				t.Fatalf("validateExternalHost(%q): err=%q missing port-in-host hint", h, errs.ToAggregate().Error())
			}
		})
	}
}

func TestValidateExternalHost_RFC1123Denied(t *testing.T) {
	denied := []string{
		"Api.Example.com", // mixed case
		"-leading-hyphen.example.com",
		"label..double-dot.example.com",
	}
	for _, h := range denied {
		t.Run(h, func(t *testing.T) {
			errs := validateExternalHost(field.NewPath("host"), h)
			if len(errs) == 0 {
				t.Fatalf("validateExternalHost(%q): expected rejection, got none", h)
			}
			if !strings.Contains(errs.ToAggregate().Error(), "RFC 1123") {
				t.Fatalf("validateExternalHost(%q): err=%q missing RFC 1123 hint", h, errs.ToAggregate().Error())
			}
		})
	}
}

func TestValidateExternalHost_PublicDNSAccepted(t *testing.T) {
	accepted := []string{
		"api.example.com",
		"*.example.com",
		"s3.amazonaws.com",
		"api.openai.com",
	}
	for _, h := range accepted {
		t.Run(h, func(t *testing.T) {
			errs := validateExternalHost(field.NewPath("host"), h)
			if len(errs) != 0 {
				t.Fatalf("validateExternalHost(%q): unexpected errs=%v", h, errs)
			}
		})
	}
}
