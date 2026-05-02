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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/policy"
)

// newPolicyCmd is the umbrella `policy` subcommand — scaffold, list,
// check. All three share the admission-algorithm logic from
// internal/policy so the CLI output never drifts from what the webhook
// would decide.
func newPolicyCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect and scaffold BrokerPolicies",
		Long: "BrokerPolicies are the operator's consent surface: each grant " +
			"lists which credentials, egress destinations, and git repos the " +
			"broker is willing to back for a set of templates. See ADR-0014.",
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newPolicyScaffoldCmd(cfg))
	cmd.AddCommand(newPolicyListCmd(cfg))
	cmd.AddCommand(newPolicyCheckCmd(cfg))
	cmd.AddCommand(newPolicySuggestCmd(cfg))
	return cmd
}

// ---------- policy scaffold ------------------------------------------

type scaffoldOptions struct {
	provider string
	name     string
}

func newPolicyScaffoldCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := scaffoldOptions{}
	cmd := &cobra.Command{
		Use:   "scaffold <template>",
		Short: "Print a starter BrokerPolicy YAML for a template's requires block",
		Long: "Reads the template's spec.requires and emits a BrokerPolicy " +
			"YAML document granting every declared credential + egress + " +
			"gitforge capability. The output is apply-able as-is; " +
			"replace the secretRef names and scope fields before running.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyScaffold(cmd.Context(), cfg, cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.provider, "provider", "",
		"Default provider kind for credential grants (UserSuppliedSecret, AnthropicAPI, GitHubApp, PATPool). "+
			"When unset, the scaffold defaults to UserSuppliedSecret and leaves a TODO.")
	cmd.Flags().StringVar(&opts.name, "name", "",
		"BrokerPolicy metadata.name. Defaults to allow-<template>.")
	return cmd
}

func runPolicyScaffold(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, templateName string, opts scaffoldOptions) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}
	return runPolicyScaffoldFor(ctx, c, ns, cmd.OutOrStdout(), templateName, opts)
}

// runPolicyScaffoldFor is the testable form — tests pass a fake
// client. The command-line entry point wraps this with kubeconfig
// loading.
func runPolicyScaffoldFor(ctx context.Context, c client.Client, ns string, out io.Writer, templateName string, opts scaffoldOptions) error {
	spec, source, err := policy.ResolveTemplate(ctx, c, ns, paddockv1alpha1.TemplateRef{Name: templateName})
	if err != nil {
		return fmt.Errorf("resolving template %q: %w", templateName, err)
	}

	name := opts.name
	if name == "" {
		name = "allow-" + templateName
	}
	bp := buildScaffoldPolicy(name, ns, templateName, spec.Requires, opts.provider)
	b, err := yaml.Marshal(bp)
	if err != nil {
		return fmt.Errorf("encoding BrokerPolicy: %w", err)
	}
	fmt.Fprintf(out,
		"# Scaffolded from template %s (%s). Replace secretRef names + gitRepos scope before applying.\n",
		templateName, source)
	fmt.Fprint(out, string(b))
	return nil
}

// buildScaffoldPolicy translates a requires block into a BrokerPolicy
// with one grant per requirement. A TODO marker is left on sensitive
// fields the operator must fill in.
func buildScaffoldPolicy(name, namespace, templateName string, req paddockv1alpha1.RequireSpec, providerOverride string) *paddockv1alpha1.BrokerPolicy {
	bp := &paddockv1alpha1.BrokerPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: paddockv1alpha1.GroupVersion.String(),
			Kind:       "BrokerPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{templateName},
		},
	}

	for _, c := range req.Credentials {
		kind := providerOverride
		if kind == "" {
			kind = "UserSuppliedSecret"
		}
		grant := paddockv1alpha1.CredentialGrant{
			Name: c.Name,
			Provider: paddockv1alpha1.ProviderConfig{
				Kind: kind,
				SecretRef: &paddockv1alpha1.SecretKeyReference{
					Name: "TODO-replace-" + strings.ToLower(c.Name) + "-secret",
					Key:  defaultSecretKeyForProviderKind(kind),
				},
			},
		}
		if kind == "GitHubApp" {
			grant.Provider.AppID = "TODO-github-app-id"
			grant.Provider.InstallationID = "TODO-github-installation-id"
		}
		bp.Spec.Grants.Credentials = append(bp.Spec.Grants.Credentials, grant)
	}

	for _, e := range req.Egress {
		ports := e.Ports
		if len(ports) == 0 {
			ports = []int32{443}
		}
		bp.Spec.Grants.Egress = append(bp.Spec.Grants.Egress, paddockv1alpha1.EgressGrant{
			Host:  e.Host,
			Ports: ports,
		})
	}
	return bp
}

