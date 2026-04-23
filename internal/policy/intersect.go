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
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// IntersectionResult describes the outcome of the ADR-0014 admission
// algorithm — intersecting a template's spec.requires with the union
// of matching BrokerPolicy grants in the run's namespace.
type IntersectionResult struct {
	// Admitted is true iff the union of MatchedPolicies' grants covers
	// every requirement in the template's requires block.
	Admitted bool

	// MatchedPolicies lists the BrokerPolicy names whose
	// appliesToTemplates selected the template. Populated regardless
	// of outcome; empty when no BrokerPolicy matched.
	MatchedPolicies []string

	// MissingCredentials lists the credential requirements no matching
	// policy granted. Populated when Admitted is false.
	MissingCredentials []CredentialShortfall

	// MissingEgress lists the egress tuples no matching policy covered.
	// Populated when Admitted is false.
	MissingEgress []EgressShortfall

	// CoveredCredentials maps each covered credential name to the
	// policy + provider that granted it. Informational — for
	// diagnostics and M10's `policy check` CLI.
	CoveredCredentials map[string]CoveredCredential
}

// CredentialShortfall names a required credential that no policy granted.
type CredentialShortfall struct {
	Name    string
	Purpose paddockv1alpha1.CredentialPurpose
}

// EgressShortfall names a (host, port) tuple no policy covered. Port
// is 0 when the requirement specified no ports (meaning "any port")
// and no grant covered it.
type EgressShortfall struct {
	Host string
	Port int32
}

// CoveredCredential names the policy + provider backing a required
// credential.
type CoveredCredential struct {
	Policy   string
	Provider string
}

// ListMatchingPolicies returns the BrokerPolicies in namespace whose
// appliesToTemplates selects templateName. Kept separate from Intersect
// because the interception-mode floor check (ADR-0013 §26) consumes
// the same list, and sharing the code prevents admission-path
// discrepancies between "what policies matched for credentials" and
// "what policies matched for mode floor".
func ListMatchingPolicies(ctx context.Context, c client.Client, namespace, templateName string) ([]*paddockv1alpha1.BrokerPolicy, error) {
	var policies paddockv1alpha1.BrokerPolicyList
	if err := c.List(ctx, &policies, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	var matching []*paddockv1alpha1.BrokerPolicy
	for i := range policies.Items {
		bp := &policies.Items[i]
		if AppliesToTemplate(bp.Spec.AppliesToTemplates, templateName) {
			matching = append(matching, bp)
		}
	}
	return matching, nil
}

// Intersect lists BrokerPolicies in the given namespace, filters by
// appliesToTemplates against templateName, unions their grants, and
// compares against requires. Returns (result, nil) even when
// !result.Admitted — callers use the shortfall lists to produce the
// admission diagnostic.
func Intersect(ctx context.Context, c client.Client, namespace, templateName string, requires paddockv1alpha1.RequireSpec) (*IntersectionResult, error) {
	matching, err := ListMatchingPolicies(ctx, c, namespace, templateName)
	if err != nil {
		return nil, err
	}

	result := &IntersectionResult{
		Admitted:           true,
		CoveredCredentials: make(map[string]CoveredCredential),
	}
	for _, bp := range matching {
		result.MatchedPolicies = append(result.MatchedPolicies, bp.Name)
	}

	for _, cred := range requires.Credentials {
		var cov *CoveredCredential
		for _, bp := range matching {
			for _, g := range bp.Spec.Grants.Credentials {
				if g.Name == cred.Name {
					cov = &CoveredCredential{Policy: bp.Name, Provider: g.Provider.Kind}
					break
				}
			}
			if cov != nil {
				break
			}
		}
		if cov == nil {
			result.Admitted = false
			result.MissingCredentials = append(result.MissingCredentials, CredentialShortfall{
				Name: cred.Name, Purpose: cred.Purpose,
			})
			continue
		}
		result.CoveredCredentials[cred.Name] = *cov
	}

	for _, eg := range requires.Egress {
		ports := eg.Ports
		if len(ports) == 0 {
			ports = []int32{0}
		}
		for _, port := range ports {
			if !grantsCoverEgress(matching, eg.Host, port) {
				result.Admitted = false
				result.MissingEgress = append(result.MissingEgress, EgressShortfall{
					Host: eg.Host, Port: port,
				})
			}
		}
	}

	return result, nil
}

// DescribeShortfall formats an admission-diagnostic string from an
// intersection result. Used by the HarnessRun webhook and the CLI's
// `policy check` command.
func DescribeShortfall(result *IntersectionResult, templateName, namespace string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "template %q requires capabilities not granted in namespace %q:\n", templateName, namespace)
	for _, c := range result.MissingCredentials {
		purpose := c.Purpose
		if purpose == "" {
			purpose = paddockv1alpha1.CredentialPurposeGeneric
		}
		fmt.Fprintf(&sb, "  - credential: %s (purpose: %s)\n", c.Name, purpose)
	}
	for _, e := range result.MissingEgress {
		if e.Port == 0 {
			fmt.Fprintf(&sb, "  - egress: %s\n", e.Host)
		} else {
			fmt.Fprintf(&sb, "  - egress: %s:%d\n", e.Host, e.Port)
		}
	}
	if len(result.MatchedPolicies) == 0 {
		sb.WriteString("  Matching BrokerPolicies considered: (none)\n")
	} else {
		fmt.Fprintf(&sb, "  Matching BrokerPolicies considered: %s\n", strings.Join(result.MatchedPolicies, ", "))
	}
	fmt.Fprintf(&sb, "  Hint: kubectl paddock policy scaffold %s -n %s", templateName, namespace)
	return sb.String()
}

func grantsCoverEgress(policies []*paddockv1alpha1.BrokerPolicy, host string, port int32) bool {
	for _, bp := range policies {
		for _, g := range bp.Spec.Grants.Egress {
			if hostMatches(g.Host, host) && portMatches(g.Ports, port) {
				return true
			}
		}
	}
	return false
}

// hostMatches reports whether a grant host (possibly with a leading
// "*." wildcard) covers a requirement host. Case-insensitive.
func hostMatches(grant, required string) bool {
	g := strings.ToLower(grant)
	r := strings.ToLower(required)
	if g == r {
		return true
	}
	if strings.HasPrefix(g, "*.") {
		suffix := g[1:]
		if strings.HasSuffix(r, suffix) && len(r) > len(suffix) {
			return true
		}
	}
	return false
}

// portMatches reports whether a grant's ports list covers the required
// port. Empty list or a 0 entry means "any port".
func portMatches(grantPorts []int32, required int32) bool {
	if len(grantPorts) == 0 {
		return true
	}
	for _, p := range grantPorts {
		if p == 0 || p == required {
			return true
		}
	}
	return false
}
