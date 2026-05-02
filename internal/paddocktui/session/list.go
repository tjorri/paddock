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
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// List returns sessions in ns, filtered by SessionLabel and sorted by
// LastActivity desc (CreationTime as tiebreaker).
func List(ctx context.Context, c client.Client, ns string) ([]Session, error) {
	var wsList paddockv1alpha1.WorkspaceList
	if err := c.List(ctx, &wsList, client.InNamespace(ns), client.MatchingLabels{SessionLabel: SessionLabelTrue}); err != nil {
		return nil, fmt.Errorf("listing workspaces in %s: %w", ns, err)
	}
	out := make([]Session, 0, len(wsList.Items))
	for i := range wsList.Items {
		out = append(out, FromWorkspace(&wsList.Items[i]))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return activitySortKey(out[i]).After(activitySortKey(out[j]))
	})
	return out, nil
}

func activitySortKey(s Session) time.Time {
	if !s.LastActivity.IsZero() {
		return s.LastActivity
	}
	return s.CreationTime
}
