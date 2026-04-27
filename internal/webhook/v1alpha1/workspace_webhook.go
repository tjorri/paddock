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
	"net/url"
	"path"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var workspacelog = logf.Log.WithName("workspace-resource")

// SetupWorkspaceWebhookWithManager registers the validating webhook for
// Workspace with the manager.
func SetupWorkspaceWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &paddockv1alpha1.Workspace{}).
		WithValidator(&WorkspaceCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-paddock-dev-v1alpha1-workspace,mutating=false,failurePolicy=fail,sideEffects=None,groups=paddock.dev,resources=workspaces,verbs=create;update,versions=v1alpha1,name=vworkspace-v1alpha1.kb.io,admissionReviewVersions=v1

// WorkspaceCustomValidator enforces Workspace spec invariants:
//
//   - spec.storage.size must be > 0;
//   - if spec.seed is set, exactly one seed source is selected;
//   - spec.storage and spec.seed are immutable after creation.
type WorkspaceCustomValidator struct{}

var _ admission.Validator[*paddockv1alpha1.Workspace] = &WorkspaceCustomValidator{}

func (v *WorkspaceCustomValidator) ValidateCreate(_ context.Context, ws *paddockv1alpha1.Workspace) (admission.Warnings, error) {
	workspacelog.V(1).Info("validating Workspace create", "name", ws.GetName())
	return nil, validateWorkspaceSpec(&ws.Spec)
}

func (v *WorkspaceCustomValidator) ValidateUpdate(_ context.Context, oldWS, newWS *paddockv1alpha1.Workspace) (admission.Warnings, error) {
	workspacelog.V(1).Info("validating Workspace update", "name", newWS.GetName())

	if !reflect.DeepEqual(oldWS.Spec.Storage, newWS.Spec.Storage) {
		return nil, fmt.Errorf("spec.storage is immutable")
	}
	if !reflect.DeepEqual(oldWS.Spec.Seed, newWS.Spec.Seed) {
		return nil, fmt.Errorf("spec.seed is immutable")
	}
	return nil, validateWorkspaceSpec(&newWS.Spec)
}

func (v *WorkspaceCustomValidator) ValidateDelete(_ context.Context, _ *paddockv1alpha1.Workspace) (admission.Warnings, error) {
	return nil, nil
}

func validateWorkspaceSpec(spec *paddockv1alpha1.WorkspaceSpec) error {
	specPath := field.NewPath("spec")
	var errs field.ErrorList

	if spec.Storage.Size.IsZero() {
		errs = append(errs, field.Required(specPath.Child("storage").Child("size"),
			"storage size must be > 0"))
	}

	if spec.Seed != nil {
		reposPath := specPath.Child("seed").Child("repos")
		switch {
		case len(spec.Seed.Repos) == 0:
			errs = append(errs, field.Required(reposPath,
				"at least one repo must be declared when seed is set"))
		default:
			errs = append(errs, validateWorkspaceRepos(reposPath, spec.Seed.Repos)...)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}

// validateWorkspaceRepos checks per-entry constraints and cross-entry
// path uniqueness.
func validateWorkspaceRepos(reposPath *field.Path, repos []paddockv1alpha1.WorkspaceGitSource) field.ErrorList {
	var errs field.ErrorList
	seenPaths := map[string]int{}
	multi := len(repos) > 1

	for i, repo := range repos {
		entryPath := reposPath.Index(i)
		if repo.URL == "" {
			errs = append(errs, field.Required(entryPath.Child("url"), ""))
		} else if e := validateRepoURL(entryPath.Child("url"), repo.URL); e != nil {
			errs = append(errs, e)
		}

		// Credentials come from either a static Secret (v0.2) or the
		// broker (v0.3). Setting both is ambiguous; the seed Pod can't
		// honour two credential sources for one clone.
		if repo.CredentialsSecretRef != nil && repo.BrokerCredentialRef != nil {
			errs = append(errs, field.Invalid(entryPath.Child("brokerCredentialRef"),
				repo.BrokerCredentialRef,
				"must not be set together with credentialsSecretRef"))
		}
		if repo.BrokerCredentialRef != nil {
			if repo.BrokerCredentialRef.Name == "" {
				errs = append(errs, field.Required(entryPath.Child("brokerCredentialRef").Child("name"), ""))
			}
			if repo.BrokerCredentialRef.Key == "" {
				errs = append(errs, field.Required(entryPath.Child("brokerCredentialRef").Child("key"), ""))
			}
			if isSSHURLLocal(repo.URL) {
				// The proxy substitution path is HTTPS-only; ssh URLs
				// can't be MITM'd. Reject at admission.
				errs = append(errs, field.Invalid(entryPath.Child("brokerCredentialRef"),
					repo.BrokerCredentialRef,
					"broker-backed credentials are only valid for https URLs; use credentialsSecretRef for ssh"))
			}
		}

		trimmed := strings.TrimSpace(repo.Path)
		if multi && trimmed == "" {
			errs = append(errs, field.Required(entryPath.Child("path"),
				"path is required when multiple repos are declared"))
			continue
		}
		if trimmed == "" {
			continue
		}
		if err := validateRepoPath(entryPath.Child("path"), trimmed); err != nil {
			errs = append(errs, err)
			continue
		}
		cleaned := path.Clean(trimmed)
		if prev, ok := seenPaths[cleaned]; ok {
			errs = append(errs, field.Duplicate(entryPath.Child("path"),
				fmt.Sprintf("path %q collides with repos[%d].path", trimmed, prev)))
			continue
		}
		seenPaths[cleaned] = i
	}
	return errs
}

// isSSHURLLocal mirrors controller.isSSHURL. Kept in the webhook
// package so admission can reject broker-backed creds on ssh URLs
// without importing the controller package (cycle).
func isSSHURLLocal(url string) bool {
	if strings.HasPrefix(url, "ssh://") {
		return true
	}
	if strings.Contains(url, "://") {
		return false
	}
	if at := strings.Index(url, "@"); at > 0 {
		rest := url[at+1:]
		if colon := strings.Index(rest, ":"); colon > 0 && !strings.Contains(rest[:colon], "/") {
			return true
		}
	}
	return false
}

// validateRepoURL checks that raw is one of:
//   - "https://..." (no userinfo — see F-50, validated separately if applicable)
//   - "ssh://user@host/..." or scp-style "user@host:path"
//
// Rejects file://, git://, http://, and any other scheme. The seed
// proxy's substitute-auth path is HTTPS-only by design, so SSH lives
// outside the MITM trust model; per-host SSH allowlisting is delegated
// to the per-seed-Pod NetworkPolicy. F-46.
func validateRepoURL(p *field.Path, raw string) *field.Error {
	if raw == "" {
		return nil // empty URL is caught by the Required check upstream.
	}
	if isSSHURLLocal(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return field.Invalid(p, raw, "must be a valid URL")
	}
	if u.Scheme != "https" {
		return field.Invalid(p, raw,
			fmt.Sprintf("scheme %q is not allowed; only https:// or ssh:// (or scp-style user@host:path) accepted", u.Scheme))
	}
	return nil
}

func validateRepoPath(p *field.Path, raw string) *field.Error {
	if strings.HasPrefix(raw, "/") {
		return field.Invalid(p, raw, "must be a relative path")
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == "/" {
		return field.Invalid(p, raw, "must not resolve to the workspace root")
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == ".." {
			return field.Invalid(p, raw, "must not contain '..' segments")
		}
	}
	return nil
}
