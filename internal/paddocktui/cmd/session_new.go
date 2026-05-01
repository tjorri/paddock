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

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

// quantityFlag wraps resource.Quantity so it satisfies pflag.Value.
type quantityFlag struct{ q *resource.Quantity }

func (f *quantityFlag) Set(s string) error {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return err
	}
	*f.q = q
	return nil
}
func (f *quantityFlag) String() string { return f.q.String() }
func (f *quantityFlag) Type() string   { return "quantity" }

type sessionNewOpts struct {
	Namespace   string
	Name        string
	Template    string
	StorageSize resource.Quantity
	SeedRepo    string
	NoTUI       bool
}

func newSessionNewCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := sessionNewOpts{StorageSize: resource.MustParse("10Gi")}
	var bo brokerOpts
	c := &cobra.Command{
		Use:   "new",
		Short: "Create a new paddock-tui session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			opts.Namespace = ns
			if err := runSessionNew(cmd.Context(), cl, opts, cmd.OutOrStdout()); err != nil {
				return err
			}
			if !opts.NoTUI {
				return runTUI(cfg, bo)
			}
			return nil
		},
	}
	c.Flags().StringVar(&opts.Name, "name", "", "Session name (required)")
	c.Flags().StringVar(&opts.Template, "template", "", "HarnessTemplate name (required)")
	c.Flags().StringVar(&opts.SeedRepo, "seed-repo", "", "Optional seed git repo URL")
	c.Flags().Var(&quantityFlag{&opts.StorageSize}, "storage", "PVC size (default 10Gi)")
	c.Flags().BoolVar(&opts.NoTUI, "no-tui", false, "Don't launch the TUI after creation")
	_ = c.MarkFlagRequired("name")
	_ = c.MarkFlagRequired("template")
	addBrokerFlags(c, &bo)
	return c
}

func runSessionNew(ctx context.Context, c client.Client, opts sessionNewOpts, out io.Writer) error {
	s, err := pdksession.Create(ctx, c, pdksession.CreateOptions{
		Namespace:   opts.Namespace,
		Name:        opts.Name,
		Template:    opts.Template,
		StorageSize: opts.StorageSize,
		SeedRepoURL: opts.SeedRepo,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "session %q created in %s (template=%s)\n", s.Name, s.Namespace, s.DefaultTemplate)
	return nil
}