// defaultSecretKeyForProviderKind returns the Secret-data key each
// provider expects. Operator overrides at apply time are cheap.
func defaultSecretKeyForProviderKind(kind string) string {
	switch kind {
	case "AnthropicAPI":
		return "api-key"
	case "GitHubApp":
		return "private-key"
	case "PATPool":
		return "pool"
	default:
		return "value"
	}
}

// ---------- policy list ----------------------------------------------

func newPolicyListCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List BrokerPolicies in the namespace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			return runPolicyList(cmd.Context(), c, ns, cmd.OutOrStdout())
		},
	}
}

func runPolicyList(ctx context.Context, c client.Client, ns string, out io.Writer) error {
	var list paddockv1alpha1.BrokerPolicyList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return fmt.Errorf("listing BrokerPolicies in %s: %w", ns, err)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTEMPLATES\tCREDENTIALS\tEGRESS\tGIT-REPOS\tAGE")
	items := append([]paddockv1alpha1.BrokerPolicy{}, list.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	for _, bp := range items {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n",
			bp.Name,
			strings.Join(bp.Spec.AppliesToTemplates, ","),
			len(bp.Spec.Grants.Credentials),
			len(bp.Spec.Grants.Egress),
			len(bp.Spec.Grants.GitRepos),
			age(bp.CreationTimestamp))
	}
	return tw.Flush()
}

// ---------- policy check ---------------------------------------------

func newPolicyCheckCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "check <template>",
		Short: "Report whether the namespace's BrokerPolicies admit the template",
		Long: "Runs the admission intersection (ADR-0014) against a template " +
			"resolved in the target namespace. Prints covered requirements " +
			"and any shortfalls, with the same diagnostic the webhook emits.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyCheck(cmd.Context(), cfg, cmd, args[0])
		},
	}
}

func runPolicyCheck(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, templateName string) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}
	return runPolicyCheckFor(ctx, c, ns, cmd.OutOrStdout(), templateName)
}

func runPolicyCheckFor(ctx context.Context, c client.Client, ns string, out io.Writer, templateName string) error {
	spec, source, err := policy.ResolveTemplate(ctx, c, ns, paddockv1alpha1.TemplateRef{Name: templateName})
	if err != nil {
		return fmt.Errorf("resolving template %q: %w", templateName, err)
	}
	result, err := policy.Intersect(ctx, c, ns, templateName, spec.Requires)
	if err != nil {
		return fmt.Errorf("intersecting policies: %w", err)
	}
	printPolicyCheck(out, templateName, source, ns, result)
	if !result.Admitted {
		// Non-zero exit so scripts can gate on this.
		return fmt.Errorf("template %q is not runnable in namespace %q", templateName, ns)
	}
	return nil
}

