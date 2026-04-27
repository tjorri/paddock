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
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// validateExternalHost enforces the per-host rules common to
// BrokerPolicy.spec.grants[].egress[].host, BrokerPolicy.spec.grants[].
// credentials[].provider.hosts (and proxyInjected.hosts), and
// HarnessTemplate.spec.requires.egress[].host. The helper is the single
// place these rules live so admission stays in lockstep across both CRDs.
//
// Phase 2h Theme 3 refactor: this initial drop keeps the existing
// wildcard-position semantics. Subsequent commits add the F-34
// cluster-internal/IP-literal deny list and the F-35 normalization rules.
func validateExternalHost(p *field.Path, raw string) field.ErrorList {
	var errs field.ErrorList
	host := strings.TrimSpace(raw)
	if host == "" {
		errs = append(errs, field.Required(p, ""))
		return errs
	}
	if strings.HasPrefix(host, "*.") && strings.Contains(host[2:], "*") {
		errs = append(errs, field.Invalid(p, raw,
			"only a single leading '*.' wildcard is permitted"))
	} else if !strings.HasPrefix(host, "*.") && strings.Contains(host, "*") {
		errs = append(errs, field.Invalid(p, raw,
			"wildcard '*' is only permitted as a leading '*.' segment"))
	}
	return errs
}
