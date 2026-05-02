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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/policy"
)

// resolvedTemplate is a flattened HarnessTemplateSpec — either a standalone
// ClusterHarnessTemplate / HarnessTemplate or a namespaced template merged
// onto its cluster parent per ADR-0003.
type resolvedTemplate struct {
	// Spec is the effective, fully-merged spec the run should run with.
	Spec paddockv1alpha1.HarnessTemplateSpec

	// SourceKind and SourceName record where Spec came from, for status
	// reporting and events. When a namespaced template inherits, these
	// reflect the namespaced template (the "direct" target of templateRef).
	SourceKind string
	SourceName string
}

// resolveTemplate looks up the referenced template and, if it inherits,
// merges its parent's locked fields in. Returns a NotFound-wrapping error
// when the target template doesn't exist so callers can surface a clear
// "TemplateNotFound" reason.
func resolveTemplate(ctx context.Context, c client.Client, run *paddockv1alpha1.HarnessRun) (*resolvedTemplate, error) {
	ref := run.Spec.TemplateRef
	if ref.Name == "" {
		return nil, fmt.Errorf("spec.templateRef.name is empty")
	}

	// Explicit kind: honour it without fallback.
	switch ref.Kind {
	case "HarnessTemplate":
		return resolveNamespacedTemplate(ctx, c, run.Namespace, ref.Name)
	case "ClusterHarnessTemplate":
		return resolveClusterTemplate(ctx, c, ref.Name)
	case "":
		// Unspecified: namespaced first, fall back to cluster.
		t, err := resolveNamespacedTemplate(ctx, c, run.Namespace, ref.Name)
		if err == nil {
			return t, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		return resolveClusterTemplate(ctx, c, ref.Name)
	default:
		return nil, fmt.Errorf("spec.templateRef.kind %q is not supported", ref.Kind)
	}
}

func resolveNamespacedTemplate(ctx context.Context, c client.Client, namespace, name string) (*resolvedTemplate, error) {
	var ht paddockv1alpha1.HarnessTemplate
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &ht); err != nil {
		return nil, err
	}

	spec := *ht.Spec.DeepCopy()
	if spec.BaseTemplateRef != nil {
		var parent paddockv1alpha1.ClusterHarnessTemplate
		if err := c.Get(ctx, client.ObjectKey{Name: spec.BaseTemplateRef.Name}, &parent); err != nil {
			return nil, fmt.Errorf("resolving baseTemplateRef %q: %w", spec.BaseTemplateRef.Name, err)
		}
		merged := policy.MergeTemplates(parent.Spec, spec)
		return &resolvedTemplate{Spec: merged, SourceKind: "HarnessTemplate", SourceName: ht.Name}, nil
	}
	return &resolvedTemplate{Spec: spec, SourceKind: "HarnessTemplate", SourceName: ht.Name}, nil
}

func resolveClusterTemplate(ctx context.Context, c client.Client, name string) (*resolvedTemplate, error) {
	var cht paddockv1alpha1.ClusterHarnessTemplate
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &cht); err != nil {
		return nil, err
	}
	return &resolvedTemplate{Spec: *cht.Spec.DeepCopy(), SourceKind: "ClusterHarnessTemplate", SourceName: cht.Name}, nil
}
