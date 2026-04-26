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
	"paddock.dev/paddock/internal/policy"
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

	result, grant, audit, err := s.issue(ctx, runNamespace, runName, req)
	if err != nil {
		// Deny path: write audit BEFORE returning the error to the
		// caller. If the audit write itself fails, return 503 so the
		// caller retries; the deny re-evaluates on retry. F-12.
		if audit != nil {
			if wErr := s.Audit.CredentialDenied(ctx, *audit); wErr != nil {
				logger.Error(wErr, "writing denial AuditEvent", "run", runName)
				writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
					"paddock-broker: audit unavailable, please retry")
				return
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

	// Issuance path: write audit BEFORE writing the JSON response. If
	// the audit write fails the credential has been minted but not yet
	// returned; the caller sees 503 and retries. F-12.
	if audit != nil {
		if wErr := s.Audit.CredentialIssued(ctx, *audit); wErr != nil {
			logger.Error(wErr, "writing issuance AuditEvent", "run", runName)
			writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
				"paddock-broker: audit unavailable, please retry")
			return
		}
	}

	resp := brokerapi.IssueResponse{
		Value:     result.Value,
		LeaseID:   result.LeaseID,
		ExpiresAt: result.ExpiresAt,
		Provider:  audit.Provider,
	}
	populateDeliveryMetadata(&resp, grant)
	writeJSON(w, http.StatusOK, resp)
}

// populateDeliveryMetadata fills DeliveryMode / Hosts / InContainerReason
// on an IssueResponse from the grant that was matched. Built-in providers
// are always ProxyInjected with baked-in default hosts (overridable via
// grant.Provider.Hosts); UserSuppliedSecret takes its delivery mode from
// grant.Provider.DeliveryMode.
func populateDeliveryMetadata(resp *brokerapi.IssueResponse, grant *paddockv1alpha1.CredentialGrant) {
	if grant == nil {
		return
	}
	switch grant.Provider.Kind {
	case "UserSuppliedSecret":
		dm := grant.Provider.DeliveryMode
		switch {
		case dm != nil && dm.ProxyInjected != nil:
			resp.DeliveryMode = "ProxyInjected"
			resp.Hosts = dm.ProxyInjected.Hosts
		case dm != nil && dm.InContainer != nil:
			resp.DeliveryMode = "InContainer"
			resp.InContainerReason = dm.InContainer.Reason
		}
	case "AnthropicAPI":
		resp.DeliveryMode = "ProxyInjected"
		resp.Hosts = hostsOrDefault(grant.Provider.Hosts, []string{"api.anthropic.com"})
	case "GitHubApp":
		resp.DeliveryMode = "ProxyInjected"
		resp.Hosts = hostsOrDefault(grant.Provider.Hosts, []string{"github.com", "api.github.com"})
	case "PATPool":
		resp.DeliveryMode = "ProxyInjected"
		resp.Hosts = hostsOrDefault(grant.Provider.Hosts, nil)
	}
}

// hostsOrDefault returns override when non-empty, else the built-in
// default list for the provider kind.
func hostsOrDefault(override, builtin []string) []string {
	if len(override) > 0 {
		return override
	}
	return builtin
}

