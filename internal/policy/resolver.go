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

// Package policy implements the shared admission and runtime capability
// logic from ADR-0014. Both the HarnessRun validating webhook and (in
// M3+) the broker's runtime checks route through the same types and
// functions here — the admission path is a fast path, not a separate
// policy language.
package policy

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// ResolveTemplate walks TemplateRef.Kind (or the default "prefer
// namespaced" order) and returns the fully-resolved spec — with parent
// inheritance already merged per ADR-0003.
//
// M2 uses this only from the HarnessRun webhook to extract the
// template's Requires. The broker reuses it in M3+.
func ResolveTemplate(ctx context.Context, c client.Client, namespace string, ref paddockv1alpha1.TemplateRef) (*paddockv1alpha1.HarnessTemplateSpec, string, error) {
	switch ref.Kind {
	case "HarnessTemplate":
		return getHarnessTemplate(ctx, c, namespace, ref.Name)
	case "ClusterHarnessTemplate":
		return getClusterHarnessTemplate(ctx, c, ref.Name)
	case "", "namespaced-first":
		// Default: prefer namespaced; fall back to cluster.
		spec, source, err := getHarnessTemplate(ctx, c, namespace, ref.Name)
		if err == nil {
			return spec, source, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, "", err
		}
		return getClusterHarnessTemplate(ctx, c, ref.Name)
	default:
		return nil, "", fmt.Errorf("unknown template kind %q", ref.Kind)
	}
}

// RequiresEmpty reports whether a resolved template declares no
// capabilities. Used by M2's admission webhook to reject any run whose
// template has expectations the broker is not yet present to satisfy.
// M3 replaces this call with the full intersection algorithm.
func RequiresEmpty(r paddockv1alpha1.RequireSpec) bool {
	return len(r.Credentials) == 0 && len(r.Egress) == 0
}

// AppliesToTemplate reports whether a BrokerPolicy's
// appliesToTemplates selector list matches the given template name.
// A selector of "*" matches anything; otherwise an exact name match is
// required. See ADR-0014.
func AppliesToTemplate(selectors []string, templateName string) bool {
	for _, sel := range selectors {
		if sel == "*" || sel == templateName {
			return true
		}
	}
	return false
}

func getHarnessTemplate(ctx context.Context, c client.Client, namespace, name string) (*paddockv1alpha1.HarnessTemplateSpec, string, error) {
	var ht paddockv1alpha1.HarnessTemplate
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &ht); err != nil {
		return nil, "", err
	}
	if ht.Spec.BaseTemplateRef != nil {
		parent, _, err := getClusterHarnessTemplate(ctx, c, ht.Spec.BaseTemplateRef.Name)
		if err != nil {
			return nil, "", fmt.Errorf("resolving base template %q: %w", ht.Spec.BaseTemplateRef.Name, err)
		}
		merged := MergeTemplates(*parent, ht.Spec)
		return &merged, "HarnessTemplate/" + ht.Name, nil
	}
	spec := *ht.Spec.DeepCopy()
	return &spec, "HarnessTemplate/" + ht.Name, nil
}

func getClusterHarnessTemplate(ctx context.Context, c client.Client, name string) (*paddockv1alpha1.HarnessTemplateSpec, string, error) {
	var cht paddockv1alpha1.ClusterHarnessTemplate
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &cht); err != nil {
		return nil, "", err
	}
	spec := *cht.Spec.DeepCopy()
	return &spec, "ClusterHarnessTemplate/" + cht.Name, nil
}

// MergeTemplates applies child overrides onto a parent's locked fields
// per ADR-0003. Locked (inherited verbatim): Image, Command, Args,
// EventAdapter, Workspace, Harness. Overridable (child wins when set):
// Defaults, Requires, PodTemplateOverlay. The controller's reconciler
// and this package's webhook + (in M3+) broker clients all route
// through this function.
func MergeTemplates(parent, child paddockv1alpha1.HarnessTemplateSpec) paddockv1alpha1.HarnessTemplateSpec {
	out := *parent.DeepCopy()

	if child.Defaults.Model != "" {
		out.Defaults.Model = child.Defaults.Model
	}
	if child.Defaults.Timeout != nil {
		out.Defaults.Timeout = child.Defaults.Timeout.DeepCopy()
	}
	if child.Defaults.Resources != nil {
		out.Defaults.Resources = child.Defaults.Resources.DeepCopy()
	}
	if child.Defaults.TerminationGracePeriodSeconds != nil {
		v := *child.Defaults.TerminationGracePeriodSeconds
		out.Defaults.TerminationGracePeriodSeconds = &v
	}

	if len(child.Requires.Credentials) > 0 {
		byName := make(map[string]paddockv1alpha1.CredentialRequirement, len(out.Requires.Credentials))
		for _, c := range out.Requires.Credentials {
			byName[c.Name] = c
		}
		for _, c := range child.Requires.Credentials {
			byName[c.Name] = c
		}
		merged := make([]paddockv1alpha1.CredentialRequirement, 0, len(byName))
		for _, c := range byName {
			merged = append(merged, c)
		}
		out.Requires.Credentials = merged
	}
	if len(child.Requires.Egress) > 0 {
		out.Requires.Egress = append(append([]paddockv1alpha1.EgressRequirement{}, out.Requires.Egress...), child.Requires.Egress...)
	}

	if child.PodTemplateOverlay != nil {
		out.PodTemplateOverlay = child.PodTemplateOverlay.DeepCopy()
	}

	return out
}
