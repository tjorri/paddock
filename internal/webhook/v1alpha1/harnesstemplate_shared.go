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
// ClusterHarnessTemplate and HarnessTemplate. See docs/adr/0003-template-
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

	for i, cred := range spec.Credentials {
		credPath := specPath.Child("credentials").Index(i)
		if cred.Name == "" {
			errs = append(errs, field.Required(credPath.Child("name"), ""))
		}
		if cred.EnvKey == "" {
			errs = append(errs, field.Required(credPath.Child("envKey"), ""))
		}
		if cred.SecretRef.Name == "" {
			errs = append(errs, field.Required(credPath.Child("secretRef").Child("name"), ""))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}
