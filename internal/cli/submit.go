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
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

type submitOptions struct {
	template     string
	templateKind string
	workspace    string
	prompt       string
	promptFile   string
	model        string
	timeout      time.Duration
	name         string
	wait         bool
	waitTimeout  time.Duration
}

func newSubmitCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := &submitOptions{}
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a HarnessRun",
		Long: `Submit a HarnessRun against a HarnessTemplate or ClusterHarnessTemplate.

The prompt may be supplied inline (--prompt), from a file (--prompt-file,
or "-" to read stdin), or omitted and provided later by patching the run.

Examples:
  kubectl paddock submit -t echo-default --prompt "hello"
  kubectl paddock submit -t codex --prompt-file task.md --workspace app --wait
  cat task.md | kubectl paddock submit -t codex --prompt-file - --wait`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubmit(cmd.Context(), cfg, cmd, opts)
		},
	}
	cmd.Flags().StringVarP(&opts.template, "template", "t", "", "Template name (required)")
	cmd.Flags().StringVar(&opts.templateKind, "template-kind", "", "Template kind (HarnessTemplate | ClusterHarnessTemplate); resolved automatically when empty")
	cmd.Flags().StringVar(&opts.workspace, "workspace", "", "Workspace to bind (empty = auto-provision ephemeral)")
	cmd.Flags().StringVar(&opts.prompt, "prompt", "", "Inline prompt")
	cmd.Flags().StringVar(&opts.promptFile, "prompt-file", "", "Read prompt from file ('-' for stdin)")
	cmd.Flags().StringVar(&opts.model, "model", "", "Override template's default model")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 0, "Override template's default timeout")
	cmd.Flags().StringVar(&opts.name, "name", "", "Run name (generated when empty)")
	cmd.Flags().BoolVar(&opts.wait, "wait", false, "Block until the run reaches a terminal phase")
	cmd.Flags().DurationVar(&opts.waitTimeout, "wait-timeout", 30*time.Minute, "Max wall time when --wait is set")
	_ = cmd.MarkFlagRequired("template")
	return cmd
}

func runSubmit(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, opts *submitOptions) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}

	run, err := buildRunFromOptions(opts, ns)
	if err != nil {
		return err
	}

	if err := c.Create(ctx, run); err != nil {
		return fmt.Errorf("creating HarnessRun: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "harnessrun.paddock.dev/%s created\n", run.Name)

	if !opts.wait {
		return nil
	}
	return waitForRun(ctx, cmd, c, run.Namespace, run.Name, opts.waitTimeout)
}

// buildRunFromOptions turns CLI flags into a HarnessRun. Exposed for tests.
func buildRunFromOptions(opts *submitOptions, namespace string) (*paddockv1alpha1.HarnessRun, error) {
	if strings.TrimSpace(opts.template) == "" {
		return nil, fmt.Errorf("--template is required")
	}
	if opts.prompt != "" && opts.promptFile != "" {
		return nil, fmt.Errorf("--prompt and --prompt-file are mutually exclusive")
	}
	if opts.prompt == "" && opts.promptFile == "" {
		return nil, fmt.Errorf("one of --prompt or --prompt-file is required")
	}

	prompt := opts.prompt
	if opts.promptFile != "" {
		data, err := readPromptFile(opts.promptFile)
		if err != nil {
			return nil, err
		}
		prompt = data
	}

	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
		},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{
				Name: opts.template,
				Kind: opts.templateKind,
			},
			WorkspaceRef: opts.workspace,
			Prompt:       prompt,
			Model:        opts.model,
		},
	}
	if opts.name != "" {
		run.Name = opts.name
	} else {
		run.GenerateName = "run-"
	}
	if opts.timeout > 0 {
		d := metav1.Duration{Duration: opts.timeout}
		run.Spec.Timeout = &d
	}
	return run, nil
}

func readPromptFile(path string) (string, error) {
	if path == "-" {
		return readPromptCapped(os.Stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening --prompt-file %s: %w", path, err)
	}
	defer f.Close()
	return readPromptCapped(f)
}

// readPromptCapped reads up to MaxInlinePromptBytes from r and rejects
// inputs that exceed the cap. The +1 trick lets us distinguish a
// just-at-cap input (cap bytes read, EOF) from an over-cap input
// (cap+1 bytes read, more available). Takes an io.Reader (rather than
// reading os.Stdin directly) so unit tests can drive the cap-detection
// branch without a child-process harness.
func readPromptCapped(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, paddockv1alpha1.MaxInlinePromptBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading prompt: %w", err)
	}
	if len(data) > paddockv1alpha1.MaxInlinePromptBytes {
		return "", fmt.Errorf(
			"prompt exceeds %d-byte limit (set by the admission webhook); "+
				"use a smaller --prompt-file or split the prompt",
			paddockv1alpha1.MaxInlinePromptBytes)
	}
	return string(data), nil
}

// waitForRun polls until the run reaches a terminal phase or the
// timeout elapses. Prints phase transitions to stdout as they happen.
func waitForRun(ctx context.Context, cmd *cobra.Command, c client.Client, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastPhase paddockv1alpha1.HarnessRunPhase
	var lastReason string
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for run %s/%s after %s", namespace, name, timeout)
		}
		run := &paddockv1alpha1.HarnessRun{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, run); err != nil {
			return fmt.Errorf("fetching run: %w", err)
		}
		reason := latestReason(run)
		if run.Status.Phase != lastPhase || reason != lastReason {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %-10s %s\n",
				time.Now().Format(time.Kitchen), run.Status.Phase, reason)
			lastPhase = run.Status.Phase
			lastReason = reason
		}
		if isTerminal(run.Status.Phase) {
			if run.Status.Phase != paddockv1alpha1.HarnessRunPhaseSucceeded {
				return fmt.Errorf("run ended in phase %s", run.Status.Phase)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func isTerminal(p paddockv1alpha1.HarnessRunPhase) bool {
	switch p {
	case paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed,
		paddockv1alpha1.HarnessRunPhaseCancelled:
		return true
	}
	return false
}

// latestReason returns the first non-True condition's reason, or a
// summary reason for the current phase — useful as a one-line status.
func latestReason(run *paddockv1alpha1.HarnessRun) string {
	// Prefer a non-True lifecycle condition with a reason.
	order := []string{
		paddockv1alpha1.HarnessRunConditionTemplateResolved,
		paddockv1alpha1.HarnessRunConditionWorkspaceBound,
		paddockv1alpha1.HarnessRunConditionJobCreated,
		paddockv1alpha1.HarnessRunConditionPodReady,
		paddockv1alpha1.HarnessRunConditionCompleted,
	}
	for _, t := range order {
		if c := findCondition(run.Status.Conditions, t); c != nil {
			if c.Status == metav1.ConditionFalse && c.Reason != "" {
				return fmt.Sprintf("%s=%s", t, c.Reason)
			}
		}
	}
	if c := findCondition(run.Status.Conditions, paddockv1alpha1.HarnessRunConditionCompleted); c != nil && c.Reason != "" {
		return c.Reason
	}
	return string(run.Status.Phase)
}