func printPolicyCheck(out io.Writer, templateName, source, namespace string, result *policy.IntersectionResult) {
	fmt.Fprintf(out, "Template:   %s (%s)\n", templateName, source)
	fmt.Fprintf(out, "Namespace:  %s\n", namespace)
	fmt.Fprintf(out, "Runnable:   %s\n", yesNo(result.Admitted))
	if len(result.MatchedPolicies) > 0 {
		fmt.Fprintf(out, "Policies:   %s\n", strings.Join(result.MatchedPolicies, ", "))
	} else {
		fmt.Fprintln(out, "Policies:   (none)")
	}
	if len(result.CoveredCredentials) > 0 {
		fmt.Fprintln(out, "Covered credentials:")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tPOLICY\tPROVIDER")
		names := make([]string, 0, len(result.CoveredCredentials))
		for n := range result.CoveredCredentials {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			cov := result.CoveredCredentials[n]
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", n, cov.Policy, cov.Provider)
		}
		tw.Flush()
	}
	if result.Admitted {
		return
	}
	if len(result.MissingCredentials) > 0 {
		fmt.Fprintln(out, "Missing credentials:")
		for _, s := range result.MissingCredentials {
			fmt.Fprintf(out, "  - %s\n", s.Name)
		}
	}
	if len(result.MissingEgress) > 0 {
		fmt.Fprintln(out, "Missing egress:")
		for _, s := range result.MissingEgress {
			if s.Port == 0 {
				fmt.Fprintf(out, "  - %s\n", s.Host)
			} else {
				fmt.Fprintf(out, "  - %s:%d\n", s.Host, s.Port)
			}
		}
	}
	fmt.Fprintf(out, "Hint: kubectl paddock policy scaffold %s -n %s\n", templateName, namespace)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ---------- policy suggest -------------------------------------------

// suggestOptions is the parsed flag shape for `paddock policy suggest`.
// --run X and --all are mutually exclusive; exactly one must be set.
// --since is optional; zero means "no lower bound on event timestamp".
type suggestOptions struct {
	runName        string
	allInNamespace bool
	since          time.Duration
}

func newPolicySuggestCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := suggestOptions{}
	cmd := &cobra.Command{
		Use:   "suggest",
		Short: "Suggest BrokerPolicy.spec.grants.egress entries from recent denials",
		Long: "Reads AuditEvent objects in the current namespace (kind=egress-block), " +
			"groups by (host, port), and prints a ready-to-paste grants.egress block. " +
			"Use after a first run to iterate toward a complete allowlist; see the " +
			"Bootstrapping an allowlist section of the v0.3→v0.4 migration doc.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			return runPolicySuggestTo(cmd.Context(), c, ns,
				cmd.OutOrStdout(), cmd.ErrOrStderr(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.runName, "run", "",
		"Limit suggestions to one HarnessRun (matches the paddock.dev/run label on AuditEvents).")
	cmd.Flags().BoolVar(&opts.allInNamespace, "all", false,
		"Aggregate denials across every run in the current namespace. Mutually exclusive with --run.")
	cmd.Flags().DurationVar(&opts.since, "since", 0,
		"Only consider denials newer than this duration (e.g. 1h, 24h). Zero (default) means no lower bound.")
	return cmd
}

// runPolicySuggest is the two-writer form used by the core test suite
// (it routes diagnostics to the same writer as output). Cobra callers
// go through runPolicySuggestTo so the no-denials message lands on
// stderr while the (empty) suggestion lands on stdout.
// ns varies by caller; unparam fires only because all current callers pass testNamespace.
func runPolicySuggest(ctx context.Context, c client.Client, ns string, out io.Writer, opts suggestOptions) error { //nolint:unparam
	return runPolicySuggestTo(ctx, c, ns, out, out, opts)
}

// runPolicySuggestTo is the testable entry point with split writers.
// stdout receives the YAML suggestion; stderr receives the no-denials
// diagnostic. Both writers must be non-nil.
func runPolicySuggestTo(ctx context.Context, c client.Client, ns string,
	stdout, stderr io.Writer, opts suggestOptions) error {
	if opts.runName == "" && !opts.allInNamespace {
		return fmt.Errorf("one of --run or --all is required")
	}
	if opts.runName != "" && opts.allInNamespace {
		return fmt.Errorf("--run and --all are mutually exclusive")
	}

	kindReq, err := labels.NewRequirement(
		paddockv1alpha1.AuditEventLabelKind,
		selection.In,
		[]string{
			string(paddockv1alpha1.AuditKindEgressBlock),
			string(paddockv1alpha1.AuditKindEgressDiscoveryAllow),
		},
	)
	if err != nil {
		return fmt.Errorf("building kind selector: %w", err)
	}
	selector := labels.NewSelector().Add(*kindReq)
	if opts.runName != "" {
		runReq, rErr := labels.NewRequirement(paddockv1alpha1.AuditEventLabelRun, selection.Equals, []string{opts.runName})
		if rErr != nil {
			return fmt.Errorf("building run selector: %w", rErr)
		}
		selector = selector.Add(*runReq)
	}
	var list paddockv1alpha1.AuditEventList
	if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return fmt.Errorf("listing AuditEvents in %s: %w", ns, err)
	}

	var cutoff time.Time
	if opts.since > 0 {
		cutoff = time.Now().UTC().Add(-opts.since)
	}
	groups := groupEgressCandidates(list.Items, cutoff)

	scope := "namespace " + ns
	if opts.runName != "" {
		scope = "run " + opts.runName
	}
	if len(groups) == 0 {
		fmt.Fprintf(stderr, "no recorded egress attempts found for %s\n", scope)
		return nil
	}
	renderSuggestion(stdout, groups, scope)
	return nil
}

