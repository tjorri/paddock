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
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

type auditOptions struct {
	run  string
	kind string
	max  int
}

func newAuditCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := auditOptions{max: 50}
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "List AuditEvents in the namespace",
		Long: "AuditEvents are the security trail Paddock writes for every " +
			"broker decision (credential issuance/denial, egress allow/block) " +
			"and the proxy's MITM outcomes. See ADR-0016.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			return runAudit(cmd.Context(), c, ns, cmd.OutOrStdout(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.run, "run", "",
		"Filter to AuditEvents for one HarnessRun (matches the paddock.dev/run label).")
	cmd.Flags().StringVar(&opts.kind, "kind", "",
		"Filter to a single kind (credential-issued, credential-denied, egress-allow, egress-block, ...).")
	cmd.Flags().IntVar(&opts.max, "limit", 50,
		"Maximum number of rows to print (most recent first). 0 means unlimited.")
	return cmd
}

func runAudit(ctx context.Context, c client.Client, ns string, out io.Writer, opts auditOptions) error {
	var list paddockv1alpha1.AuditEventList
	listOpts := []client.ListOption{client.InNamespace(ns)}
	if opts.run != "" {
		listOpts = append(listOpts, client.MatchingLabels{paddockv1alpha1.AuditEventLabelRun: opts.run})
	}
	if err := c.List(ctx, &list, listOpts...); err != nil {
		return fmt.Errorf("listing AuditEvents in %s: %w", ns, err)
	}

	// Sort newest-first by Timestamp (emitter-set), falling back to
	// creationTimestamp. Optional client-side kind filter.
	items := list.Items
	if opts.kind != "" {
		filtered := items[:0]
		for _, e := range items {
			if string(e.Spec.Kind) == opts.kind {
				filtered = append(filtered, e)
			}
		}
		items = filtered
	}
	sort.Slice(items, func(i, j int) bool {
		ti := items[i].Spec.Timestamp.Time
		if ti.IsZero() {
			ti = items[i].CreationTimestamp.Time
		}
		tj := items[j].Spec.Timestamp.Time
		if tj.IsZero() {
			tj = items[j].CreationTimestamp.Time
		}
		return ti.After(tj)
	})
	if opts.max > 0 && len(items) > opts.max {
		items = items[:opts.max]
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tKIND\tDECISION\tRUN\tTARGET\tREASON")
	for _, e := range items {
		ts := e.Spec.Timestamp.Time
		if ts.IsZero() {
			ts = e.CreationTimestamp.Time
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			ts.UTC().Format("2006-01-02T15:04:05Z"),
			orNone(string(e.Spec.Kind)),
			orNone(string(e.Spec.Decision)),
			auditRunName(&e),
			auditTarget(&e),
			orNone(truncate(e.Spec.Reason, 60)),
		)
	}
	return tw.Flush()
}

// auditRunName prefers the RunRef spec field, falling back to the
// emitter-set label for events written by the proxy sidecar before the
// RunRef convention was adopted (pre-M7).
func auditRunName(e *paddockv1alpha1.AuditEvent) string {
	if e.Spec.RunRef != nil && e.Spec.RunRef.Name != "" {
		return e.Spec.RunRef.Name
	}
	if v := e.Labels[paddockv1alpha1.AuditEventLabelRun]; v != "" {
		return v
	}
	return noneSentinel
}

// auditTarget collapses Destination + Credential into one column so
// the table stays narrow; dispatched by which field the emitter set.
func auditTarget(e *paddockv1alpha1.AuditEvent) string {
	if e.Spec.Destination != nil {
		if e.Spec.Destination.Port == 0 {
			return e.Spec.Destination.Host
		}
		return fmt.Sprintf("%s:%d", e.Spec.Destination.Host, e.Spec.Destination.Port)
	}
	if e.Spec.Credential != nil {
		// provider/name is dense enough for the table; purpose lives on
		// the template, not the event, so skip it here.
		return fmt.Sprintf("%s/%s", orNone(e.Spec.Credential.Provider), e.Spec.Credential.Name)
	}
	return noneSentinel
}
