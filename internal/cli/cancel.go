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

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func newCancelCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run>",
		Short: "Delete a HarnessRun (graceful cancel)",
		Long: `Delete a HarnessRun. The controller honours the finalizer and drives
graceful shutdown: the Job is deleted, the workspace binding is released,
and the run's owned resources are cascade-reaped.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCancel(cmd.Context(), cfg, cmd, args[0])
		},
	}
}

func runCancel(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, name string) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, run); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("run %s/%s not found", ns, name)
		}
		return fmt.Errorf("fetching run: %w", err)
	}
	bg := metav1.DeletePropagationBackground
	if err := c.Delete(ctx, run, &client.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting run: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "harnessrun.paddock.dev/%s deletion requested\n", run.Name)
	return nil
}
