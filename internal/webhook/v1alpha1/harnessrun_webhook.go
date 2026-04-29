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
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
	"paddock.dev/paddock/internal/policy"
)

var harnessrunlog = logf.Log.WithName("harnessrun-resource")

// SetupHarnessRunWebhookWithManager registers the validating webhook for
// HarnessRun with the manager. The validator gets the manager's client so
// it can resolve the referenced template and intersect its requires with
// in-namespace BrokerPolicies (ADR-0014). sink receives one AuditEvent
// per admission decision; pass auditing.NoopSink{} in test environments.
func SetupHarnessRunWebhookWithManager(mgr ctrl.Manager, sink auditing.Sink) error {
	return ctrl.NewWebhookManagedBy(mgr, &paddockv1alpha1.HarnessRun{}).
		WithValidator(&HarnessRunCustomValidator{Client: mgr.GetClient(), Sink: sink}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-harnessrun,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=harnessruns,verbs=create;update,versions=v1alpha1,name=vharnessrun-v1alpha1.kb.io,admissionReviewVersions=v1

// HarnessRunCustomValidator enforces HarnessRun spec invariants:
//
//   - exactly one of spec.prompt or spec.promptFrom;
//   - spec.templateRef.name non-empty;
//   - spec.extraEnv values do not use valueFrom in any shape (v0.3:
//     runtime-resolved env values must flow through the broker or an
//     explicit spec field; see ADR-0015 and spec 0002 §5.4);
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
// Sink receives one AuditEvent per admission decision; a nil Sink is
// treated as a no-op (fail-open: audit unavailability never blocks admission).
type HarnessRunCustomValidator struct {
	Client client.Client
	Sink   auditing.Sink
}

var _ admission.Validator[*paddockv1alpha1.HarnessRun] = &HarnessRunCustomValidator{}

func (v *HarnessRunCustomValidator) ValidateCreate(ctx context.Context, run *paddockv1alpha1.HarnessRun) (admission.Warnings, error) {
	harnessrunlog.V(1).Info("validating HarnessRun create", "name", run.GetName())
	var err error
	if err = validateHarnessRunSpec(&run.Spec); err == nil {
		err = v.validateAgainstTemplate(ctx, run)
	}
	v.audit(ctx, run, nil, err)
	return nil, err
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

	var err error
	if !reflect.DeepEqual(oldRun.Spec, newRun.Spec) {
		err = fmt.Errorf("spec is immutable: submit a new HarnessRun to change configuration")
	} else if specErr := validateHarnessRunSpec(&newRun.Spec); specErr != nil {
		// Still run spec validation so a formerly-valid object can't drift
		// through changes to types or defaults.
		err = specErr
	} else {
		err = v.validateAgainstTemplate(ctx, newRun)
	}
	owner := &metav1.OwnerReference{
		APIVersion: paddockv1alpha1.GroupVersion.String(),
		Kind:       "HarnessRun",
		Name:       newRun.Name,
		UID:        newRun.UID,
	}
	v.audit(ctx, newRun, owner, err)
	return nil, err
}

func (v *HarnessRunCustomValidator) ValidateDelete(_ context.Context, _ *paddockv1alpha1.HarnessRun) (admission.Warnings, error) {
	return nil, nil
}

// audit emits one policy-applied (admit) or policy-rejected (reject)
// AuditEvent. Failures are logged but never block the validator's
// decision — admission must not depend on audit availability (F-32).
func (v *HarnessRunCustomValidator) audit(ctx context.Context, run *paddockv1alpha1.HarnessRun, owner *metav1.OwnerReference, err error) {
	if v.Sink == nil {
		return
	}
	in := auditing.AdmissionInput{
		RunName:     run.Name,
		Namespace:   run.Namespace,
		TemplateRef: run.Spec.TemplateRef.Name,
		OwnerRef:    owner,
	}
	var ae *paddockv1alpha1.AuditEvent
	if err == nil {
		in.Reason = "admitted"
		ae = auditing.NewPolicyApplied(in)
	} else {
		in.Reason = err.Error()
		ae = auditing.NewPolicyRejected(in)
	}
	if wErr := v.Sink.Write(ctx, ae); wErr != nil {
		harnessrunlog.Error(wErr, "writing admission AuditEvent",
			"name", run.Name, "namespace", run.Namespace)
	}
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
	matches, err := policy.ListMatchingPolicies(ctx, v.Client, run.Namespace, run.Spec.TemplateRef.Name)
	if err != nil {
		return fmt.Errorf("listing BrokerPolicies: %w", err)
	}
	now := time.Now()
	filtered := policy.FilterUnexpired(matches, now)
	if len(filtered) == 0 && len(matches) > 0 {
		// All matching policies were filtered out for expired discovery.
		// len(matches) > 0 distinguishes "every match was expired" from
		// "no policy matched at all"; the latter is handled by
		// IntersectMatches below producing !result.Admitted with a
		// shortfall list, and gives a more useful error.
		names := make([]string, 0, len(matches))
		for _, bp := range matches {
			names = append(names, bp.Name)
		}
		return fmt.Errorf("BrokerPolicy(s) %s have expired egressDiscovery windows; "+
			"advance or remove spec.egressDiscovery.expiresAt to resume admitting runs",
			strings.Join(names, ", "))
	}
	result := policy.IntersectMatches(filtered, spec.Requires)
	if !result.Admitted {
		return fmt.Errorf("%s", policy.DescribeShortfall(result, run.Spec.TemplateRef.Name, run.Namespace))
	}
	return nil
}

// reservedExtraEnvLiterals are env var names the controller authors
// itself on the agent container. Tenant overrides via spec.extraEnv
// are rejected at admission — HTTPS_PROXY / SSL_CERT_FILE in particular
// are load-bearing for cooperative-mode interception (F-39 / Phase 2e).
var reservedExtraEnvLiterals = map[string]struct{}{
	"HTTPS_PROXY":         {},
	"HTTP_PROXY":          {},
	"NO_PROXY":            {},
	"SSL_CERT_FILE":       {},
	"NODE_EXTRA_CA_CERTS": {},
	"REQUESTS_CA_BUNDLE":  {},
	"GIT_SSL_CAINFO":      {},
}

// reservedExtraEnvPrefix reserves the entire PADDOCK_ namespace for the
// controller. New PADDOCK_* envs added to buildEnv inherit this
// protection without requiring the literal set above to be updated.
const reservedExtraEnvPrefix = "PADDOCK_"

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

	if hasPrompt && len(spec.Prompt) > paddockv1alpha1.MaxInlinePromptBytes {
		errs = append(errs, field.TooLong(specPath.Child("prompt"), "", paddockv1alpha1.MaxInlinePromptBytes))
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

	// The two checks below intentionally both run for each extraEnv
	// entry (no early-return between them) so a single entry that
	// violates both rules — e.g. {Name: "HTTPS_PROXY", ValueFrom:
	// {SecretKeyRef: ...}} — produces both errors in the aggregate.
	// The webhook contract is "report all violations at once" so an
	// operator can fix the spec in one round-trip. See the dual-error
	// Ginkgo test in harnessrun_webhook_test.go.
	for i, e := range spec.ExtraEnv {
		if _, reserved := reservedExtraEnvLiterals[e.Name]; reserved ||
			strings.HasPrefix(e.Name, reservedExtraEnvPrefix) {
			errs = append(errs, field.Forbidden(
				specPath.Child("extraEnv").Index(i).Child("name"),
				"env name is reserved by the controller; "+
					"see docs/internal/specs/0002-broker-proxy-v0.3.md §5.4"))
		}
		if e.ValueFrom != nil {
			// F-31 closes valueFrom to any non-nil shape (was: secretKeyRef
			// only). The broker is the only legitimate channel for
			// runtime-resolved values; if a future use case needs e.g.
			// fieldRef for pod name passthrough, surface it as an explicit
			// HarnessRun spec field. See spec 0002 §5.4.
			errs = append(errs, field.Forbidden(
				specPath.Child("extraEnv").Index(i).Child("valueFrom"),
				"valueFrom is not permitted on extraEnv; use a literal value, "+
					"or declare a credential on the template's requires and grant "+
					"via a BrokerPolicy (see spec 0002 §5.4)"))
		}
	}

	errs = append(errs, validateRunInteractiveSpec(spec, specPath)...)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}

// validateRunInteractiveSpec validates spec.mode and spec.interactiveOverrides.
// It follows the field.ErrorList convention used throughout this file.
func validateRunInteractiveSpec(spec *paddockv1alpha1.HarnessRunSpec, fldPath *field.Path) field.ErrorList {
	if spec.InteractiveOverrides == nil {
		return nil
	}

	var errs field.ErrorList
	overridesPath := fldPath.Child("interactiveOverrides")

	if spec.Mode != paddockv1alpha1.HarnessRunModeInteractive {
		errs = append(errs, field.Forbidden(overridesPath,
			"interactiveOverrides may only be set when spec.mode == Interactive"))
		// No point checking duration values if the mode gate already failed.
		return errs
	}

	type durationField struct {
		name  string
		value *metav1.Duration
	}
	fields := []durationField{
		{"idleTimeout", spec.InteractiveOverrides.IdleTimeout},
		{"detachIdleTimeout", spec.InteractiveOverrides.DetachIdleTimeout},
		{"detachTimeout", spec.InteractiveOverrides.DetachTimeout},
		{"maxLifetime", spec.InteractiveOverrides.MaxLifetime},
	}
	for _, f := range fields {
		if f.value == nil {
			continue
		}
		if f.value.Duration <= 0 {
			errs = append(errs, field.Invalid(
				overridesPath.Child(f.name),
				f.value.Duration,
				"must be positive",
			))
		}
	}

	return errs
}
