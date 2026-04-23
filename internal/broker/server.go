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

// Package broker is the runtime for the paddock-broker Deployment.
// The broker holds upstream credentials (API keys, GitHub App private
// keys, PAT pools), validates caller identity via TokenReview, and
// issues per-run values through pluggable providers. See ADR-0012
// and spec 0002 §6.
package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/broker/providers"
)

// Server is the HTTP handler set for the broker. Register it on a
// net/http.Server configured for mTLS on :8443.
type Server struct {
	// Client reads HarnessRuns, templates, BrokerPolicies, and
	// provider-backing Secrets.
	Client client.Client

	// Auth validates caller Bearer tokens.
	Auth TokenValidator

	// Providers holds every registered provider by Name().
	Providers *providers.Registry

	// Audit writes AuditEvents for every decision.
	Audit *AuditWriter
}

// Register installs the broker's handlers on the given mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc(brokerapi.PathHealthz, s.handleHealthz)
	mux.HandleFunc(brokerapi.PathReadyz, s.handleReadyz)
	mux.HandleFunc(brokerapi.PathIssue, s.handleIssue)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleIssue is the core endpoint. It authenticates the caller, looks
// up the run + template, intersects the template's requires with
// in-namespace BrokerPolicies, dispatches to the matching Provider,
// and emits an AuditEvent regardless of outcome.
func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "BadRequest", "POST required")
		return
	}

	caller, err := s.authenticate(ctx, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}

	runName := r.Header.Get(brokerapi.HeaderRun)
	if runName == "" {
		writeError(w, http.StatusBadRequest, "BadRequest",
			fmt.Sprintf("header %s is required", brokerapi.HeaderRun))
		return
	}
	runNamespace := r.Header.Get(brokerapi.HeaderNamespace)
	if runNamespace == "" {
		runNamespace = caller.Namespace
	}

	// Non-controller callers may only ask about runs in their own namespace.
	if !caller.IsController && runNamespace != caller.Namespace {
		writeError(w, http.StatusForbidden, "Forbidden",
			fmt.Sprintf("caller in namespace %q cannot ask about runs in namespace %q",
				caller.Namespace, runNamespace))
		return
	}

	var req brokerapi.IssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("decoding body: %v", err))
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "name is required")
		return
	}

	result, audit, err := s.issue(ctx, runNamespace, runName, req)
	if err != nil {
		// Best-effort audit write for denials; any failure here is logged
		// but not surfaced to the caller (the credential denial is the
		// primary signal).
		if audit != nil {
			if wErr := s.Audit.CredentialDenied(ctx, *audit); wErr != nil {
				logger.Error(wErr, "writing denial AuditEvent", "run", runName)
			}
		}
		var appErr *applicationError
		if errors.As(err, &appErr) {
			writeError(w, appErr.status, appErr.code, appErr.message)
			return
		}
		writeError(w, http.StatusInternalServerError, "ProviderFailure", err.Error())
		return
	}

	if audit != nil {
		if wErr := s.Audit.CredentialIssued(ctx, *audit); wErr != nil {
			logger.Error(wErr, "writing issuance AuditEvent", "run", runName)
		}
	}

	writeJSON(w, http.StatusOK, brokerapi.IssueResponse{
		Value:     result.Value,
		LeaseID:   result.LeaseID,
		ExpiresAt: result.ExpiresAt,
		Provider:  audit.Provider,
	})
}

