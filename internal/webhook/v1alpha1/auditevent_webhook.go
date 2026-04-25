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

var auditeventlog = logf.Log.WithName("auditevent-resource")

// SetupAuditEventWebhookWithManager registers the validating webhook for
// AuditEvent. AuditEvents are write-once: spec is sealed at creation.
func SetupAuditEventWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &paddockv1alpha1.AuditEvent{}).
		WithValidator(&AuditEventCustomValidator{}).
		Complete()
}

// failurePolicy=ignore on this validator only — write-once enforcement
// is bypassed during webhook outages, but the AuditEvent emit path
// across broker/proxy/controller must NOT depend on the controller
// pod's webhook server being up. Phase 2c F-33; see docs/adr/0016.
// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-auditevent,mutating=false,failurePolicy=ignore,sideEffects=None,groups=paddock.dev,resources=auditevents,verbs=create;update,versions=v1alpha1,name=vauditevent-v1alpha1.kb.io,admissionReviewVersions=v1

// AuditEventCustomValidator enforces the write-once invariant and
// shape-checks the spec on create. See ADR-0016.
type AuditEventCustomValidator struct{}

var _ admission.Validator[*paddockv1alpha1.AuditEvent] = &AuditEventCustomValidator{}

func (v *AuditEventCustomValidator) ValidateCreate(_ context.Context, ae *paddockv1alpha1.AuditEvent) (admission.Warnings, error) {
	auditeventlog.V(1).Info("validating AuditEvent create", "name", ae.GetName())
	return nil, validateAuditEventSpec(&ae.Spec)
}

func (v *AuditEventCustomValidator) ValidateUpdate(_ context.Context, oldAE, newAE *paddockv1alpha1.AuditEvent) (admission.Warnings, error) {
	auditeventlog.V(1).Info("validating AuditEvent update", "name", newAE.GetName())
	if !reflect.DeepEqual(oldAE.Spec, newAE.Spec) {
		return nil, fmt.Errorf("spec is immutable: AuditEvent is write-once (ADR-0016)")
	}
	return nil, nil
}

func (v *AuditEventCustomValidator) ValidateDelete(_ context.Context, _ *paddockv1alpha1.AuditEvent) (admission.Warnings, error) {
	return nil, nil
}

func validateAuditEventSpec(spec *paddockv1alpha1.AuditEventSpec) error {
	specPath := field.NewPath("spec")
	var errs field.ErrorList

	if spec.Decision == "" {
		errs = append(errs, field.Required(specPath.Child("decision"), ""))
	}
	if spec.Kind == "" {
		errs = append(errs, field.Required(specPath.Child("kind"), ""))
	}
	if spec.Timestamp.IsZero() {
		errs = append(errs, field.Required(specPath.Child("timestamp"), ""))
	}

	if spec.Kind == paddockv1alpha1.AuditKindEgressBlockSummary {
		if spec.Count < 1 {
			errs = append(errs, field.Required(specPath.Child("count"),
				"summary events must set count ≥ 1"))
		}
		if spec.WindowStart == nil || spec.WindowEnd == nil {
			errs = append(errs, field.Required(specPath.Child("windowStart"),
				"summary events must set windowStart and windowEnd"))
		}
	} else if spec.Count != 0 && spec.Count != 1 {
		errs = append(errs, field.Invalid(specPath.Child("count"), spec.Count,
			"count is only valid on summary kinds; leave zero for single events"))
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}
