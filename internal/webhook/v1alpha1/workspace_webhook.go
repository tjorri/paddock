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

package v1alpha1

import (
	"context"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var workspacelog = logf.Log.WithName("workspace-resource")

// SetupWorkspaceWebhookWithManager registers the validating webhook for
// Workspace with the manager.
func SetupWorkspaceWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &paddockv1alpha1.Workspace{}).
		WithValidator(&WorkspaceCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-workspace,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=workspaces,verbs=create;update,versions=v1alpha1,name=vworkspace-v1alpha1.kb.io,admissionReviewVersions=v1

// WorkspaceCustomValidator enforces Workspace spec invariants:
//
//   - spec.storage.size must be > 0;
//   - if spec.seed is set, exactly one seed source is selected;
//   - spec.storage and spec.seed are immutable after creation.
type WorkspaceCustomValidator struct{}

var _ admission.Validator[*paddockv1alpha1.Workspace] = &WorkspaceCustomValidator{}

func (v *WorkspaceCustomValidator) ValidateCreate(_ context.Context, ws *paddockv1alpha1.Workspace) (admission.Warnings, error) {
	workspacelog.V(1).Info("validating Workspace create", "name", ws.GetName())
	return nil, validateWorkspaceSpec(&ws.Spec)
}

func (v *WorkspaceCustomValidator) ValidateUpdate(_ context.Context, oldWS, newWS *paddockv1alpha1.Workspace) (admission.Warnings, error) {
	workspacelog.V(1).Info("validating Workspace update", "name", newWS.GetName())

	if !reflect.DeepEqual(oldWS.Spec.Storage, newWS.Spec.Storage) {
		return nil, fmt.Errorf("spec.storage is immutable")
	}
	if !reflect.DeepEqual(oldWS.Spec.Seed, newWS.Spec.Seed) {
		return nil, fmt.Errorf("spec.seed is immutable")
	}
	return nil, validateWorkspaceSpec(&newWS.Spec)
}

func (v *WorkspaceCustomValidator) ValidateDelete(_ context.Context, _ *paddockv1alpha1.Workspace) (admission.Warnings, error) {
	return nil, nil
}

func validateWorkspaceSpec(spec *paddockv1alpha1.WorkspaceSpec) error {
	specPath := field.NewPath("spec")
	var errs field.ErrorList

	if spec.Storage.Size.IsZero() {
		errs = append(errs, field.Required(specPath.Child("storage").Child("size"),
			"storage size must be > 0"))
	}

	if spec.Seed != nil {
		// Exactly one seed source. v0.1 ships only Git; add FromArchive in v0.2.
		if spec.Seed.Git == nil {
			errs = append(errs, field.Required(specPath.Child("seed"),
				"at least one seed source (git) must be set"))
		} else if spec.Seed.Git.URL == "" {
			errs = append(errs, field.Required(specPath.Child("seed").Child("git").Child("url"), ""))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}
