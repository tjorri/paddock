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
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func newStatusCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status <run>",
		Short: "Show detailed status for a HarnessRun",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context(), cfg, cmd, args[0])
		},
	}
}

func runStatus(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, name string) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}
	run := &paddockv1alpha1.HarnessRun{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, run); err != nil {
		return fmt.Errorf("fetching run %s/%s: %w", ns, name, err)
	}
	printRunStatus(cmd.OutOrStdout(), run)
	return nil
}

func printRunStatus(w io.Writer, run *paddockv1alpha1.HarnessRun) {
	fmt.Fprintf(w, "Name:       %s/%s\n", run.Namespace, run.Name)
	fmt.Fprintf(w, "Template:   %s (kind=%s)\n", run.Spec.TemplateRef.Name, orNone(run.Spec.TemplateRef.Kind))
	fmt.Fprintf(w, "Workspace:  %s\n", orNone(run.Status.WorkspaceRef))
	fmt.Fprintf(w, "Job:        %s\n", orNone(run.Status.JobName))
	fmt.Fprintf(w, "Phase:      %s\n", orNone(string(run.Status.Phase)))
	fmt.Fprintf(w, "Start:      %s\n", timeOrNone(run.Status.StartTime))
	fmt.Fprintf(w, "End:        %s\n", timeOrNone(run.Status.CompletionTime))
	fmt.Fprintln(w)

	if len(run.Status.Conditions) > 0 {
		fmt.Fprintln(w, "Conditions:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  TYPE\tSTATUS\tREASON\tMESSAGE")
		for _, c := range run.Status.Conditions {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
				c.Type, c.Status, orNone(c.Reason), truncate(c.Message, 60))
		}
		tw.Flush()
		fmt.Fprintln(w)
	}

	if run.Status.Outputs != nil {
		o := run.Status.Outputs
		fmt.Fprintln(w, "Outputs:")
		if o.Summary != "" {
			fmt.Fprintf(w, "  Summary:       %s\n", o.Summary)
		}
		if o.FilesChanged != 0 {
			fmt.Fprintf(w, "  FilesChanged:  %d\n", o.FilesChanged)
		}
		if len(o.PullRequests) > 0 {
			fmt.Fprintln(w, "  PullRequests:")
			for _, pr := range o.PullRequests {
				fmt.Fprintf(w, "    - %s\n", pr)
			}
		}
		if len(o.Artifacts) > 0 {
			fmt.Fprintln(w, "  Artifacts:")
			for _, a := range o.Artifacts {
				fmt.Fprintf(w, "    - %s\n", a)
			}
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

func timeOrNone(t interface{ IsZero() bool }) string {
	// Tolerates either a *metav1.Time (possibly nil) or metav1.Time.
	switch v := t.(type) {
	case nil:
		return "<none>"
	default:
		if v == nil || v.IsZero() {
			return "<none>"
		}
	}
	return fmt.Sprintf("%v", t)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

var _ = strings.Builder{} // keep strings import stable for future formatters
