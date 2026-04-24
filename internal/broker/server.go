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
	mux.HandleFunc(brokerapi.PathValidateEgress, s.handleValidateEgress)
	mux.HandleFunc(brokerapi.PathSubstituteAuth, s.handleSubstituteAuth)
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
	if !hasRequirement(spec.Requires.Credentials, req.Name) {
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
	grant, matchedPolicy, policyName, err := matchPolicyGrant(ctx, s.Client, namespace, run.Spec.TemplateRef.Name, req.Name)
	if err != nil {
		return providers.IssueResult{}, nil, fmt.Errorf("listing BrokerPolicies: %w", err)
	}
	if grant == nil {
		return providers.IssueResult{}, &CredentialAudit{
				RunName:        runName,
				Namespace:      namespace,
				CredentialName: req.Name,
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
				Provider:       grant.Provider.Kind,
				MatchedPolicy:  policyName,
				Reason:         "provider not registered on this broker",
			},
			&applicationError{
				status: http.StatusInternalServerError, code: "ProviderFailure",
				message: fmt.Sprintf("provider kind %q is not registered on this broker", grant.Provider.Kind),
			}
	}

	result, err := provider.Issue(ctx, providers.IssueRequest{
		RunName:        runName,
		Namespace:      namespace,
		CredentialName: req.Name,
		Grant:          *grant,
		GitRepos:       matchedPolicy.Spec.Grants.GitRepos,
	})
	audit := &CredentialAudit{
		RunName:        runName,
		Namespace:      namespace,
		CredentialName: req.Name,
		Provider:       provider.Name(),
		MatchedPolicy:  policyName,
		When:           time.Now().UTC(),
	}
	if err != nil {
		audit.Reason = err.Error()
		// Pool exhaustion is a transient, caller-actionable failure —
		// the run's reconciler keeps BrokerReady=False and requeues
		// until a lease frees up. Distinguish from the blanket
		// ProviderFailure so the controller can pick an appropriate
		// backoff.
		if errors.Is(err, providers.ErrPoolExhausted) {
			return providers.IssueResult{}, audit, &applicationError{
				status: http.StatusServiceUnavailable, code: "PoolExhausted",
				message: err.Error(),
			}
		}
		return providers.IssueResult{}, audit, &applicationError{
			status: http.StatusInternalServerError, code: "ProviderFailure",
			message: err.Error(),
		}
	}
	return result, audit, nil
}

// handleValidateEgress is the proxy's per-connection policy check.
// The proxy hits this with run identity in X-Paddock-Run and the
// destination in the body; the broker intersects matching BrokerPolicies'
// egress grants against (host, port) and returns allow/deny.
//
// This endpoint is deliberately narrow — no template resolution, no
// credential dispatch, just "does any policy grant this destination?".
// Keeps the hot path cheap enough to call on every new upstream
// connection without a proxy-side cache.
func (s *Server) handleValidateEgress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "BadRequest", "POST required")
		return
	}

	caller, err := s.authenticate(ctx, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}
	runName, runNamespace, err := resolveRunIdentity(r, caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}

	var req brokerapi.ValidateEgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("decoding body: %v", err))
		return
	}
	if req.Host == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "host is required")
		return
	}

	// Resolve the run so we know which template's policies to check.
	// Not-found runs short-circuit to deny — the proxy shouldn't be
	// asking for a run that was deleted.
	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: runName, Namespace: runNamespace}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, brokerapi.ValidateEgressResponse{
				Allowed: false,
				Reason:  "run no longer exists",
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "ProviderFailure", err.Error())
		return
	}

	grant, policyName, err := matchEgressGrant(ctx, s.Client, runNamespace, run.Spec.TemplateRef.Name, req.Host, req.Port)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ProviderFailure", err.Error())
		return
	}
	if grant == nil {
		writeJSON(w, http.StatusOK, brokerapi.ValidateEgressResponse{
			Allowed: false,
			Reason:  fmt.Sprintf("no BrokerPolicy grants egress to %s:%d", req.Host, req.Port),
		})
		return
	}
	writeJSON(w, http.StatusOK, brokerapi.ValidateEgressResponse{
		Allowed:       true,
		MatchedPolicy: policyName,
	})
}

// handleSubstituteAuth swaps a Paddock-issued bearer for upstream
// credentials at MITM time. The proxy sends the incoming Authorization
// / x-api-key values; the broker walks providers implementing
// Substituter, returns the first match. On error (expired / unknown)
// the proxy drops the connection — the agent sees a TLS error and
// retries with the same bearer, which will fail again until admission
// re-runs.
func (s *Server) handleSubstituteAuth(w http.ResponseWriter, r *http.Request) {
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
	runName, runNamespace, err := resolveRunIdentity(r, caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}

	var req brokerapi.SubstituteAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("decoding body: %v", err))
		return
	}
	if req.IncomingAuthorization == "" && req.IncomingXAPIKey == "" {
		writeError(w, http.StatusBadRequest, "BadRequest",
			"request must carry incomingAuthorization or incomingXApiKey")
		return
	}

	pReq := providers.SubstituteRequest{
		RunName:   runName,
		Namespace: runNamespace,
		Host:      req.Host,
		Port:      req.Port,
	}

	// Try x-api-key first (Anthropic agents put the bearer there); then
	// Authorization (Bearer ...). First provider that claims the bearer
	// answers definitively.
	candidates := []string{req.IncomingXAPIKey, req.IncomingAuthorization}
	for _, bearer := range candidates {
		if bearer == "" {
			continue
		}
		pReq.IncomingBearer = bearer
		for _, prov := range s.Providers.All() {
			sub, ok := prov.(providers.Substituter)
			if !ok {
				continue
			}
			result, err := sub.SubstituteAuth(ctx, pReq)
			if !result.Matched {
				continue
			}
			if err != nil {
				logger.Info("SubstituteAuth denied", "run", runName, "provider", prov.Name(), "err", err)
				writeError(w, http.StatusForbidden, "SubstituteFailed", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, brokerapi.SubstituteAuthResponse{
				SetHeaders:    result.SetHeaders,
				RemoveHeaders: result.RemoveHeaders,
			})
			return
		}
	}
	writeError(w, http.StatusNotFound, "BearerUnknown",
		"no registered provider owns the supplied bearer")
}

// resolveRunIdentity extracts (runName, runNamespace) from request
// headers, validating that non-controller callers only ask about runs
// in their own namespace. Shared between handleIssue, handleValidateEgress
// and handleSubstituteAuth.
func resolveRunIdentity(r *http.Request, caller CallerIdentity) (string, string, error) {
	runName := r.Header.Get(brokerapi.HeaderRun)
	if runName == "" {
		return "", "", fmt.Errorf("header %s is required", brokerapi.HeaderRun)
	}
	runNamespace := r.Header.Get(brokerapi.HeaderNamespace)
	if runNamespace == "" {
		runNamespace = caller.Namespace
	}
	if !caller.IsController && runNamespace != caller.Namespace {
		return "", "", fmt.Errorf("caller in namespace %q cannot ask about runs in namespace %q",
			caller.Namespace, runNamespace)
	}
	return runName, runNamespace, nil
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
