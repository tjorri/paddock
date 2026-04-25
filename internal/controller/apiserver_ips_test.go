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

import (
	"net"
	"testing"

	"k8s.io/client-go/rest"
)

func TestAPIServerIPsFromConfig_IPLiteralHost(t *testing.T) {
	cfg := &rest.Config{Host: "https://10.96.0.1:443"}
	ips, err := apiserverIPsFromConfig(cfg)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("10.96.0.1")) {
		t.Errorf("ips = %v, want [10.96.0.1]", ips)
	}
}

func TestAPIServerIPsFromConfig_LocalhostHost(t *testing.T) {
	cfg := &rest.Config{Host: "https://localhost:6443"}
	ips, err := apiserverIPsFromConfig(cfg)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// localhost resolves to at least 127.0.0.1 on every platform.
	found127 := false
	for _, ip := range ips {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			found127 = true
		}
	}
	if !found127 {
		t.Errorf("ips = %v, want includes 127.0.0.1", ips)
	}
}

func TestAPIServerIPsFromConfig_HostWithoutScheme(t *testing.T) {
	cfg := &rest.Config{Host: "10.96.0.1"}
	ips, err := apiserverIPsFromConfig(cfg)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("10.96.0.1")) {
		t.Errorf("ips = %v, want [10.96.0.1]", ips)
	}
}

func TestAPIServerIPsFromConfig_EmptyHost(t *testing.T) {
	cfg := &rest.Config{Host: ""}
	_, err := apiserverIPsFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for empty host")
	}
}

func TestAPIServerIPsFromConfig_UnresolvableHost(t *testing.T) {
	// .invalid is reserved by RFC 6761 — guaranteed never to resolve.
	cfg := &rest.Config{Host: "https://nothing.invalid:6443"}
	_, err := apiserverIPsFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for unresolvable host")
	}
}

func TestAPIServerIPsFromConfig_FiltersIPv6(t *testing.T) {
	// ::1 is loopback v6 only. The helper must reject IPv6-only hosts
	// because every existing NP rule uses IPv4 ipBlock CIDRs.
	cfg := &rest.Config{Host: "https://[::1]:6443"}
	_, err := apiserverIPsFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for IPv6-only host (no IPv4 resolved)")
	}
}
