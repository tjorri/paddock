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

package session

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// CreateOptions parameterises Create. Name is required; Namespace is
// resolved from the kubeconfig at command time and threaded through.
type CreateOptions struct {
	Namespace   string
	Name        string
	Template    string            // HarnessTemplate name; recorded as default + last template.
	StorageSize resource.Quantity // PVC size; required.
	SeedRepoURL string            // optional; if set, becomes spec.seed.repos[0].URL.
	SeedBranch  string            // optional.
}

// Create creates a new session-labeled Workspace and returns its
// Session projection. Does not wait for the Workspace controller to
// finish seeding — callers that need that should watch separately.
func Create(ctx context.Context, c client.Client, opts CreateOptions) (Session, error) {
	if opts.Name == "" {
		return Session{}, fmt.Errorf("session name is required")
	}
	if opts.Template == "" {
		return Session{}, fmt.Errorf("session template is required")
	}
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
			Labels:    map[string]string{SessionLabel: SessionLabelTrue},
			Annotations: map[string]string{
				DefaultTemplateAnnotation: opts.Template,
				LastTemplateAnnotation:    opts.Template,
			},
		},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Storage:   paddockv1alpha1.WorkspaceStorage{Size: opts.StorageSize},
			Ephemeral: false,
		},
	}
	if opts.SeedRepoURL != "" {
		ws.Spec.Seed = &paddockv1alpha1.WorkspaceSeed{
			Repos: []paddockv1alpha1.WorkspaceGitSource{
				{URL: opts.SeedRepoURL, Branch: opts.SeedBranch},
			},
		}
	}
	if err := c.Create(ctx, ws); err != nil {
		return Session{}, fmt.Errorf("creating workspace %s/%s: %w", opts.Namespace, opts.Name, err)
	}
	return FromWorkspace(ws), nil
}
