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
	"net"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// clusterInternalExacts are hostnames that resolve only inside a
// Kubernetes cluster. None are legitimate external egress targets.
var clusterInternalExacts = map[string]struct{}{
	"localhost":                            {},
	"kubernetes":                           {},
	"kubernetes.default":                   {},
	"kubernetes.default.svc":               {},
	"kubernetes.default.svc.cluster.local": {},
	"svc":                                  {},
	"cluster.local":                        {},
	"svc.cluster.local":                    {},
}

// clusterInternalSuffixes match any host that ends in one of these
// suffixes after a non-empty label. ".svc.cluster.local" subsumes
// ".svc" and ".cluster.local" via the suffix sweep below; the explicit
// list keeps the predicate self-documenting and order-independent.
var clusterInternalSuffixes = []string{
	".svc.cluster.local",
	".svc",
	".cluster.local",
}

// isClusterInternal reports whether s names a Kubernetes-cluster-internal
// endpoint. Wildcard hosts strip the leading "*." before consulting this
// predicate so "*.svc.cluster.local" resolves through the suffix sweep.
func isClusterInternal(s string) bool {
	if _, ok := clusterInternalExacts[s]; ok {
		return true
	}
	for _, suf := range clusterInternalSuffixes {
		if strings.HasSuffix(s, suf) && len(s) > len(suf) {
			return true
		}
	}
	return false
}

// validateExternalHost enforces the per-host rules common to
// BrokerPolicy.spec.grants[].egress[].host, BrokerPolicy.spec.grants[].
// credentials[].provider.hosts (and proxyInjected.hosts), and
// HarnessTemplate.spec.requires.egress[].host.
//
// Rules (Phase 2h Theme 3, F-23/F-34/F-35):
//   - Empty / whitespace-only is field.Required.
//   - Bare "*" alone (catch-all) is rejected; bare "*." (empty suffix)
//     is rejected.
//   - Wildcard position: only a single leading "*." is permitted.
//   - Cluster-internal hosts (localhost, kubernetes.*, *.svc, *.svc.
//     cluster.local, *.cluster.local) are rejected — for both literal
//     and "*."-prefixed forms.
//   - IP literals (v4 and v6, with or without brackets) are rejected.
//   - Host must not contain a port (use the egress grant's "ports" field).
//   - Host must be a lowercase RFC 1123 DNS subdomain (after stripping
//     a leading "*." for wildcards).
func validateExternalHost(p *field.Path, raw string) field.ErrorList {
	var errs field.ErrorList
	host := strings.TrimSpace(raw)
	if host == "" {
		errs = append(errs, field.Required(p, ""))
		return errs
	}

	// Bare "*" (catch-all). The wildcard-position rule below would also
	// catch this, but the catch-all error message is specifically
	// helpful: it tells the operator to write "*.example.com" instead.
	if host == "*" {
		errs = append(errs, field.Invalid(p, raw,
			`catch-all host is not permitted; restrict to a specific TLD or apex (e.g. "*.example.com")`))
		return errs
	}
	// Bare "*." (empty suffix).
	if host == "*." {
		errs = append(errs, field.Invalid(p, raw,
			`empty wildcard suffix is not permitted; provide a TLD or apex after "*." (e.g. "*.example.com")`))
		return errs
	}

	// Wildcard position.
	if strings.HasPrefix(host, "*.") && strings.Contains(host[2:], "*") {
		errs = append(errs, field.Invalid(p, raw,
			"only a single leading '*.' wildcard is permitted"))
		return errs
	}
	if !strings.HasPrefix(host, "*.") && strings.Contains(host, "*") {
		errs = append(errs, field.Invalid(p, raw,
			"wildcard '*' is only permitted as a leading '*.' segment"))
		return errs
	}

	// Strip "*." for downstream checks. effective is what we test against
	// the cluster-internal / RFC 1123 / IP-literal predicates.
	effective := strings.TrimPrefix(host, "*.")

	// IP-literal checks — must run before the port-in-host colon check
	// because bare IPv6 addresses (e.g. "::1", "2001:db8::1") contain
	// colons and would otherwise trigger the port error first.
	//
	// 1. Bracketed form "[::1]": strip brackets then test with ParseIP.
	if strings.HasPrefix(effective, "[") && strings.HasSuffix(effective, "]") {
		stripped := effective[1 : len(effective)-1]
		if net.ParseIP(stripped) != nil {
			errs = append(errs, field.Invalid(p, raw,
				"IP literals are not permitted; use a DNS name so the proxy can match the SNI"))
			return errs
		}
	}
	// 2. Unbracketed form ("::1", "10.0.0.1", "2001:db8::1").
	if net.ParseIP(effective) != nil {
		errs = append(errs, field.Invalid(p, raw,
			"IP literals are not permitted; use a DNS name so the proxy can match the SNI"))
		return errs
	}

	// Port-in-host: a colon in the (unbracketed) effective host means
	// the operator wrote "api.example.com:443" instead of putting the
	// port in the egress grant's "ports" field.
	// IP literals have already been rejected above, so any remaining
	// colon is a port separator.
	if !strings.HasPrefix(effective, "[") && strings.Contains(effective, ":") {
		errs = append(errs, field.Invalid(p, raw,
			`host must not contain a port; ports go in the egress grant's "ports" field`))
		return errs
	}

	if isClusterInternal(effective) {
		errs = append(errs, field.Invalid(p, raw,
			"cluster-internal hosts are not permitted; use a public DNS name instead"))
		return errs
	}

	if msgs := validation.IsDNS1123Subdomain(effective); len(msgs) > 0 {
		errs = append(errs, field.Invalid(p, raw,
			"host must be a lowercase RFC 1123 DNS name: "+strings.Join(msgs, "; ")))
		return errs
	}

	return errs
}
