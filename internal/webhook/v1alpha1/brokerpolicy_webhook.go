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

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var brokerpolicylog = logf.Log.WithName("brokerpolicy-resource")

// SetupBrokerPolicyWebhookWithManager registers the validating webhook
// for BrokerPolicy with the manager.
func SetupBrokerPolicyWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &paddockv1alpha1.BrokerPolicy{}).
		WithValidator(&BrokerPolicyCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-brokerpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=brokerpolicies,verbs=create;update,versions=v1alpha1,name=vbrokerpolicy-v1alpha1.kb.io,admissionReviewVersions=v1

// BrokerPolicyCustomValidator enforces BrokerPolicy spec invariants:
//
//   - appliesToTemplates has at least one entry;
//   - every grant has the fields its provider kind requires;
//   - credential names are unique within the policy;
//   - egress hosts are non-empty and wildcard-valid;
//   - git repo tuples are complete.
type BrokerPolicyCustomValidator struct{}

var _ admission.Validator[*paddockv1alpha1.BrokerPolicy] = &BrokerPolicyCustomValidator{}

func (v *BrokerPolicyCustomValidator) ValidateCreate(_ context.Context, bp *paddockv1alpha1.BrokerPolicy) (admission.Warnings, error) {
	brokerpolicylog.V(1).Info("validating BrokerPolicy create", "name", bp.GetName())
	return nil, validateBrokerPolicySpec(&bp.Spec)
}

func (v *BrokerPolicyCustomValidator) ValidateUpdate(_ context.Context, _, newBP *paddockv1alpha1.BrokerPolicy) (admission.Warnings, error) {
	brokerpolicylog.V(1).Info("validating BrokerPolicy update", "name", newBP.GetName())
	return nil, validateBrokerPolicySpec(&newBP.Spec)
}

func (v *BrokerPolicyCustomValidator) ValidateDelete(_ context.Context, _ *paddockv1alpha1.BrokerPolicy) (admission.Warnings, error) {
	return nil, nil
}

func validateBrokerPolicySpec(spec *paddockv1alpha1.BrokerPolicySpec) error {
	specPath := field.NewPath("spec")
	var errs field.ErrorList

	if len(spec.AppliesToTemplates) == 0 {
		errs = append(errs, field.Required(specPath.Child("appliesToTemplates"),
			"at least one template selector is required"))
	}
	for i, sel := range spec.AppliesToTemplates {
		if strings.TrimSpace(sel) == "" {
			errs = append(errs, field.Invalid(specPath.Child("appliesToTemplates").Index(i),
				sel, "selector must not be empty"))
		}
	}

	grantsPath := specPath.Child("grants")
	errs = append(errs, validateCredentialGrants(grantsPath.Child("credentials"), spec.Grants.Credentials)...)
	errs = append(errs, validateEgressGrants(grantsPath.Child("egress"), spec.Grants.Egress)...)
	errs = append(errs, validateGitRepoGrants(grantsPath.Child("gitRepos"), spec.Grants.GitRepos)...)

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
			errs = append(errs, field.Duplicate(entry.Child("name"),
				fmt.Sprintf("name %q collides with credentials[%d].name", g.Name, prev)))
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
	switch cfg.Kind {
	case "Static", "AnthropicAPI", "PATPool":
		if cfg.SecretRef == nil {
			errs = append(errs, field.Required(p.Child("secretRef"),
				fmt.Sprintf("provider kind %q requires secretRef", cfg.Kind)))
		}
		if cfg.AppID != "" || cfg.InstallationID != "" {
			errs = append(errs, field.Forbidden(p,
				fmt.Sprintf("provider kind %q must not set appId or installationId", cfg.Kind)))
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
	return errs
}

func validateEgressGrants(p *field.Path, grants []paddockv1alpha1.EgressGrant) field.ErrorList {
	var errs field.ErrorList
	for i, g := range grants {
		entry := p.Index(i)
		host := strings.TrimSpace(g.Host)
		if host == "" {
			errs = append(errs, field.Required(entry.Child("host"), ""))
			continue
		}
		if strings.HasPrefix(host, "*.") && strings.Contains(host[2:], "*") {
			errs = append(errs, field.Invalid(entry.Child("host"), g.Host,
				"only a single leading '*.' wildcard is permitted"))
		} else if !strings.HasPrefix(host, "*.") && strings.Contains(host, "*") {
			errs = append(errs, field.Invalid(entry.Child("host"), g.Host,
				"wildcard '*' is only permitted as a leading '*.' segment"))
		}
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
