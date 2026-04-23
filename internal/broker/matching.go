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

package broker

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/broker/providers"
	"paddock.dev/paddock/internal/policy"
)

// resolveTemplateSpec is a thin wrapper that drops the source string
// from policy.ResolveTemplate — the broker doesn't need it.
func resolveTemplateSpec(ctx context.Context, c client.Client, namespace string, ref paddockv1alpha1.TemplateRef) (*paddockv1alpha1.HarnessTemplateSpec, error) {
	spec, _, err := policy.ResolveTemplate(ctx, c, namespace, ref)
	return spec, err
}

// findRequirement looks up a credential requirement by name.
func findRequirement(creds []paddockv1alpha1.CredentialRequirement, name string) (paddockv1alpha1.CredentialRequirement, bool) {
	for _, c := range creds {
		if c.Name == name {
			return c, true
		}
	}
	return paddockv1alpha1.CredentialRequirement{}, false
}

// matchPolicyGrant walks BrokerPolicies in the namespace, selects
// those whose appliesToTemplates matches templateName, and returns
// the first grant whose Name == credentialName. Returns (nil, "", nil)
// when nothing matches. Multiple policies compose additively; on a
// collision the first match wins (deterministic by name order; ties
// broken by etcd ordering, which is stable per-namespace).
//
// This is a subset of the full ADR-0014 intersection — M3 only needs
// credential lookup by name. The webhook's admission-time check
// implements the complete algorithm in M3 commit 2.
func matchPolicyGrant(ctx context.Context, c client.Client, namespace, templateName, credentialName string) (*paddockv1alpha1.CredentialGrant, string, error) {
	var list paddockv1alpha1.BrokerPolicyList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, "", err
	}
	for i := range list.Items {
		bp := &list.Items[i]
		if !policy.AppliesToTemplate(bp.Spec.AppliesToTemplates, templateName) {
			continue
		}
		for j := range bp.Spec.Grants.Credentials {
			g := &bp.Spec.Grants.Credentials[j]
			if g.Name == credentialName {
				return g, bp.Name, nil
			}
		}
	}
	return nil, "", nil
}

// providerBacksPurpose reports whether p lists purpose in its
// Purposes() set.
func providerBacksPurpose(p providers.Provider, purpose paddockv1alpha1.CredentialPurpose) bool {
	// Empty purpose on the requirement is treated as "generic".
	if purpose == "" {
		purpose = paddockv1alpha1.CredentialPurposeGeneric
	}
	for _, supported := range p.Purposes() {
		if supported == purpose {
			return true
		}
	}
	return false
}

// matchEgressGrant walks BrokerPolicies applying to the run's template
// and returns the first egress grant that covers (host, port).
// Multiple matching grants compose additively; on a collision the
// first matching policy wins (deterministic by List order).
//
// A nil return means no policy grants the destination — the proxy's
// ValidateEgress call then becomes a deny.
func matchEgressGrant(
	ctx context.Context,
	c client.Client,
	namespace, templateName, host string,
	port int,
) (grant *paddockv1alpha1.EgressGrant, policyName string, err error) {
	var list paddockv1alpha1.BrokerPolicyList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, "", err
	}
	for i := range list.Items {
		bp := &list.Items[i]
		if !policy.AppliesToTemplate(bp.Spec.AppliesToTemplates, templateName) {
			continue
		}
		for j := range bp.Spec.Grants.Egress {
			g := &bp.Spec.Grants.Egress[j]
			if egressCovers(g, host, port) {
				return g, bp.Name, nil
			}
		}
	}
	return nil, "", nil
}

// egressCovers mirrors policy.grantsCoverEgress for one grant — kept
// as a thin wrapper so the broker can pass a *paddockv1alpha1.EgressGrant
// directly (policy package operates on slices at admission time).
func egressCovers(g *paddockv1alpha1.EgressGrant, host string, port int) bool {
	if !policy.EgressHostMatches(g.Host, host) {
		return false
	}
	if len(g.Ports) == 0 {
		return true
	}
	p32 := int32(port) //nolint:gosec // CONNECT port is bounded [1,65535]
	for _, allowed := range g.Ports {
		if allowed == 0 || allowed == p32 {
			return true
		}
	}
	return false
}
