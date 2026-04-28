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
	"fmt"
	"net"
	"strings"
)

// DeniedCIDRSet is a closed set of CIDR networks the proxy will refuse
// to dial regardless of whether the BrokerPolicy allow-list passed.
// Used by F-22 layer 2 (private/cluster-internal IP rejection) on
// post-resolution IPs and on the agent's transparent SO_ORIGINAL_DST.
type DeniedCIDRSet struct {
	nets []*net.IPNet
}

// ParseDeniedCIDRs parses a comma-separated CIDR list. Empty input
// returns an empty (no-deny) set; whitespace around entries is
// tolerated. A malformed entry returns an error.
func ParseDeniedCIDRs(csv string) (*DeniedCIDRSet, error) {
	d := &DeniedCIDRSet{}
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return d, nil
	}
	for _, raw := range strings.Split(csv, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("denied CIDR %q: %w", raw, err)
		}
		d.nets = append(d.nets, n)
	}
	return d, nil
}

// Contains returns true when ip falls in any denied network. Nil
// receiver and empty set both report false. Nil ip reports false.
func (d *DeniedCIDRSet) Contains(ip net.IP) bool {
	if d == nil || len(d.nets) == 0 || ip == nil {
		return false
	}
	for _, n := range d.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
