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

package session

import (
	"context"
	"fmt"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// TemplateInfo is a flattened view of a HarnessTemplate suited for the
// new-session modal.
type TemplateInfo struct {
	Name        string
	Kind        string // "HarnessTemplate" or "ClusterHarnessTemplate"
	Description string
}

// DescriptionAnnotation lets template authors surface a one-line blurb
// in paddock-tui's picker. Optional; image is the fallback.
const DescriptionAnnotation = "paddock.dev/description"

// ListTemplates returns namespaced HarnessTemplates plus all
// ClusterHarnessTemplates. Sorted by Name. ClusterHarnessTemplate
// errors are tolerated (RBAC may forbid list at cluster scope).
func ListTemplates(ctx context.Context, c client.Client, ns string) ([]TemplateInfo, error) {
	out := []TemplateInfo{}

	var nsList paddockv1alpha1.HarnessTemplateList
	if err := c.List(ctx, &nsList, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("listing HarnessTemplates in %s: %w", ns, err)
	}
	for _, t := range nsList.Items {
		out = append(out, TemplateInfo{
			Name: t.Name, Kind: "HarnessTemplate",
			Description: descOrImage(t.Annotations, t.Spec.Image),
		})
	}

	var clusterList paddockv1alpha1.ClusterHarnessTemplateList
	if err := c.List(ctx, &clusterList); err == nil {
		for _, t := range clusterList.Items {
			out = append(out, TemplateInfo{
				Name: t.Name, Kind: "ClusterHarnessTemplate",
				Description: descOrImage(t.Annotations, t.Spec.Image),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func descOrImage(ann map[string]string, image string) string {
	if v := ann[DescriptionAnnotation]; v != "" {
		return v
	}
	return image
}
