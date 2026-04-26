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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setCondition sets or replaces the condition of the given type on the
// slice. Preserves LastTransitionTime when Status doesn't change.
//
// Used by both reconcilers (HarnessRun and Workspace) and the
// BrokerPolicy reconciler's discovery-conditions helper. Lives in its
// own file so readers of any reconciler can find it in the obvious
// place; future condition helpers (e.g. a unified setConditionWithLTT
// that absorbs applyDiscoveryConditions) can grow alongside it.
func setCondition(conds *[]metav1.Condition, c metav1.Condition) {
	now := metav1.Now()
	for i, existing := range *conds {
		if existing.Type != c.Type {
			continue
		}
		if existing.Status == c.Status {
			c.LastTransitionTime = existing.LastTransitionTime
		} else {
			c.LastTransitionTime = now
		}
		(*conds)[i] = c
		return
	}
	c.LastTransitionTime = now
	*conds = append(*conds, c)
}