// issue is the broker's core decision function. Returns (result, grant, audit, err).
// grant is the matched CredentialGrant when one was found, so the caller
// can attach delivery metadata (mode, hosts, reason) to the response.
// audit is non-nil whenever a decision was made (so the caller records
// either credential-issued or credential-denied). err is the surface
// error for the caller response; applicationError is preferred.
func (s *Server) issue(ctx context.Context, namespace, runName string, req brokerapi.IssueRequest) (providers.IssueResult, *paddockv1alpha1.CredentialGrant, *CredentialAudit, error) {
	// Resolve the run and its template.
	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: runName, Namespace: namespace}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return providers.IssueResult{}, nil, &CredentialAudit{
					RunName:        runName,
					Namespace:      namespace,
					CredentialName: req.Name,
					Reason:         "run not found",
				},
				&applicationError{status: http.StatusNotFound, code: "RunNotFound", message: err.Error()}
		}
		return providers.IssueResult{}, nil, &CredentialAudit{
				RunName:        runName,
				Namespace:      namespace,
				CredentialName: req.Name,
				Reason:         fmt.Sprintf("broker infrastructure error: loading run: %v", err),
			},
			fmt.Errorf("loading run: %w", err)
	}

	spec, err := resolveTemplateSpec(ctx, s.Client, namespace, run.Spec.TemplateRef)
	if err != nil {
		return providers.IssueResult{}, nil, &CredentialAudit{
				RunName:        runName,
				Namespace:      namespace,
				CredentialName: req.Name,
				Reason:         fmt.Sprintf("broker infrastructure error: resolving template: %v", err),
			},
			fmt.Errorf("resolving template: %w", err)
	}
	if !hasRequirement(spec.Requires.Credentials, req.Name) {
		return providers.IssueResult{}, nil, &CredentialAudit{
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
		return providers.IssueResult{}, nil, &CredentialAudit{
				RunName:        runName,
				Namespace:      namespace,
				CredentialName: req.Name,
				Reason:         fmt.Sprintf("broker infrastructure error: listing BrokerPolicies: %v", err),
			},
			fmt.Errorf("listing BrokerPolicies: %w", err)
	}
	if grant == nil {
		return providers.IssueResult{}, nil, &CredentialAudit{
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
		return providers.IssueResult{}, grant, &CredentialAudit{
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
			return providers.IssueResult{}, grant, audit, &applicationError{
				status: http.StatusServiceUnavailable, code: "PoolExhausted",
				message: err.Error(),
			}
		}
		return providers.IssueResult{}, grant, audit, &applicationError{
			status: http.StatusInternalServerError, code: "ProviderFailure",
			message: err.Error(),
		}
	}
	return result, grant, audit, nil
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
		// No explicit egress grant. Before denying, check whether any
		// matching BrokerPolicy has an active discovery window — if so,
		// return Allowed=true with DiscoveryAllow=true so the proxy
		// can emit an egress-discovery-allow event instead of denying.
		matches, err := policy.ListMatchingPolicies(ctx, s.Client, runNamespace, run.Spec.TemplateRef.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ProviderFailure", err.Error())
			return
		}
		if policy.AnyDiscoveryActive(matches, time.Now()) {
			writeJSON(w, http.StatusOK, brokerapi.ValidateEgressResponse{
				Allowed:        true,
				DiscoveryAllow: true,
				Reason:         fmt.Sprintf("no grant covers %s:%d, but a matching policy has an active egressDiscovery window", req.Host, req.Port),
			})
			return
		}
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

	// F-10: re-fetch HarnessRun on every SubstituteAuth call so a
	// run that was deleted or transitioned to a terminal phase since
	// the bearer was issued cannot continue substituting credentials.
	// Cached client; sub-millisecond informer-cache lookup.
	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: runName, Namespace: runNamespace}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			runTerminatedAudit := CredentialAudit{
				RunName:        runName,
				Namespace:      runNamespace,
				CredentialName: req.Host,
				Reason:         "run not found",
			}
			if wErr := s.Audit.CredentialDenied(ctx, runTerminatedAudit); wErr != nil {
				logger.Error(wErr, "writing substitute-auth denial AuditEvent", "run", runName)
				writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
					"paddock-broker: audit unavailable, please retry")
				return
			}
			writeError(w, http.StatusNotFound, "RunTerminated", "run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "ProviderFailure", err.Error())
		return
	}
	switch run.Status.Phase {
	case paddockv1alpha1.HarnessRunPhaseCancelled,
		paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed:
		runTerminatedAudit := CredentialAudit{
			RunName:        runName,
			Namespace:      runNamespace,
			CredentialName: req.Host,
			Reason:         fmt.Sprintf("run terminated: %s", run.Status.Phase),
		}
		if wErr := s.Audit.CredentialDenied(ctx, runTerminatedAudit); wErr != nil {
			logger.Error(wErr, "writing substitute-auth denial AuditEvent", "run", runName)
			writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
				"paddock-broker: audit unavailable, please retry")
			return
		}
		writeError(w, http.StatusForbidden, "RunTerminated",
			fmt.Sprintf("run terminated: %s", run.Status.Phase))
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
				denyAudit := CredentialAudit{
					RunName:        runName,
					Namespace:      runNamespace,
					CredentialName: pReq.Host,
					Provider:       prov.Name(),
					Reason:         "substitute failed: " + err.Error(),
				}
				if wErr := s.Audit.CredentialDenied(ctx, denyAudit); wErr != nil {
					logger.Error(wErr, "writing substitute-auth denial AuditEvent", "run", runName)
					writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
						"paddock-broker: audit unavailable, please retry")
					return
				}
				writeError(w, http.StatusForbidden, "SubstituteFailed", err.Error())
				return
			}
			grantAudit := CredentialAudit{
				RunName:        runName,
				Namespace:      runNamespace,
				CredentialName: pReq.Host,
				Provider:       prov.Name(),
				Reason:         "substituted upstream credential",
			}
			if wErr := s.Audit.CredentialIssued(ctx, grantAudit); wErr != nil {
				logger.Error(wErr, "writing substitute-auth issuance AuditEvent", "run", runName)
				writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
					"paddock-broker: audit unavailable, please retry")
				return
			}
			writeJSON(w, http.StatusOK, brokerapi.SubstituteAuthResponse{
				SetHeaders:    result.SetHeaders,
				RemoveHeaders: result.RemoveHeaders,
			})
			return
		}
	}
	bearerUnknownAudit := CredentialAudit{
		RunName:        runName,
		Namespace:      runNamespace,
		CredentialName: req.Host,
		Reason:         "no registered provider owns the supplied bearer",
	}
	if wErr := s.Audit.CredentialDenied(ctx, bearerUnknownAudit); wErr != nil {
		logger.Error(wErr, "writing substitute-auth denial AuditEvent", "run", runName)
		writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
			"paddock-broker: audit unavailable, please retry")
		return
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
