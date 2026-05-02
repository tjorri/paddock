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

// Package runs wraps HarnessRun create/watch/cancel operations from the
// paddock-tui's perspective. It is independent of internal/cli — the
// no-internal-import rule keeps paddock-tui easy to lift out.
package runs

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// CreateOptions parameterises Create.
type CreateOptions struct {
	Namespace    string
	WorkspaceRef string
	Template     string
	Prompt       string
	// Mode selects between Batch (zero value) and Interactive. Defaults
	// to Batch so existing callers stay green.
	Mode paddockv1alpha1.HarnessRunMode
}

// Create creates a HarnessRun against an existing Workspace. The
// HarnessRun's metadata.generateName uses the workspace name as a
// prefix so user-visible run names are easy to associate with the
// session (e.g. starlight-7-abcde).
//
// Returns the generated name.
func Create(ctx context.Context, c client.Client, opts CreateOptions) (string, error) {
	if opts.WorkspaceRef == "" || opts.Template == "" || opts.Prompt == "" {
		return "", fmt.Errorf("workspace, template, and prompt are all required")
	}
	hr := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    opts.Namespace,
			GenerateName: opts.WorkspaceRef + "-",
		},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef:  paddockv1alpha1.TemplateRef{Name: opts.Template},
			WorkspaceRef: opts.WorkspaceRef,
			Prompt:       opts.Prompt,
			Mode:         opts.Mode,
		},
	}
	if err := c.Create(ctx, hr); err != nil {
		return "", fmt.Errorf("creating HarnessRun: %w", err)
	}
	return hr.Name, nil
}
