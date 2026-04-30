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
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	pdkapp "paddock.dev/paddock/internal/paddocktui/app"
	paddockbroker "paddock.dev/paddock/internal/paddocktui/broker"
	pdkui "paddock.dev/paddock/internal/paddocktui/ui"
)

// brokerOpts collects the broker-connectivity flags. Populated from
// cobra flag bindings and passed to runTUI.
type brokerOpts struct {
	service   string
	namespace string
	port      int
	sa        string
	caSecret  string
}

// addBrokerFlags registers the broker-connectivity flags on cmd and
// binds them into opts. Call this for every cobra.Command whose RunE
// invokes runTUI.
func addBrokerFlags(cmd *cobra.Command, opts *brokerOpts) {
	cmd.Flags().StringVar(&opts.service, "broker-service", "paddock-broker", "broker Service name")
	cmd.Flags().StringVar(&opts.namespace, "broker-namespace", "paddock-system", "broker Service namespace")
	cmd.Flags().IntVar(&opts.port, "broker-port", 8443, "broker Service port")
	cmd.Flags().StringVar(&opts.sa, "broker-sa", "default", "ServiceAccount whose token authenticates to the broker (mints audience=paddock-broker tokens)")
	cmd.Flags().StringVar(&opts.caSecret, "broker-ca-secret", "broker-serving-cert", "Secret in --broker-namespace holding the broker's serving CA under key ca.crt")
}

// teaModel adapts pdkapp.Model to Bubble Tea's tea.Model by wiring the
// View method to ui.View. We do this here (in the cmd package, which
// imports both app and ui) to keep the strict separation: app/ doesn't
// know about ui/, ui/ doesn't know about Bubble Tea's tea.Model
// interface.
type teaModel struct {
	pdkapp.Model
	width, height int
}

func (t teaModel) Init() tea.Cmd { return t.Model.Init() }

func (t teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		t.width = ws.Width
		t.height = ws.Height
		return t, nil
	}
	next, cmd := t.Model.Update(msg)
	t.Model = next.(pdkapp.Model)
	return t, cmd
}

func (t teaModel) View() string {
	return pdkui.View(t.Model, t.width, t.height)
}

func newTUICmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	var bo brokerOpts
	c := &cobra.Command{
		Use:    "tui",
		Short:  "Launch the interactive TUI (default action when no subcommand)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cfg, bo)
		},
	}
	addBrokerFlags(c, &bo)
	return c
}

func runTUI(cfg *genericclioptions.ConfigFlags, bo brokerOpts) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}

	restCfg, err := cfg.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig for broker: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	brokerClient, err := paddockbroker.New(ctx, paddockbroker.Options{
		Service:           bo.service,
		Namespace:         bo.namespace,
		Port:              bo.port,
		ServiceAccount:    bo.sa,
		Source:            restCfg,
		CASecretName:      bo.caSecret,
		CASecretNamespace: bo.namespace,
	})
	if err != nil {
		return fmt.Errorf("connect to broker: %w", err)
	}
	defer brokerClient.Close() //nolint:errcheck

	model := pdkapp.NewModel(c, ns)
	model.BrokerClient = brokerClient

	tm := teaModel{Model: model}
	prog := tea.NewProgram(tm, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := prog.Run()
	if err != nil {
		return err
	}
	// Per spec §9: warn the user about queued prompts that were dropped
	// on quit. Bubble Tea exits alt-screen before returning, so stderr
	// writes here land in the regular terminal scrollback.
	if fm, ok := final.(teaModel); ok {
		dropped := []string{}
		for _, name := range fm.SessionOrder {
			s := fm.Sessions[name]
			if s == nil {
				continue
			}
			for _, p := range s.Queue.Items() {
				dropped = append(dropped, fmt.Sprintf("%s: %s", name, truncate(p, 60)))
			}
		}
		if len(dropped) > 0 {
			fmt.Fprintf(os.Stderr, "paddock-tui: %d queued prompt(s) dropped on quit:\n", len(dropped))
			for _, d := range dropped {
				fmt.Fprintf(os.Stderr, "  - %s\n", d)
			}
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
