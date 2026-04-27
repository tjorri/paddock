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
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// validateHarnessTemplateSpec enforces the shared validation rules for
// ClusterHarnessTemplate and HarnessTemplate. See docs/contributing/adr/0003-template-
// override-semantics.md for the override rules.
//
// Cluster-scoped templates cannot carry baseTemplateRef and must supply
// image+command. Namespaced templates with baseTemplateRef must leave the
// locked fields empty; without baseTemplateRef they behave as standalone
// and must supply image+command.
func validateHarnessTemplateSpec(spec *paddockv1alpha1.HarnessTemplateSpec, isClusterScoped bool) error {
	specPath := field.NewPath("spec")
	var errs field.ErrorList

	if isClusterScoped && spec.BaseTemplateRef != nil {
		errs = append(errs, field.Forbidden(
			specPath.Child("baseTemplateRef"),
			"ClusterHarnessTemplate cannot inherit from another template",
		))
	}

	inheriting := !isClusterScoped && spec.BaseTemplateRef != nil

	if inheriting {
		// Locked fields must be empty on an inheriting namespaced template.
		if spec.Image != "" {
			errs = append(errs, field.Forbidden(specPath.Child("image"),
				"must be empty when baseTemplateRef is set; inherited from the base template"))
		}
		if len(spec.Command) != 0 {
			errs = append(errs, field.Forbidden(specPath.Child("command"),
				"must be empty when baseTemplateRef is set; inherited from the base template"))
		}
		if len(spec.Args) != 0 {
			errs = append(errs, field.Forbidden(specPath.Child("args"),
				"must be empty when baseTemplateRef is set; inherited from the base template"))
		}
		if spec.EventAdapter != nil {
			errs = append(errs, field.Forbidden(specPath.Child("eventAdapter"),
				"must be empty when baseTemplateRef is set; inherited from the base template"))
		}
		// Workspace block is locked too — empty struct (zero value) is fine,
		// but any non-zero field means the author tried to override it.
		if spec.Workspace != (paddockv1alpha1.WorkspaceRequirement{}) {
			errs = append(errs, field.Forbidden(specPath.Child("workspace"),
				"must be empty when baseTemplateRef is set; inherited from the base template"))
		}
	} else {
		// Standalone template (cluster-scoped, or namespaced without
		// baseTemplateRef) must carry its own pod shape.
		if spec.Image == "" {
			errs = append(errs, field.Required(specPath.Child("image"),
				"image is required when baseTemplateRef is not set"))
		}
		if len(spec.Command) == 0 {
			errs = append(errs, field.Required(specPath.Child("command"),
				"command is required when baseTemplateRef is not set"))
		}
	}

	if spec.EventAdapter != nil && spec.EventAdapter.Image == "" {
		errs = append(errs, field.Required(specPath.Child("eventAdapter").Child("image"),
			"eventAdapter.image is required when eventAdapter is set"))
	}

	errs = append(errs, validateRequireSpec(specPath.Child("requires"), &spec.Requires)...)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}

// validateRequireSpec checks a template's Requires block. Names must be
// non-empty and unique; egress hosts are non-empty and wildcard-valid;
// ports are in range.
func validateRequireSpec(p *field.Path, req *paddockv1alpha1.RequireSpec) field.ErrorList {
	var errs field.ErrorList

	credsPath := p.Child("credentials")
	seen := map[string]int{}
	for i, c := range req.Credentials {
		entry := credsPath.Index(i)
		if c.Name == "" {
			errs = append(errs, field.Required(entry.Child("name"), ""))
			continue
		}
		if prev, ok := seen[c.Name]; ok {
			errs = append(errs, field.Duplicate(entry.Child("name"),
				fmt.Sprintf("name %q collides with credentials[%d].name", c.Name, prev)))
			continue
		}
		seen[c.Name] = i
	}

	egressPath := p.Child("egress")
	for i, e := range req.Egress {
		entry := egressPath.Index(i)
		errs = append(errs, validateExternalHost(entry.Child("host"), e.Host)...)
		for j, port := range e.Ports {
			if port < 0 || port > 65535 {
				errs = append(errs, field.Invalid(entry.Child("ports").Index(j),
					port, "port must be 0 (any) or in [1, 65535]"))
			}
		}
	}

	return errs
}