// issue is the broker's core decision function. Returns (result, audit, err).
// audit is non-nil whenever a decision was made (so the caller records
// either credential-issued or credential-denied). err is the surface
// error for the caller response; applicationError is preferred.
func (s *Server) issue(ctx context.Context, namespace, runName string, req brokerapi.IssueRequest) (providers.IssueResult, *CredentialAudit, error) {
	// Resolve the run and its template.
	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: runName, Namespace: namespace}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return providers.IssueResult{}, &CredentialAudit{
					RunName:        runName,
					Namespace:      namespace,
					CredentialName: req.Name,
					Reason:         "run not found",
				},
				&applicationError{status: http.StatusNotFound, code: "RunNotFound", message: err.Error()}
		}
		return providers.IssueResult{}, nil, fmt.Errorf("loading run: %w", err)
	}

	spec, err := resolveTemplateSpec(ctx, s.Client, namespace, run.Spec.TemplateRef)
	if err != nil {
		return providers.IssueResult{}, nil, fmt.Errorf("resolving template: %w", err)
	}
	requirement, ok := findRequirement(spec.Requires.Credentials, req.Name)
	if !ok {
		return providers.IssueResult{}, &CredentialAudit{
				RunName:        runName,
				Namespace:      namespace,
				CredentialName: req.Name,
				Reason:         "template does not declare this credential in spec.requires",
			},
			&applicationError{
				status:  http.StatusNotFound,
				code:    "CredentialNotFound",
				message: fmt.Sprintf("template %q does not declare credential %q in spec.requires", run.Spec.TemplateRef.Name, req.Name),
			}
	}

	// Intersect template.requires against in-namespace BrokerPolicies.
	grant, policyName, err := matchPolicyGrant(ctx, s.Client, namespace, run.Spec.TemplateRef.Name, req.Name)
	if err != nil {
		return providers.IssueResult{}, nil, fmt.Errorf("listing BrokerPolicies: %w", err)
	}
	if grant == nil {
		return providers.IssueResult{}, &CredentialAudit{
				RunName:        runName,
				Namespace:      namespace,
				CredentialName: req.Name,
				Purpose:        requirement.Purpose,
				Reason:         fmt.Sprintf("no BrokerPolicy in namespace %q grants credential %q for template %q", namespace, req.Name, run.Spec.TemplateRef.Name),
			},
			&applicationError{
				status: http.StatusForbidden, code: "PolicyMissing",
				message: fmt.Sprintf("no BrokerPolicy in namespace %q grants credential %q for template %q",
					namespace, req.Name, run.Spec.TemplateRef.Name),
			}
	}

	// Dispatch to the provider.
	provider, ok := s.Providers.Lookup(grant.Provider.Kind)
	if !ok {
		return providers.IssueResult{}, &CredentialAudit{
				RunName:        runName,
				Namespace:      namespace,
				CredentialName: req.Name,
				Purpose:        requirement.Purpose,
				Provider:       grant.Provider.Kind,
				MatchedPolicy:  policyName,
				Reason:         "provider not registered on this broker",
			},
			&applicationError{
				status: http.StatusInternalServerError, code: "ProviderFailure",
				message: fmt.Sprintf("provider kind %q is not registered on this broker", grant.Provider.Kind),
			}
	}
	if !providerBacksPurpose(provider, requirement.Purpose) {
		return providers.IssueResult{}, &CredentialAudit{
				RunName:        runName,
				Namespace:      namespace,
				CredentialName: req.Name,
				Purpose:        requirement.Purpose,
				Provider:       provider.Name(),
				MatchedPolicy:  policyName,
				Reason:         fmt.Sprintf("provider %q cannot back purpose %q", provider.Name(), requirement.Purpose),
			},
			&applicationError{
				status: http.StatusForbidden, code: "PolicyMissing",
				message: fmt.Sprintf("provider %q cannot back purpose %q", provider.Name(), requirement.Purpose),
			}
	}

	result, err := provider.Issue(ctx, providers.IssueRequest{
		RunName:        runName,
		Namespace:      namespace,
		CredentialName: req.Name,
		Purpose:        requirement.Purpose,
		Grant:          *grant,
	})
	audit := &CredentialAudit{
		RunName:        runName,
		Namespace:      namespace,
		CredentialName: req.Name,
		Purpose:        requirement.Purpose,
		Provider:       provider.Name(),
		MatchedPolicy:  policyName,
		When:           time.Now().UTC(),
	}
	if err != nil {
		audit.Reason = err.Error()
		return providers.IssueResult{}, audit, &applicationError{
			status: http.StatusInternalServerError, code: "ProviderFailure",
			message: err.Error(),
		}
	}
	return result, audit, nil
}

func (s *Server) authenticate(ctx context.Context, r *http.Request) (CallerIdentity, error) {
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		return CallerIdentity{}, fmt.Errorf("missing or malformed Authorization header")
	}
	token := strings.TrimPrefix(authz, "Bearer ")
	return s.Auth.Authenticate(ctx, token)
}

// applicationError is an error the handler maps directly to an HTTP
// status + code + message. Any other error becomes 500 ProviderFailure.
type applicationError struct {
	status  int
	code    string
	message string
}

func (e *applicationError) Error() string { return e.message }

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, brokerapi.ErrorResponse{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
