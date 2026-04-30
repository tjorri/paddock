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

package runs

import (
	"context"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// List returns HarnessRuns in ns whose Spec.WorkspaceRef matches.
// Ordered newest-first by CreationTimestamp so callers can pick the
// freshest match for a workspace (e.g. when reattaching the TUI to a
// bound interactive run).
func List(ctx context.Context, c client.Client, ns, workspaceRef string) ([]paddockv1alpha1.HarnessRun, error) {
	var all paddockv1alpha1.HarnessRunList
	if err := c.List(ctx, &all, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	out := make([]paddockv1alpha1.HarnessRun, 0, len(all.Items))
	for _, r := range all.Items {
		if r.Spec.WorkspaceRef == workspaceRef {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreationTimestamp.After(out[j].CreationTimestamp.Time)
	})
	return out, nil
}
