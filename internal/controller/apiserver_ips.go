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
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"
)

// APIServerIPsFromConfig returns the IPv4 set the controller's
// kubeconfig resolves the kube-apiserver to. Used to seed a per-run
// NetworkPolicy allow rule on TCP/443 from sidecar pods (collector,
// adapter) so AuditEvent emission and ConfigMap writes can reach the
// apiserver.
//
// Behaviour:
//   - cfg.Host is parsed as a URL. The scheme/port are stripped.
//   - If the host is an IPv4 literal, returns that single IP.
//   - If the host is a hostname, net.LookupIP is called and all returned
//     IPv4 addresses are returned.
//   - IPv6 addresses are filtered out — every existing NP rule in the
//     codebase uses IPv4 ipBlock CIDRs. Dual-stack support is future work.
//   - An empty result (no IPv4 resolved, or the host was empty/invalid)
//     returns an error so manager startup can fail fast rather than
//     starting with a permissively-configured controller.
func APIServerIPsFromConfig(cfg *rest.Config) ([]net.IP, error) {
	if cfg == nil || strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("APIServerIPsFromConfig: empty cfg.Host")
	}
	host := cfg.Host
	// rest.Config.Host can be either a URL ("https://10.96.0.1:443") or
	// a bare host ("10.96.0.1"). Try URL parsing; fall back to host-as-is.
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return nil, fmt.Errorf("APIServerIPsFromConfig: parse host %q: %w", host, err)
		}
		host = u.Hostname()
	} else if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = h
	}
	host = strings.Trim(host, "[]") // strip IPv6 brackets if present

	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return []net.IP{v4}, nil
		}
		return nil, fmt.Errorf("APIServerIPsFromConfig: host %q is IPv6 only; IPv4 required", host)
	}

	netAddrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return nil, fmt.Errorf("APIServerIPsFromConfig: lookup %q: %w", host, err)
	}
	var v4s []net.IP
	for _, a := range netAddrs {
		if v4 := a.IP.To4(); v4 != nil {
			v4s = append(v4s, v4)
		}
	}
	if len(v4s) == 0 {
		return nil, fmt.Errorf("APIServerIPsFromConfig: %q resolved to no IPv4 addresses", host)
	}
	return v4s, nil
}
