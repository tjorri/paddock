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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
	"paddock.dev/paddock/internal/policy"
)

var brokerpolicylog = logf.Log.WithName("brokerpolicy-resource")

// MaxDiscoveryWindow caps egressDiscovery.expiresAt to keep discovery
// windows short-lived. Operators who want a different cap need an
// operator-flag-tunable variant (deferred from v0.4).
const MaxDiscoveryWindow = 7 * 24 * time.Hour

// SetupBrokerPolicyWebhookWithManager registers the validating webhook
// for BrokerPolicy with the manager. sink receives one AuditEvent per
// admission decision; pass auditing.NoopSink{} in test environments.
func SetupBrokerPolicyWebhookWithManager(mgr ctrl.Manager, sink auditing.Sink) error {
	return ctrl.NewWebhookManagedBy(mgr, &paddockv1alpha1.BrokerPolicy{}).
		WithValidator(&BrokerPolicyCustomValidator{Sink: sink}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-brokerpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=brokerpolicies,verbs=create;update,versions=v1alpha1,name=vbrokerpolicy-v1alpha1.kb.io,admissionReviewVersions=v1

// BrokerPolicyCustomValidator enforces BrokerPolicy spec invariants:
//
//   - appliesToTemplates has at least one entry;
//   - every grant has the fields its provider kind requires;
//   - UserSuppliedSecret declares deliveryMode (proxyInjected or inContainer);
//   - built-in providers do not set deliveryMode;
//   - credential names are unique within the policy;
//   - egress hosts are non-empty and wildcard-valid;
//   - every proxy-injected host is covered by an egress grant;
//   - git repo tuples are complete;
//   - spec.interception, when present, has exactly one of transparent
//     or cooperativeAccepted (with accepted=true and a written reason);
//   - spec.egressDiscovery, when present, has accepted=true, a reason
//     ≥20 chars, and expiresAt in (now, now+7d].
//
// Sink receives one AuditEvent per admission decision; a nil Sink is
// treated as a no-op (fail-open: audit unavailability never blocks admission).
type BrokerPolicyCustomValidator struct {
	Sink auditing.Sink
}

var _ admission.Validator[*paddockv1alpha1.BrokerPolicy] = &BrokerPolicyCustomValidator{}

func (v *BrokerPolicyCustomValidator) ValidateCreate(ctx context.Context, bp *paddockv1alpha1.BrokerPolicy) (admission.Warnings, error) {
	brokerpolicylog.V(1).Info("validating BrokerPolicy create", "name", bp.GetName())
	err := validateBrokerPolicySpec(&bp.Spec, time.Now())
	owner := &metav1.OwnerReference{
		APIVersion: paddockv1alpha1.GroupVersion.String(),
		Kind:       "BrokerPolicy",
		Name:       bp.Name,
		UID:        bp.UID,
	}
	v.audit(ctx, bp, owner, err)
	return nil, err
}

func (v *BrokerPolicyCustomValidator) ValidateUpdate(ctx context.Context, _, newBP *paddockv1alpha1.BrokerPolicy) (admission.Warnings, error) {
	brokerpolicylog.V(1).Info("validating BrokerPolicy update", "name", newBP.GetName())
	err := validateBrokerPolicySpec(&newBP.Spec, time.Now())
	owner := &metav1.OwnerReference{
		APIVersion: paddockv1alpha1.GroupVersion.String(),
		Kind:       "BrokerPolicy",
		Name:       newBP.Name,
		UID:        newBP.UID,
	}
	v.audit(ctx, newBP, owner, err)
	return nil, err
}

func (v *BrokerPolicyCustomValidator) ValidateDelete(_ context.Context, _ *paddockv1alpha1.BrokerPolicy) (admission.Warnings, error) {
	return nil, nil
}

// audit emits one policy-applied (admit) or policy-rejected (reject)
// AuditEvent. Failures are logged but never block the validator's
// decision — admission must not depend on audit availability (F-32).
func (v *BrokerPolicyCustomValidator) audit(ctx context.Context, bp *paddockv1alpha1.BrokerPolicy, owner *metav1.OwnerReference, err error) {
	if v.Sink == nil {
		return
	}
	in := auditing.AdmissionInput{
		Namespace: bp.Namespace,
		OwnerRef:  owner,
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
		brokerpolicylog.Error(wErr, "writing admission AuditEvent",
			"name", bp.Name, "namespace", bp.Namespace)
	}
}

func validateBrokerPolicySpec(spec *paddockv1alpha1.BrokerPolicySpec, now time.Time) error {
	specPath := field.NewPath("spec")
	var errs field.ErrorList

	if len(spec.AppliesToTemplates) == 0 {
		errs = append(errs, field.Required(specPath.Child("appliesToTemplates"),
			"at least one template selector is required"))
	}
	for i, sel := range spec.AppliesToTemplates {
		entry := specPath.Child("appliesToTemplates").Index(i)
		trimmed := strings.TrimSpace(sel)
		if trimmed == "" {
			errs = append(errs, field.Invalid(entry, sel, "selector must not be empty"))
			continue
		}
		if err := policy.ValidateAppliesToTemplate(trimmed); err != nil {
			errs = append(errs, field.Invalid(entry, sel,
				"selector is not a valid glob pattern: "+err.Error()))
		}
	}

	grantsPath := specPath.Child("grants")
	errs = append(errs, validateCredentialGrants(grantsPath.Child("credentials"), spec.Grants.Credentials)...)
	errs = append(errs, validateEgressGrants(grantsPath.Child("egress"), spec.Grants.Egress)...)
	errs = append(errs, validateGitRepoGrants(grantsPath.Child("gitRepos"), spec.Grants.GitRepos)...)
	errs = append(errs, validateCredentialHostsCoveredByEgress(grantsPath, spec.Grants)...)
	errs = append(errs, validateInterception(specPath.Child("interception"), spec.Interception)...)
	errs = append(errs, validateEgressDiscovery(specPath.Child("egressDiscovery"), spec.EgressDiscovery, now)...)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}

func validateCredentialGrants(p *field.Path, grants []paddockv1alpha1.CredentialGrant) field.ErrorList {
	var errs field.ErrorList
	seen := map[string]int{}
	for i, g := range grants {
		entry := p.Index(i)
		if g.Name == "" {
			errs = append(errs, field.Required(entry.Child("name"), ""))
			continue
		}
		if prev, ok := seen[g.Name]; ok {
			errs = append(errs, field.Invalid(entry.Child("name"), g.Name,
				fmt.Sprintf(`name "%s" collides with credentials[%d].name`, g.Name, prev)))
			continue
		}
		seen[g.Name] = i
		errs = append(errs, validateProviderConfig(entry.Child("provider"), g.Provider)...)
	}
	return errs
}

func validateProviderConfig(p *field.Path, cfg paddockv1alpha1.ProviderConfig) field.ErrorList {
	var errs field.ErrorList
	if cfg.Kind == "" {
		errs = append(errs, field.Required(p.Child("kind"), ""))
		return errs
	}
	if cfg.Kind != "UserSuppliedSecret" && cfg.DeliveryMode != nil {
		errs = append(errs, field.Forbidden(p.Child("deliveryMode"),
			"deliveryMode is only valid for UserSuppliedSecret"))
	}
	switch cfg.Kind {
	case "UserSuppliedSecret":
		if cfg.SecretRef == nil {
			errs = append(errs, field.Required(p.Child("secretRef"),
				"UserSuppliedSecret requires secretRef"))
		}
		if cfg.AppID != "" || cfg.InstallationID != "" {
			errs = append(errs, field.Forbidden(p,
				"UserSuppliedSecret must not set appId or installationId"))
		}
		if len(cfg.Hosts) > 0 {
			errs = append(errs, field.Forbidden(p.Child("hosts"),
				"for UserSuppliedSecret, hosts live under deliveryMode.proxyInjected.hosts"))
		}
		errs = append(errs, validateDeliveryMode(p.Child("deliveryMode"), cfg.DeliveryMode)...)
	case "AnthropicAPI":
		if cfg.SecretRef == nil {
			errs = append(errs, field.Required(p.Child("secretRef"),
				fmt.Sprintf("provider kind %q requires secretRef", cfg.Kind)))
		}
		if cfg.AppID != "" || cfg.InstallationID != "" {
			errs = append(errs, field.Forbidden(p,
				fmt.Sprintf("provider kind %q must not set appId or installationId", cfg.Kind)))
		}
	case "PATPool":
		if cfg.SecretRef == nil {
			errs = append(errs, field.Required(p.Child("secretRef"),
				fmt.Sprintf("provider kind %q requires secretRef", cfg.Kind)))
		}
		if cfg.AppID != "" || cfg.InstallationID != "" {
			errs = append(errs, field.Forbidden(p,
				fmt.Sprintf("provider kind %q must not set appId or installationId", cfg.Kind)))
		}
		if len(cfg.Hosts) == 0 {
			// F-09: PATPool has no built-in default host (PATs are git-account-
			// scoped, not service-scoped); operators must declare which hosts
			// the leased PATs may be substituted on so a leaked bearer can't
			// be replayed against an unrelated upstream.
			errs = append(errs, field.Required(p.Child("hosts"),
				"PATPool requires hosts: list the destinations these PATs may be substituted on"))
		}
	case "GitHubApp":
		if cfg.AppID == "" {
			errs = append(errs, field.Required(p.Child("appId"), "required for GitHubApp provider"))
		}
		if cfg.InstallationID == "" {
			errs = append(errs, field.Required(p.Child("installationId"), "required for GitHubApp provider"))
		}
		if cfg.SecretRef == nil {
			errs = append(errs, field.Required(p.Child("secretRef"),
				"required for GitHubApp provider (holds the app private key)"))
		}
	}
	if cfg.SecretRef != nil {
		if cfg.SecretRef.Name == "" {
			errs = append(errs, field.Required(p.Child("secretRef").Child("name"), ""))
		}
		if cfg.SecretRef.Key == "" {
			errs = append(errs, field.Required(p.Child("secretRef").Child("key"), ""))
		}
	}
	errs = append(errs, validateHosts(p.Child("hosts"), cfg.Hosts)...)
	return errs
}

func validateDeliveryMode(p *field.Path, dm *paddockv1alpha1.DeliveryMode) field.ErrorList {
	var errs field.ErrorList
	if dm == nil {
		errs = append(errs, field.Required(p,
			`provider "UserSuppliedSecret" requires deliveryMode. Set deliveryMode.proxyInjected (with hosts + one of header/queryParam/basicAuth) to inject the real value at the proxy, or deliveryMode.inContainer (with accepted=true and a reason) to accept that the secret will be visible to the agent container.`))
		return errs
	}
	count := 0
	if dm.ProxyInjected != nil {
		count++
	}
	if dm.InContainer != nil {
		count++
	}
	switch count {
	case 0:
		errs = append(errs, field.Invalid(p, "", "exactly one of proxyInjected or inContainer must be set"))
	case 1:
		// fine
	default:
		errs = append(errs, field.Invalid(p, "",
			"exactly one of proxyInjected or inContainer must be set; both were provided"))
	}
	if dm.ProxyInjected != nil {
		errs = append(errs, validateProxyInjected(p.Child("proxyInjected"), dm.ProxyInjected)...)
	}
	if dm.InContainer != nil {
		errs = append(errs, validateInContainer(p.Child("inContainer"), dm.InContainer)...)
	}
	return errs
}

func validateProxyInjected(p *field.Path, pi *paddockv1alpha1.ProxyInjectedDelivery) field.ErrorList {
	var errs field.ErrorList
	if len(pi.Hosts) == 0 {
		errs = append(errs, field.Required(p.Child("hosts"),
			"at least one host is required for proxy-injected delivery"))
	}
	errs = append(errs, validateHosts(p.Child("hosts"), pi.Hosts)...)

	count := 0
	if pi.Header != nil {
		count++
	}
	if pi.QueryParam != nil {
		count++
	}
	if pi.BasicAuth != nil {
		count++
	}
	switch count {
	case 0:
		errs = append(errs, field.Required(p,
			"exactly one of header/queryParam/basicAuth must be set"))
	case 1:
		// fine
	default:
		errs = append(errs, field.Invalid(p, "",
			"exactly one of header/queryParam/basicAuth must be set; multiple were provided"))
	}
	if pi.Header != nil && strings.TrimSpace(pi.Header.Name) == "" {
		errs = append(errs, field.Required(p.Child("header").Child("name"), ""))
	}
	if pi.QueryParam != nil && strings.TrimSpace(pi.QueryParam.Name) == "" {
		errs = append(errs, field.Required(p.Child("queryParam").Child("name"), ""))
	}
	if pi.BasicAuth != nil && strings.TrimSpace(pi.BasicAuth.Username) == "" {
		errs = append(errs, field.Required(p.Child("basicAuth").Child("username"), ""))
	}
	return errs
}

func validateInContainer(p *field.Path, ic *paddockv1alpha1.InContainerDelivery) field.ErrorList {
	var errs field.ErrorList
	if !ic.Accepted {
		errs = append(errs, field.Invalid(p.Child("accepted"), ic.Accepted,
			"accepted must be true to deliver a secret in-container; set it with a reason or use deliveryMode.proxyInjected instead"))
	}
	if len(strings.TrimSpace(ic.Reason)) < 20 {
		errs = append(errs, field.Invalid(p.Child("reason"), ic.Reason,
			"reason must be at least 20 characters explaining why in-container delivery is needed"))
	}
	return errs
}

// validateInterception enforces spec 0003 §3.7's union rules on
// spec.interception. A nil pointer is legal (defaults to transparent
// at runtime); a set pointer must have exactly one sub-field, and
// cooperativeAccepted carries the standard accepted+reason opt-in.
func validateInterception(p *field.Path, i *paddockv1alpha1.InterceptionSpec) field.ErrorList {
	var errs field.ErrorList
	if i == nil {
		return errs
	}
	count := 0
	if i.Transparent != nil {
		count++
	}
	if i.CooperativeAccepted != nil {
		count++
	}
	switch count {
	case 0:
		errs = append(errs, field.Invalid(p, "",
			"exactly one of transparent or cooperativeAccepted must be set; "+
				"omit spec.interception to default to transparent"))
	case 1:
		// fine
	default:
		errs = append(errs, field.Invalid(p, "",
			"exactly one of transparent or cooperativeAccepted must be set; both were provided"))
	}
	if ca := i.CooperativeAccepted; ca != nil {
		if !ca.Accepted {
			errs = append(errs, field.Invalid(p.Child("cooperativeAccepted").Child("accepted"), ca.Accepted,
				"accepted must be true to opt into cooperative interception; "+
					"omit spec.interception (or set spec.interception.transparent) to use the safer default"))
		}
		if len(strings.TrimSpace(ca.Reason)) < 20 {
			errs = append(errs, field.Invalid(p.Child("cooperativeAccepted").Child("reason"), ca.Reason,
				"reason must be at least 20 characters explaining why cooperative interception is needed"))
		}
	}
	return errs
}

// validateEgressDiscovery enforces spec 0003 §3.6's bounded discovery
// window opt-in. nil is valid (the feature is optional). When set, the
// shape mirrors Plan A's InContainerDelivery + Plan B's
// CooperativeAcceptedInterception accept+reason pattern, plus a hard
// cap on expiresAt.
func validateEgressDiscovery(p *field.Path, ed *paddockv1alpha1.EgressDiscoverySpec, now time.Time) field.ErrorList {
	var errs field.ErrorList
	if ed == nil {
		return errs
	}
	if !ed.Accepted {
		errs = append(errs, field.Invalid(p.Child("accepted"), ed.Accepted,
			"accepted must be true to opt into a discovery window; "+
				"omit spec.egressDiscovery to keep deny-by-default"))
	}
	if len(strings.TrimSpace(ed.Reason)) < 20 {
		errs = append(errs, field.Invalid(p.Child("reason"), ed.Reason,
			"reason must be at least 20 characters explaining why a discovery window is needed"))
	}
	expiry := ed.ExpiresAt.Time
	if expiry.IsZero() || !expiry.After(now) {
		errs = append(errs, field.Invalid(p.Child("expiresAt"), ed.ExpiresAt,
			"expiresAt must be in the future"))
	} else if expiry.After(now.Add(MaxDiscoveryWindow)) {
		errs = append(errs, field.Invalid(p.Child("expiresAt"), ed.ExpiresAt,
			fmt.Sprintf("expiresAt must be within %d days of now",
				int(MaxDiscoveryWindow.Hours()/24))))
	}
	return errs
}

func validateHosts(p *field.Path, hosts []string) field.ErrorList {
	errs := make(field.ErrorList, 0, len(hosts))
	for i, h := range hosts {
		errs = append(errs, validateExternalHost(p.Index(i), h)...)
	}
	return errs
}

func validateEgressGrants(p *field.Path, grants []paddockv1alpha1.EgressGrant) field.ErrorList {
	var errs field.ErrorList
	for i, g := range grants {
		entry := p.Index(i)
		errs = append(errs, validateExternalHost(entry.Child("host"), g.Host)...)
		for j, port := range g.Ports {
			if port < 0 || port > 65535 {
				errs = append(errs, field.Invalid(entry.Child("ports").Index(j),
					port, "port must be 0 (any) or in [1, 65535]"))
			}
		}
	}
	return errs
}

func validateGitRepoGrants(p *field.Path, grants []paddockv1alpha1.GitRepoGrant) field.ErrorList {
	var errs field.ErrorList
	seen := map[string]int{}
	for i, g := range grants {
		entry := p.Index(i)
		if g.Owner == "" {
			errs = append(errs, field.Required(entry.Child("owner"), ""))
		}
		if g.Repo == "" {
			errs = append(errs, field.Required(entry.Child("repo"), ""))
		}
		if g.Owner == "" || g.Repo == "" {
			continue
		}
		key := g.Owner + "/" + g.Repo
		if prev, ok := seen[key]; ok {
			errs = append(errs, field.Duplicate(entry,
				fmt.Sprintf("%s collides with gitRepos[%d]", key, prev)))
			continue
		}
		seen[key] = i
	}
	return errs
}

func validateCredentialHostsCoveredByEgress(p *field.Path, g paddockv1alpha1.BrokerPolicyGrants) field.ErrorList {
	var errs field.ErrorList
	for i, cg := range g.Credentials {
		var hosts []string
		var hostsPath *field.Path
		if cg.Provider.DeliveryMode != nil && cg.Provider.DeliveryMode.ProxyInjected != nil {
			hosts = cg.Provider.DeliveryMode.ProxyInjected.Hosts
			hostsPath = p.Child("credentials").Index(i).Child("provider").Child("deliveryMode").Child("proxyInjected").Child("hosts")
		} else {
			hosts = cg.Provider.Hosts
			hostsPath = p.Child("credentials").Index(i).Child("provider").Child("hosts")
		}
		for j, h := range hosts {
			if !hostCoveredByAnyEgress(h, g.Egress) {
				errs = append(errs, field.Invalid(hostsPath.Index(j), h,
					fmt.Sprintf("host %q is not covered by any egress grant; add an entry to spec.grants.egress (globs with leading '*.' are supported)", h)))
			}
		}
	}
	return errs
}

func hostCoveredByAnyEgress(candidate string, egress []paddockv1alpha1.EgressGrant) bool {
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	for _, e := range egress {
		eh := strings.ToLower(strings.TrimSpace(e.Host))
		if strings.HasPrefix(eh, "*.") {
			suffix := eh[1:]
			if strings.HasSuffix(candidate, suffix) && candidate != suffix[1:] {
				return true
			}
			continue
		}
		if eh == candidate {
			return true
		}
	}
	return false
}
