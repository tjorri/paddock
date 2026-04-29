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
	"paddock.dev/paddock/internal/policy"
)

// resolveTemplateSpec is a thin wrapper that drops the source string
// from policy.ResolveTemplate — the broker doesn't need it.
func resolveTemplateSpec(ctx context.Context, c client.Client, namespace string, ref paddockv1alpha1.TemplateRef) (*paddockv1alpha1.HarnessTemplateSpec, error) {
	spec, _, err := policy.ResolveTemplate(ctx, c, namespace, ref)
	return spec, err
}

// hasRequirement reports whether the template declares a credential
// requirement with the given name.
func hasRequirement(creds []paddockv1alpha1.CredentialRequirement, name string) bool {
	for _, c := range creds {
		if c.Name == name {
			return true
		}
	}
	return false
}

// matchPolicyGrant walks BrokerPolicies in the namespace, selects
// those whose appliesToTemplates matches templateName, and returns
// the first grant whose Name == credentialName together with the
// matched policy (for its sibling gitRepos, which gitforge providers
// scope their tokens against). Returns (nil, nil, "", nil) when
// nothing matches. Multiple policies compose additively; on a
// collision the first match wins (deterministic by name order; ties
// broken by etcd ordering, which is stable per-namespace).
//
// This is a subset of the full ADR-0014 intersection — M3 only needs
// credential lookup by name. The webhook's admission-time check
// implements the complete algorithm in M3 commit 2.
func matchPolicyGrant(ctx context.Context, c client.Client, namespace, templateName, credentialName string) (*paddockv1alpha1.CredentialGrant, *paddockv1alpha1.BrokerPolicy, string, error) {
	var list paddockv1alpha1.BrokerPolicyList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, nil, "", err
	}
	for i := range list.Items {
		bp := &list.Items[i]
		if !policy.AppliesToTemplate(bp.Spec.AppliesToTemplates, templateName) {
			continue
		}
		for j := range bp.Spec.Grants.Credentials {
			g := &bp.Spec.Grants.Credentials[j]
			if g.Name == credentialName {
				return g, bp, bp.Name, nil
			}
		}
	}
	return nil, nil, "", nil
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

// anyProxyInjectedHostCovers reports whether any BrokerPolicy in
// namespace that applies to templateName has a credential grant whose
// deliveryMode.proxyInjected.hosts covers host. Used by
// handleValidateEgress to decide SubstituteAuth on the allow path.
//
// Multi-policy semantics: any-wins (mirrors egressDiscovery; matches
// the v0.4 ethos that matching policies compose additively). Host
// matching reuses policy.AnyHostMatches so "*.foo.com" works.
//
// Errors propagate from List; nil error + false return on no match.
func anyProxyInjectedHostCovers(
	ctx context.Context,
	c client.Client,
	namespace, templateName, host string,
) (bool, error) {
	var list paddockv1alpha1.BrokerPolicyList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	for i := range list.Items {
		bp := &list.Items[i]
		if !policy.AppliesToTemplate(bp.Spec.AppliesToTemplates, templateName) {
			continue
		}
		for j := range bp.Spec.Grants.Credentials {
			g := &bp.Spec.Grants.Credentials[j]
			if g.Provider.DeliveryMode == nil || g.Provider.DeliveryMode.ProxyInjected == nil {
				continue
			}
			if policy.AnyHostMatches(g.Provider.DeliveryMode.ProxyInjected.Hosts, host) {
				return true, nil
			}
		}
	}
	return false, nil
}
