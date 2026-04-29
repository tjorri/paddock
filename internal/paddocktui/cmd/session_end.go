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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

func newSessionEndCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "end NAME",
		Short: "Delete a paddock-tui session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			if !yes {
				if !confirm(os.Stdin, cmd.OutOrStdout(), fmt.Sprintf("End session %q in namespace %q? (y/N) ", args[0], ns)) {
					fmt.Fprintln(cmd.OutOrStdout(), "cancelled")
					return nil
				}
			}
			return runSessionEnd(cmd.Context(), c, ns, args[0], true, cmd.OutOrStdout())
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	return c
}

func runSessionEnd(ctx context.Context, c client.Client, ns, name string, _ bool, out io.Writer) error {
	if err := pdksession.End(ctx, c, ns, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "session %q in %s ended\n", name, ns)
	return nil
}

func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprint(out, prompt)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false
	}
	resp := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return resp == "y" || resp == "yes"
}
