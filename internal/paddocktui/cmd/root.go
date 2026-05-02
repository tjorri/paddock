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

// Package cmd implements the paddock-tui binary's cobra command tree.
//
// The TUI is the primary UX: invoking `paddock-tui` with no subcommand
// launches the Bubble Tea program. The non-TUI subcommands
// (`session list`, `session new`, `session end`, `version`) are kept
// for scripting and one-off operations.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(paddockv1alpha1.AddToScheme(scheme))
}

// NewRootCmd builds the root cobra command. With no subcommand the
// TUI launches; with `version`, `session ...` etc. the corresponding
// non-TUI command runs.
func NewRootCmd() *cobra.Command {
	cfg := genericclioptions.NewConfigFlags(true)
	var bo brokerOpts

	root := &cobra.Command{
		Use:           "paddock-tui",
		Short:         "Interactive multi-session TUI for Paddock",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cfg, bo)
		},
	}
	cfg.AddFlags(root.PersistentFlags())
	addBrokerFlags(root, &bo)

	root.AddCommand(newVersionCmd())
	root.AddCommand(newSessionCmd(cfg))
	root.AddCommand(newTUICmd(cfg))

	root.SetErr(os.Stderr)
	root.SetOut(os.Stdout)
	return root
}

// newClient builds a controller-runtime client from the kubectl-style
// config flags. Shared by every subcommand. Returns the resolved
// namespace as the second value.
func newClient(cfg *genericclioptions.ConfigFlags) (client.Client, string, error) {
	restConfig, err := cfg.ToRESTConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, "", fmt.Errorf("building Kubernetes client: %w", err)
	}
	ns, _, err := cfg.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("resolving namespace: %w", err)
	}
	return c, ns, nil
}
