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

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var harnesstemplatelog = logf.Log.WithName("harnesstemplate-resource")

// SetupHarnessTemplateWebhookWithManager registers the validating webhook
// for HarnessTemplate with the manager.
func SetupHarnessTemplateWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &paddockv1alpha1.HarnessTemplate{}).
		WithValidator(&HarnessTemplateCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-harnesstemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=harnesstemplates,verbs=create;update,versions=v1alpha1,name=vharnesstemplate-v1alpha1.kb.io,admissionReviewVersions=v1

// HarnessTemplateCustomValidator validates a HarnessTemplate on admission.
// See docs/adr/0003-template-override-semantics.md for rules.
type HarnessTemplateCustomValidator struct{}

var _ admission.Validator[*paddockv1alpha1.HarnessTemplate] = &HarnessTemplateCustomValidator{}

func (v *HarnessTemplateCustomValidator) ValidateCreate(_ context.Context, tpl *paddockv1alpha1.HarnessTemplate) (admission.Warnings, error) {
	harnesstemplatelog.V(1).Info("validating HarnessTemplate create", "name", tpl.GetName())
	return nil, validateHarnessTemplateSpec(&tpl.Spec, false)
}

func (v *HarnessTemplateCustomValidator) ValidateUpdate(_ context.Context, _, newTpl *paddockv1alpha1.HarnessTemplate) (admission.Warnings, error) {
	harnesstemplatelog.V(1).Info("validating HarnessTemplate update", "name", newTpl.GetName())
	return nil, validateHarnessTemplateSpec(&newTpl.Spec, false)
}

func (v *HarnessTemplateCustomValidator) ValidateDelete(_ context.Context, _ *paddockv1alpha1.HarnessTemplate) (admission.Warnings, error) {
	return nil, nil
}
