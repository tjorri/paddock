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
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/policy"
)

func newDescribeCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "describe",
		Short: "Describe a Paddock resource in detail (template | ...)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newDescribeTemplateCmd(cfg))
	return cmd
}

func newDescribeTemplateCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "template <name>",
		Short: "Describe a template — resolved spec, requires, and runnability",
		Long: "Resolves <name> against the namespaced template set with " +
			"fallback to cluster-scoped, prints the merged spec, and runs " +
			"the same admission intersection a HarnessRun webhook would.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribeTemplate(cmd.Context(), cfg, cmd, args[0])
		},
	}
}

func runDescribeTemplate(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, name string) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}
	return runDescribeTemplateFor(ctx, c, ns, cmd.OutOrStdout(), name)
}

func runDescribeTemplateFor(ctx context.Context, c client.Client, ns string, out io.Writer, name string) error {
	spec, source, err := policy.ResolveTemplate(ctx, c, ns, paddockv1alpha1.TemplateRef{Name: name})
	if err != nil {
		return fmt.Errorf("resolving template %q: %w", name, err)
	}
	result, err := policy.Intersect(ctx, c, ns, name, spec.Requires)
	if err != nil {
		return fmt.Errorf("intersecting policies: %w", err)
	}
	printTemplateDescription(out, name, source, ns, spec, result)
	return nil
}

func printTemplateDescription(out io.Writer, name, source, namespace string, spec *paddockv1alpha1.HarnessTemplateSpec, result *policy.IntersectionResult) {
	fmt.Fprintf(out, "Name:       %s\n", name)
	fmt.Fprintf(out, "Source:     %s\n", source)
	fmt.Fprintf(out, "Namespace:  %s\n", namespace)
	fmt.Fprintf(out, "Harness:    %s\n", orNone(spec.Harness))
	fmt.Fprintf(out, "Image:      %s\n", orNone(spec.Image))
	if len(spec.Command) > 0 {
		fmt.Fprintf(out, "Command:    %s\n", strings.Join(spec.Command, " "))
	}
	if len(spec.Args) > 0 {
		fmt.Fprintf(out, "Args:       %s\n", strings.Join(spec.Args, " "))
	}
	if spec.EventAdapter != nil {
		fmt.Fprintf(out, "EventAdapter: %s\n", spec.EventAdapter.Image)
	}
	if spec.Workspace.Required {
		mount := spec.Workspace.MountPath
		if mount == "" {
			mount = "/workspace"
		}
		fmt.Fprintf(out, "Workspace:  required (mount %s)\n", mount)
	}
	fmt.Fprintln(out)

	if len(spec.Requires.Credentials) == 0 && len(spec.Requires.Egress) == 0 {
		fmt.Fprintln(out, "Requires:   (none)")
	} else {
		fmt.Fprintln(out, "Requires:")
		if len(spec.Requires.Credentials) > 0 {
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "  CREDENTIAL")
			for _, c := range spec.Requires.Credentials {
				fmt.Fprintf(tw, "  %s\n", c.Name)
			}
			tw.Flush()
		}
		if len(spec.Requires.Egress) > 0 {
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "  EGRESS\tPORTS")
			for _, e := range spec.Requires.Egress {
				ports := "any"
				if len(e.Ports) > 0 {
					parts := make([]string, len(e.Ports))
					for i, p := range e.Ports {
						parts[i] = fmt.Sprintf("%d", p)
					}
					ports = strings.Join(parts, ",")
				}
				fmt.Fprintf(tw, "  %s\t%s\n", e.Host, ports)
			}
			tw.Flush()
		}
	}
	fmt.Fprintln(out)

	// Runnability hint — same algorithm the webhook runs.
	fmt.Fprintf(out, "Runnable in %s:  %s\n", namespace, yesNo(result.Admitted))
	if len(result.MatchedPolicies) > 0 {
		fmt.Fprintf(out, "Matching policies: %s\n", strings.Join(result.MatchedPolicies, ", "))
	}
	if !result.Admitted {
		fmt.Fprintln(out, "Shortfall:")
		if len(result.MissingCredentials) > 0 {
			for _, s := range result.MissingCredentials {
				fmt.Fprintf(out, "  - missing credential %s\n", s.Name)
			}
		}
		for _, s := range result.MissingEgress {
			if s.Port == 0 {
				fmt.Fprintf(out, "  - missing egress %s\n", s.Host)
			} else {
				fmt.Fprintf(out, "  - missing egress %s:%d\n", s.Host, s.Port)
			}
		}
		fmt.Fprintf(out, "Hint: kubectl paddock policy scaffold %s -n %s\n", name, namespace)
	}
}
