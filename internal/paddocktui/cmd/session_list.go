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

package cmd

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func newSessionListCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List paddock-tui sessions in the current namespace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			return runSessionList(cmd.Context(), c, ns, cmd.OutOrStdout())
		},
	}
}

func runSessionList(ctx context.Context, c client.Client, ns string, out io.Writer) error {
	sessions, err := pdksession.List(ctx, c, ns)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTEMPLATE\tACTIVE-RUN\tRUNS\tLAST-ACTIVITY\tAGE")
	for _, s := range sessions {
		last := "-"
		if !s.LastActivity.IsZero() {
			last = humanDuration(time.Since(s.LastActivity)) + " ago"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			s.Name, s.LastTemplate, dashIfEmpty(s.ActiveRunRef), s.TotalRuns, last,
			humanDuration(time.Since(s.CreationTime)),
		)
	}
	return tw.Flush()
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// humanDuration is a pared-down duration formatter: "5m", "2h13m", "3d",
// "47s". Avoids importing k8s.io/apimachinery's duration helper to keep
// the import surface minimal.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}
