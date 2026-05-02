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
	"text/tabwriter"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func newListCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:       "list <resource>",
		Short:     "List Paddock resources (runs | workspaces | templates | clustertemplates)",
		Long:      "List Paddock resources in the target namespace. `clustertemplates` is cluster-scoped and ignores the namespace flag.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"runs", "workspaces", "templates", "clustertemplates"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd.Context(), cfg, cmd, args[0])
		},
	}
	return cmd
}

func runList(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, kind string) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}
	switch kind {
	case "runs", "run", "hr", "harnessrun", "harnessruns":
		return listRuns(ctx, c, ns, cmd.OutOrStdout())
	case "workspaces", "workspace", "ws":
		return listWorkspaces(ctx, c, ns, cmd.OutOrStdout())
	case "templates", "template", "ht":
		return listTemplates(ctx, c, ns, cmd.OutOrStdout())
	case "clustertemplates", "clustertemplate", "cht":
		return listClusterTemplates(ctx, c, cmd.OutOrStdout())
	default:
		return fmt.Errorf("unknown resource %q (try runs | workspaces | templates | clustertemplates)", kind)
	}
}

func listRuns(ctx context.Context, c client.Client, ns string, out io.Writer) error {
	var list paddockv1alpha1.HarnessRunList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPHASE\tTEMPLATE\tWORKSPACE\tAGE")
	for _, r := range list.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.Name, orNone(string(r.Status.Phase)),
			r.Spec.TemplateRef.Name, orNone(r.Status.WorkspaceRef),
			age(r.CreationTimestamp))
	}
	return tw.Flush()
}

func listWorkspaces(ctx context.Context, c client.Client, ns string, out io.Writer) error {
	var list paddockv1alpha1.WorkspaceList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPHASE\tACTIVE-RUN\tRUNS\tAGE")
	for _, w := range list.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			w.Name, orNone(string(w.Status.Phase)),
			orNone(w.Status.ActiveRunRef), w.Status.TotalRuns,
			age(w.CreationTimestamp))
	}
	return tw.Flush()
}

func listTemplates(ctx context.Context, c client.Client, ns string, out io.Writer) error {
	var list paddockv1alpha1.HarnessTemplateList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tHARNESS\tBASE\tIMAGE\tAGE")
	for _, t := range list.Items {
		base := ""
		if t.Spec.BaseTemplateRef != nil {
			base = t.Spec.BaseTemplateRef.Name
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			t.Name, orNone(t.Spec.Harness), orNone(base), orNone(t.Spec.Image),
			age(t.CreationTimestamp))
	}
	return tw.Flush()
}

func listClusterTemplates(ctx context.Context, c client.Client, out io.Writer) error {
	var list paddockv1alpha1.ClusterHarnessTemplateList
	if err := c.List(ctx, &list); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tHARNESS\tIMAGE\tAGE")
	for _, t := range list.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			t.Name, orNone(t.Spec.Harness), orNone(t.Spec.Image),
			age(t.CreationTimestamp))
	}
	return tw.Flush()
}
