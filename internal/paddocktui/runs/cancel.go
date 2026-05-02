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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Cancel terminates an in-flight HarnessRun by deleting it with
// background propagation. This mirrors the mechanism used by
// `kubectl paddock cancel`: the controller honours the finalizer and
// drives graceful shutdown — the Job is deleted, the workspace binding
// is released, and owned resources are cascade-reaped.
//
// The logic is duplicated from internal/cli rather than imported, per
// the no-internal-import rule for this package.
func Cancel(ctx context.Context, c client.Client, ns, name string) error {
	var hr paddockv1alpha1.HarnessRun
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &hr); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("run %s/%s not found", ns, name)
		}
		return fmt.Errorf("fetching run %s/%s: %w", ns, name, err)
	}
	bg := metav1.DeletePropagationBackground
	if err := c.Delete(ctx, &hr, &client.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting run %s/%s: %w", ns, name, err)
	}
	return nil
}
