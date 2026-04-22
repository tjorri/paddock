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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var harnessrunlog = logf.Log.WithName("harnessrun-resource")

// SetupHarnessRunWebhookWithManager registers the validating webhook for
// HarnessRun with the manager.
func SetupHarnessRunWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&paddockv1alpha1.HarnessRun{}).
		WithValidator(&HarnessRunCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-harnessrun,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=harnessruns,verbs=create;update,versions=v1alpha1,name=vharnessrun-v1alpha1.kb.io,admissionReviewVersions=v1

// HarnessRunCustomValidator enforces HarnessRun spec invariants:
//
//   - exactly one of spec.prompt or spec.promptFrom;
//   - spec.templateRef.name non-empty;
//   - spec immutable after creation.
type HarnessRunCustomValidator struct{}

var _ webhook.CustomValidator = &HarnessRunCustomValidator{}

func (v *HarnessRunCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	run, ok := obj.(*paddockv1alpha1.HarnessRun)
	if !ok {
		return nil, fmt.Errorf("expected a HarnessRun object but got %T", obj)
	}
	harnessrunlog.V(1).Info("validating HarnessRun create", "name", run.GetName())
	return nil, validateHarnessRunSpec(&run.Spec)
}

func (v *HarnessRunCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldRun, ok := oldObj.(*paddockv1alpha1.HarnessRun)
	if !ok {
		return nil, fmt.Errorf("expected a HarnessRun object for oldObj but got %T", oldObj)
	}
	newRun, ok := newObj.(*paddockv1alpha1.HarnessRun)
	if !ok {
		return nil, fmt.Errorf("expected a HarnessRun object for newObj but got %T", newObj)
	}
	harnessrunlog.V(1).Info("validating HarnessRun update", "name", newRun.GetName())

	if !reflect.DeepEqual(oldRun.Spec, newRun.Spec) {
		return nil, fmt.Errorf("spec is immutable: submit a new HarnessRun to change configuration")
	}
	// Still run spec validation so a formerly-valid object can't drift
	// through changes to types or defaults.
	return nil, validateHarnessRunSpec(&newRun.Spec)
}

func (v *HarnessRunCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateHarnessRunSpec(spec *paddockv1alpha1.HarnessRunSpec) error {
	specPath := field.NewPath("spec")
	var errs field.ErrorList

	if spec.TemplateRef.Name == "" {
		errs = append(errs, field.Required(specPath.Child("templateRef").Child("name"), ""))
	}

	hasPrompt := spec.Prompt != ""
	hasPromptFrom := spec.PromptFrom != nil
	switch {
	case hasPrompt && hasPromptFrom:
		errs = append(errs, field.Forbidden(specPath,
			"exactly one of prompt or promptFrom may be set"))
	case !hasPrompt && !hasPromptFrom:
		errs = append(errs, field.Required(specPath,
			"one of prompt or promptFrom must be set"))
	}

	if hasPromptFrom {
		pf := spec.PromptFrom
		hasCM := pf.ConfigMapKeyRef != nil
		hasSec := pf.SecretKeyRef != nil
		switch {
		case hasCM && hasSec:
			errs = append(errs, field.Forbidden(specPath.Child("promptFrom"),
				"exactly one of configMapKeyRef or secretKeyRef may be set"))
		case !hasCM && !hasSec:
			errs = append(errs, field.Required(specPath.Child("promptFrom"),
				"one of configMapKeyRef or secretKeyRef must be set"))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}
