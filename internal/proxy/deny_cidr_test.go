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
	"net"
	"testing"
)

func TestParseDeniedCIDRs_RejectsMalformed(t *testing.T) {
	if _, err := ParseDeniedCIDRs("10.0.0.0/8,not-a-cidr"); err == nil {
		t.Fatal("expected error for malformed CIDR; got nil")
	}
}

func TestDeniedCIDRSet_Contains(t *testing.T) {
	d, err := ParseDeniedCIDRs("10.0.0.0/8,169.254.0.0/16,127.0.0.0/8")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.50", true},
		{"169.254.169.254", true},
		{"127.0.0.1", true},
		{"1.2.3.4", false},
		{"172.16.0.1", false}, // not in this denied set
	}
	for _, c := range cases {
		got := d.Contains(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("Contains(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestDeniedCIDRSet_NilOrEmptyContainsFalse(t *testing.T) {
	var d *DeniedCIDRSet
	if d.Contains(net.ParseIP("10.0.0.1")) {
		t.Error("nil set should report Contains=false")
	}
	d2, _ := ParseDeniedCIDRs("")
	if d2.Contains(net.ParseIP("10.0.0.1")) {
		t.Error("empty set should report Contains=false")
	}
}