// hostPort keys the grouping map. Kept as a value type so map lookups
// don't need pointer dereferencing.
type hostPort struct {
	host string
	port int32
}

// groupEgressCandidates aggregates AuditEvent destinations by (host, port)
// across both egress-block and egress-discovery-allow event kinds, producing
// the candidate set for a policy suggestion. Events older than cutoff are
// dropped; a zero cutoff disables the filter. Events whose Destination is nil
// are skipped silently.
func groupEgressCandidates(events []paddockv1alpha1.AuditEvent, cutoff time.Time) map[hostPort]int {
	out := map[hostPort]int{}
	for _, e := range events {
		if e.Spec.Destination == nil {
			continue
		}
		if !cutoff.IsZero() && e.Spec.Timestamp.Time.Before(cutoff) {
			continue
		}
		key := hostPort{host: e.Spec.Destination.Host, port: e.Spec.Destination.Port}
		out[key]++
	}
	return out
}

// renderSuggestion writes a YAML snippet to w. Output is sorted by
// count desc, host asc for byte-stability across runs. The final
// `ports:` list uses [443] when the port is known, [] when port is 0
// ("any port" sentinel).
func renderSuggestion(w io.Writer, groups map[hostPort]int, scope string) {
	type row struct {
		key   hostPort
		count int
	}
	rows := make([]row, 0, len(groups))
	for k, v := range groups {
		rows = append(rows, row{key: k, count: v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].key.host < rows[j].key.host
	})

	// Column-align the host field so the output is readable at a glance.
	// maxHost tracks the widest hostField including the trailing comma.
	maxHost := 0
	for _, r := range rows {
		quoted := len(r.key.host) + 3 // for the two " and the trailing ,
		if quoted > maxHost {
			maxHost = quoted
		}
	}

	fmt.Fprintf(w, "# Suggested additions for %s (%d distinct destinations):\n", scope, len(rows))
	fmt.Fprintln(w, "spec.grants.egress:")
	for _, r := range rows {
		hostField := fmt.Sprintf("%q,", r.key.host)
		pad := strings.Repeat(" ", maxHost+1-len(hostField))
		portsField := "[]"
		if r.key.port != 0 {
			portsField = fmt.Sprintf("[%d]", r.key.port)
		}
		unit := "attempts"
		if r.count == 1 {
			unit = "attempt"
		}
		fmt.Fprintf(w, "  - { host: %s%sports: %s }    # %2d %s logged\n",
			hostField, pad, portsField, r.count, unit)
	}
}
