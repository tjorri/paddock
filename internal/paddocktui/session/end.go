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

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// End deletes a session-labeled Workspace. Refuses to delete a
// Workspace that isn't a session — paddock-tui only manages its own
// labeled workspaces.
func End(ctx context.Context, c client.Client, ns, name string) error {
	var ws paddockv1alpha1.Workspace
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &ws); err != nil {
		return fmt.Errorf("fetching workspace %s/%s: %w", ns, name, err)
	}
	if !IsSession(&ws) {
		return fmt.Errorf("workspace %s/%s is not a paddock-tui session (missing %q label)", ns, name, SessionLabel)
	}
	if err := c.Delete(ctx, &ws); err != nil {
		return fmt.Errorf("deleting workspace %s/%s: %w", ns, name, err)
	}
	return nil
}
