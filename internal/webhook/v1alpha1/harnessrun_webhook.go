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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/policy"
)

var harnessrunlog = logf.Log.WithName("harnessrun-resource")

// SetupHarnessRunWebhookWithManager registers the validating webhook for
// HarnessRun with the manager. The validator gets the manager's client so
// it can resolve the referenced template and intersect its requires with
// in-namespace BrokerPolicies (ADR-0014).
func SetupHarnessRunWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &paddockv1alpha1.HarnessRun{}).
		WithValidator(&HarnessRunCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-harnessrun,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=harnessruns,verbs=create;update,versions=v1alpha1,name=vharnessrun-v1alpha1.kb.io,admissionReviewVersions=v1

// HarnessRunCustomValidator enforces HarnessRun spec invariants:
//
//   - exactly one of spec.prompt or spec.promptFrom;
//   - spec.templateRef.name non-empty;
//   - spec.extraEnv values do not source from Secrets (v0.3: credentials
//     flow through the broker; see ADR-0015);
//   - spec immutable after creation;
//   - (v0.3, M2 placeholder) the referenced template must not declare a
//     non-empty requires block until the broker lands in M3. Admission
//     against such templates is rejected with a clear diagnostic; the
//     full BrokerPolicy intersection algorithm replaces this check in
//     M3 (ADR-0014).
//
// Client is optional — test code constructs the validator without one,
// which skips the cross-object requires check. Production installs
// always wire the manager's client via SetupHarnessRunWebhookWithManager.
type HarnessRunCustomValidator struct {
	Client client.Client
}

var _ admission.Validator[*paddockv1alpha1.HarnessRun] = &HarnessRunCustomValidator{}

func (v *HarnessRunCustomValidator) ValidateCreate(ctx context.Context, run *paddockv1alpha1.HarnessRun) (admission.Warnings, error) {
	harnessrunlog.V(1).Info("validating HarnessRun create", "name", run.GetName())
	if err := validateHarnessRunSpec(&run.Spec); err != nil {
		return nil, err
	}
	return nil, v.validateAgainstTemplate(ctx, run)
}

func (v *HarnessRunCustomValidator) ValidateUpdate(ctx context.Context, oldRun, newRun *paddockv1alpha1.HarnessRun) (admission.Warnings, error) {
	harnessrunlog.V(1).Info("validating HarnessRun update", "name", newRun.GetName())

	// Terminating updates must be admitted unconditionally: the only
	// non-status change the reconciler makes on a deleting HarnessRun
	// is removing the `paddock.dev/harnessrun-finalizer`, and that
	// update needs to survive even when the run's BrokerPolicy has
	// already been deleted. Running the template→policy intersection
	// here would deny finalizer clearance, pinning the run (and its
	// namespace) in Terminating forever. Observed in CI run
	// 24880620880 where AfterAll's kubectl delete ns cascaded the
	// BrokerPolicy delete before the controller finished processing
	// the HarnessRun finalizer.
	if !newRun.DeletionTimestamp.IsZero() {
		return nil, nil
	}

	if !reflect.DeepEqual(oldRun.Spec, newRun.Spec) {
		return nil, fmt.Errorf("spec is immutable: submit a new HarnessRun to change configuration")
	}
	// Still run spec validation so a formerly-valid object can't drift
	// through changes to types or defaults.
	if err := validateHarnessRunSpec(&newRun.Spec); err != nil {
		return nil, err
	}
	return nil, v.validateAgainstTemplate(ctx, newRun)
}

func (v *HarnessRunCustomValidator) ValidateDelete(_ context.Context, _ *paddockv1alpha1.HarnessRun) (admission.Warnings, error) {
	return nil, nil
}

// validateAgainstTemplate resolves the run's template and runs the
// ADR-0014 intersection: template.requires must be covered by the
// union of matching BrokerPolicy grants. Returns nil when the
// validator has no client (tests) or the template is not yet present
// (the reconciler will produce a clearer TemplateNotFound error).
func (v *HarnessRunCustomValidator) validateAgainstTemplate(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	if v.Client == nil {
		return nil
	}
	spec, _, err := policy.ResolveTemplate(ctx, v.Client, run.Namespace, run.Spec.TemplateRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("resolving template: %w", err)
	}
	if policy.RequiresEmpty(spec.Requires) {
		return nil
	}
	result, err := policy.Intersect(ctx, v.Client, run.Namespace, run.Spec.TemplateRef.Name, spec.Requires)
	if err != nil {
		return fmt.Errorf("intersecting BrokerPolicies: %w", err)
	}
	if !result.Admitted {
		return fmt.Errorf("%s", policy.DescribeShortfall(result, run.Spec.TemplateRef.Name, run.Namespace))
	}

	// Intersection admitted; now check the interception-mode floor
	// (ADR-0013 §26). A BrokerPolicy may refuse to back runs that would
	// downgrade to cooperative mode due to PSA on the namespace.
	matchingPolicies, err := policy.ListMatchingPolicies(ctx, v.Client, run.Namespace, run.Spec.TemplateRef.Name)
	if err != nil {
		return fmt.Errorf("listing matching BrokerPolicies: %w", err)
	}
	mode, floor, err := policy.ResolveInterceptionMode(ctx, v.Client, run.Namespace, matchingPolicies)
	if err != nil {
		return fmt.Errorf("resolving interception mode: %w", err)
	}
	if floor.Policy != "" {
		return fmt.Errorf("%s", policy.DescribeModeFloorRejection(run.Namespace, mode, floor))
	}
	return nil
}

// MaxInlinePromptBytes caps spec.prompt at 256 KiB, well under the
// 1 MiB ConfigMap/Secret ceiling and leaving headroom for the
// materialisation wrapper. promptFrom sources are not size-checked at
// admission time — doing so would require cluster reads and make
// validation non-static; oversized Secret/ConfigMap-sourced prompts
// fail later at the reconciler's materialise step.
const MaxInlinePromptBytes = 256 * 1024

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

	if hasPrompt && len(spec.Prompt) > MaxInlinePromptBytes {
		errs = append(errs, field.TooLong(specPath.Child("prompt"), "", MaxInlinePromptBytes))
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

	// v0.3: spec.extraEnv may not source values from Secrets. The broker
	// is the only path for credential injection — a SecretKeyRef here
	// bypasses the audit trail. See spec 0002 §5.4.
	for i, e := range spec.ExtraEnv {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			errs = append(errs, field.Forbidden(
				specPath.Child("extraEnv").Index(i).Child("valueFrom").Child("secretKeyRef"),
				"secret-valued env vars must flow through the broker; "+
					"declare the credential on the template's requires and grant it via a BrokerPolicy"))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}
