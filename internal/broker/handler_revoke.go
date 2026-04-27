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

package broker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// handleRevoke implements POST /v1/revoke. Authorisation: caller must
// be the controller (IsController == true). Run identity is carried in
// the standard X-Paddock-Run / X-Paddock-Run-Namespace headers. The
// handler dispatches to the named provider's Revoke method, emits a
// credential-revoked AuditEvent before responding (Phase 2c
// fail-closed-on-audit-failure), and returns 204 NoContent on success.
//
// Idempotent: a Revoke against an unknown leaseID returns 204 (the
// provider returns nil for unknown IDs; the audit event still fires so
// the operator-visible trail records the attempt).
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, brokerapi.CodeBadRequest, "POST required")
		return
	}

	caller, err := s.authenticate(ctx, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, brokerapi.CodeUnauthorized, err.Error())
		return
	}
	if !caller.IsController {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden,
			"only the controller-manager may call /v1/revoke")
		return
	}

	runName, runNamespace, err := resolveRunIdentity(r, caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, err.Error())
		return
	}

	var req brokerapi.RevokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, fmt.Sprintf("decoding body: %v", err))
		return
	}
	if req.Provider == "" || req.LeaseID == "" {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest,
			"provider and leaseId are required")
		return
	}

	prov, ok := s.Providers.Lookup(req.Provider)
	if !ok {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest,
			fmt.Sprintf("unknown provider %q", req.Provider))
		return
	}

	revokeErr := prov.Revoke(ctx, req.LeaseID)
	audit := CredentialAudit{
		RunName:        runName,
		Namespace:      runNamespace,
		CredentialName: req.CredentialName,
		Provider:       req.Provider,
		When:           time.Now().UTC(),
	}
	if revokeErr != nil {
		audit.Reason = "revoke failed: " + revokeErr.Error()
		// Provider revoke failure is audited as a denial-shape event so
		// the operator-visible trail records the attempt; the response is
		// 500. If the audit write itself fails, return 503 so the caller
		// retries. Phase 2c fail-closed contract.
		if wErr := s.Audit.CredentialDenied(ctx, audit); wErr != nil {
			logger.Error(wErr, "writing revoke-failed AuditEvent", "run", runName)
			writeError(w, http.StatusServiceUnavailable, brokerapi.CodeAuditUnavailable,
				"paddock-broker: audit unavailable, please retry")
			return
		}
		writeError(w, http.StatusInternalServerError, brokerapi.CodeProviderFailure, revokeErr.Error())
		return
	}

	// Success path: emit credential-revoked BEFORE writing the response.
	// If the audit write fails, return 503 so the caller retries.
	// Phase 2c fail-closed-on-audit-failure contract.
	audit.Reason = "lease revoked"
	if wErr := s.Audit.CredentialRevoked(ctx, audit); wErr != nil {
		logger.Error(wErr, "writing credential-revoked AuditEvent", "run", runName)
		writeError(w, http.StatusServiceUnavailable, brokerapi.CodeAuditUnavailable,
			"paddock-broker: audit unavailable, please retry")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
