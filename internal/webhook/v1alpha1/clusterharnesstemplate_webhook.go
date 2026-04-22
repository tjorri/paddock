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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var clusterharnesstemplatelog = logf.Log.WithName("clusterharnesstemplate-resource")

// SetupClusterHarnessTemplateWebhookWithManager registers the validating
// webhook for ClusterHarnessTemplate with the manager.
func SetupClusterHarnessTemplateWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&paddockv1alpha1.ClusterHarnessTemplate{}).
		WithValidator(&ClusterHarnessTemplateCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-clusterharnesstemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=clusterharnesstemplates,verbs=create;update,versions=v1alpha1,name=vclusterharnesstemplate-v1alpha1.kb.io,admissionReviewVersions=v1

// ClusterHarnessTemplateCustomValidator validates a ClusterHarnessTemplate
// on admission. A cluster-scoped template must carry its own pod shape and
// cannot inherit — see docs/adr/0003-template-override-semantics.md.
type ClusterHarnessTemplateCustomValidator struct{}

var _ webhook.CustomValidator = &ClusterHarnessTemplateCustomValidator{}

func (v *ClusterHarnessTemplateCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	tpl, ok := obj.(*paddockv1alpha1.ClusterHarnessTemplate)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterHarnessTemplate object but got %T", obj)
	}
	clusterharnesstemplatelog.V(1).Info("validating ClusterHarnessTemplate create", "name", tpl.GetName())
	return nil, validateHarnessTemplateSpec(&tpl.Spec, true)
}

func (v *ClusterHarnessTemplateCustomValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	tpl, ok := newObj.(*paddockv1alpha1.ClusterHarnessTemplate)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterHarnessTemplate object but got %T", newObj)
	}
	clusterharnesstemplatelog.V(1).Info("validating ClusterHarnessTemplate update", "name", tpl.GetName())
	return nil, validateHarnessTemplateSpec(&tpl.Spec, true)
}

func (v *ClusterHarnessTemplateCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
