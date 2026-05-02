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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/auditing"
	brokerapi "github.com/tjorri/paddock/internal/broker/api"
	"github.com/tjorri/paddock/internal/policy"
)

// interactiveSmallBodyCap caps the read length for /interrupt and /end
// bodies. Both are tiny (InterruptRequest is empty, EndRequest is just
// a short reason string); 1 KiB is a defensive cap against junk POSTs.
const interactiveSmallBodyCap = 1 << 10

// maxReasonBytes caps the sanitized /end reason persisted to the
// AuditEvent detail. Keeps the field bounded for downstream log
// parsers regardless of the 1 KiB body cap above.
const maxReasonBytes = 256

// sanitizeReason normalizes a caller-supplied /end reason: trims,
// replaces control characters with a single space, and truncates to
// maxReasonBytes (rune-safe — never splits a UTF-8 rune mid-encoding).
// Returns "" when the input is whitespace-only.
func sanitizeReason(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if b.Len() >= maxReasonBytes {
			break
		}
		if unicode.IsControl(r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// handlePrompts authenticates the caller, validates the run is in
// Interactive mode with a runs.interact grant, allocates a turn
// sequence, walks lazy renewals, emits prompt-submitted, then
// reverse-proxies the prompt to the adapter sidecar.
func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	caller, err := s.authenticate(ctx, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, brokerapi.CodeUnauthorized, err.Error())
		return
	}
	ns, name, err := pathRunIdentity(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, err.Error())
		return
	}
	if !caller.IsController && caller.Namespace != ns {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden, "namespace mismatch")
		return
	}

	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, brokerapi.CodeRunNotFound, "run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, brokerapi.CodeProviderFailure, err.Error())
		return
	}

	if run.Spec.Mode != paddockv1alpha1.HarnessRunModeInteractive {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, "run is not Interactive mode")
		return
	}
	if !s.allowInteract(ctx, &run) {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden,
			"no BrokerPolicy grants runs.interact for this run's template")
		return
	}
	// In-flight guard: a non-nil CurrentTurnSeq is the authoritative
	// signal that a prompt is currently being processed by the adapter.
	// We previously gated on Phase==Running, but until the controller's
	// Idle-transition logic lands (separate task), Interactive runs sit
	// in Running constantly — that gate would block every prompt. The
	// turn-seq pointer is set by handlePrompts on forward and cleared by
	// the adapter (per ADR contract) when the turn completes.
	if run.Status.Interactive != nil && run.Status.Interactive.CurrentTurnSeq != nil {
		writeError(w, http.StatusConflict, brokerapi.CodeConflict, "a prompt is already in flight")
		return
	}

	// MaxInlinePromptBytes is the single source of truth for the prompt
	// body cap; +1 KiB of slack allows a max-sized Text to be read fully
	// (JSON envelope + escaping) while short-circuiting a malicious body.
	r.Body = http.MaxBytesReader(w, r.Body, paddockv1alpha1.MaxInlinePromptBytes+1024)
	var body brokerapi.PromptRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, "prompt body too large")
			return
		}
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, fmt.Sprintf("decoding body: %v", err))
		return
	}
	if len(body.Text) == 0 || len(body.Text) > paddockv1alpha1.MaxInlinePromptBytes {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, "prompt empty or too large")
		return
	}

	if s.Renewer != nil {
		updated, rErr := s.Renewer.WalkAndRenew(ctx, ns, name, run.Status.IssuedLeases)
		if rErr != nil {
			logger.Error(rErr, "renewal walk failed", "run", name)
		} else if !equalLeases(updated, run.Status.IssuedLeases) {
			if pErr := s.patchIssuedLeases(ctx, &run, updated); pErr != nil {
				logger.Error(pErr, "patching issuedLeases", "run", name)
			}
		}
	}

	// Invariant: cmd/broker wires Router in production; nil here means a malformed test setup or an incomplete bootstrap.
	if s.Router == nil {
		writeError(w, http.StatusServiceUnavailable, brokerapi.CodeNotConfigured, "interactive router not configured")
		return
	}
	seq := s.Router.NextTurnSeq(ns, name)

	sum := sha256.Sum256([]byte(body.Text))
	hash := "sha256:" + hex.EncodeToString(sum[:])

	if s.Audit != nil {
		ae := auditing.NewPromptSubmitted(auditing.PromptAuditInput{
			RunName:      name,
			Namespace:    ns,
			SubmitterSA:  caller.ServiceAccount,
			PromptHash:   hash,
			PromptLength: len(body.Text),
			TurnSeq:      seq,
			When:         time.Now().UTC(),
		})
		if wErr := s.Audit.Write(ctx, ae); wErr != nil {
			logger.Error(wErr, "writing prompt-submitted audit", "run", name)
			writeError(w, http.StatusServiceUnavailable, brokerapi.CodeAuditUnavailable,
				"paddock-broker: audit unavailable, please retry")
			return
		}
	}

	fwd := struct {
		Text      string `json:"text"`
		Seq       int32  `json:"seq"`
		Submitter string `json:"submitter"`
	}{Text: body.Text, Seq: seq, Submitter: caller.ServiceAccount}
	fwdBody, err := json.Marshal(fwd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, brokerapi.CodeProviderFailure, err.Error())
		return
	}

	// Wrap the writer so we can observe the upstream status code; we
	// only patch Status.Interactive when the forward actually succeeded.
	// Otherwise a 502 (no ready pod) or 5xx from the adapter would
	// strand CurrentTurnSeq=<seq> and the next /prompts would 409 on
	// the in-flight guard with no way for the caller to recover.
	rec := &statusRecorder{ResponseWriter: w}
	s.Router.ForwardPromptWithBody(ctx, rec, r, ns, name, fwdBody)

	if rec.status < 200 || rec.status >= 300 {
		return
	}

	now := nowMeta()
	seqCopy := seq
	// Patch under retry-on-conflict: re-Get inside the loop so each
	// attempt's MergeFrom base reflects the latest ResourceVersion.
	// Plain Update() lost the race when the renewal walker (or another
	// /prompts on a different replica) patched IssuedLeases between our
	// initial Get and the write — leaving CurrentTurnSeq unset and
	// breaking the in-flight 409 guard for subsequent prompts.
	if pErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh paddockv1alpha1.HarnessRun
		if gErr := s.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &fresh); gErr != nil {
			return gErr
		}
		base := fresh.DeepCopy()
		if fresh.Status.Interactive == nil {
			fresh.Status.Interactive = &paddockv1alpha1.InteractiveStatus{}
		}
		fresh.Status.Interactive.PromptCount++
		fresh.Status.Interactive.LastPromptAt = &now
		fresh.Status.Interactive.CurrentTurnSeq = &seqCopy
		fresh.Status.Interactive.IdleSince = nil
		return s.Client.Status().Patch(ctx, &fresh, client.MergeFrom(base))
	}); pErr != nil {
		logger.Error(pErr, "patching interactive status", "run", name)
	}
}

// statusRecorder is a tiny http.ResponseWriter wrapper that captures
// the first status code seen, so handlers can decide whether the
// upstream forward actually succeeded before mutating run state.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write tolerates handlers that call Write without WriteHeader first.
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// handleInterrupt forwards a POST /interrupt to the adapter after the
// same admission checks as /prompts, minus the body parse, renewal,
// turn-allocation, and audit emission.
func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	caller, err := s.authenticate(ctx, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, brokerapi.CodeUnauthorized, err.Error())
		return
	}
	ns, name, err := pathRunIdentity(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, err.Error())
		return
	}
	if !caller.IsController && caller.Namespace != ns {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden, "namespace mismatch")
		return
	}

	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, brokerapi.CodeRunNotFound, "run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, brokerapi.CodeProviderFailure, err.Error())
		return
	}
	if !s.allowInteract(ctx, &run) {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden,
			"no BrokerPolicy grants runs.interact for this run's template")
		return
	}
	// Defensive cap on /interrupt body: InterruptRequest is currently
	// empty, but a junk POST shouldn't be allowed to stream upstream.
	r.Body = http.MaxBytesReader(w, r.Body, interactiveSmallBodyCap)
	// Invariant: cmd/broker wires Router in production; nil here means a malformed test setup or an incomplete bootstrap.
	if s.Router == nil {
		writeError(w, http.StatusServiceUnavailable, brokerapi.CodeNotConfigured, "interactive router not configured")
		return
	}
	s.Router.ForwardInterrupt(ctx, w, r, ns, name)
}

// handleEnd forwards a POST /end to the adapter and emits an
// interactive-run-terminated audit event with the supplied (or default)
// reason.
func (s *Server) handleEnd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	caller, err := s.authenticate(ctx, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, brokerapi.CodeUnauthorized, err.Error())
		return
	}
	ns, name, err := pathRunIdentity(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, err.Error())
		return
	}
	if !caller.IsController && caller.Namespace != ns {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden, "namespace mismatch")
		return
	}

	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, brokerapi.CodeRunNotFound, "run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, brokerapi.CodeProviderFailure, err.Error())
		return
	}
	if !s.allowInteract(ctx, &run) {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden,
			"no BrokerPolicy grants runs.interact for this run's template")
		return
	}

	reason := "explicit"
	// /end's body is optional; ignore decode errors so an empty body
	// still terminates with the default reason. Cap defensively at 1 KiB.
	var body brokerapi.EndRequest
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, interactiveSmallBodyCap)
		if dErr := json.NewDecoder(r.Body).Decode(&body); dErr != nil && dErr != io.EOF {
			logger.V(1).Info("ignoring /end body decode error", "err", dErr.Error(), "ns", ns, "run", name)
		}
	}
	if cleaned := sanitizeReason(body.Reason); cleaned != "" {
		reason = cleaned
	}

	// Invariant: cmd/broker wires Router in production; nil here means a malformed test setup or an incomplete bootstrap.
	if s.Router == nil {
		writeError(w, http.StatusServiceUnavailable, brokerapi.CodeNotConfigured, "interactive router not configured")
		return
	}
	// Wrap the writer so we can observe the upstream status code; we
	// only emit interactive-run-terminated when the forward actually
	// succeeded. Mirrors the pattern in handlePrompts: a 502 from the
	// adapter must not produce a "terminated" audit when no End signal
	// reached the run.
	rec := &statusRecorder{ResponseWriter: w}
	s.Router.ForwardEnd(ctx, rec, r, ns, name)

	if rec.status < 200 || rec.status >= 300 {
		return
	}

	if s.Audit != nil {
		ae := auditing.NewInteractiveRunTerminated(auditing.InteractiveRunTerminatedInput{
			RunName:   name,
			Namespace: ns,
			Reason:    reason,
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			When:      time.Now().UTC(),
		})
		if wErr := s.Audit.Write(ctx, ae); wErr != nil {
			logger.Error(wErr, "writing interactive-run-terminated audit", "run", name)
		}
	}
}

// pathRunIdentity extracts (ns, name) from the ServeMux 1.22+ path
// values. Empty values surface as a 400.
func pathRunIdentity(r *http.Request) (string, string, error) {
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	if ns == "" || name == "" {
		return "", "", fmt.Errorf("path namespace/name required")
	}
	return ns, name, nil
}

// allowInteract reports whether any matching BrokerPolicy grants
// runs.interact for the run's template. Errors are logged and treated
// as deny — fail-closed when policy resolution misbehaves.
func (s *Server) allowInteract(ctx context.Context, run *paddockv1alpha1.HarnessRun) bool {
	logger := log.FromContext(ctx)
	tpl, _, err := policy.ResolveTemplate(ctx, s.Client, run.Namespace, run.Spec.TemplateRef)
	if err != nil {
		logger.Error(err, "resolving template for runs.interact check", "run", run.Name)
		return false
	}
	matching, err := policy.ListMatchingPolicies(ctx, s.Client, run.Namespace, run.Spec.TemplateRef.Name)
	if err != nil {
		logger.Error(err, "listing BrokerPolicies for runs.interact check", "run", run.Name)
		return false
	}
	result := policy.IntersectMatches(matching, tpl.Requires)
	return result.RunsInteract
}

// allowShell returns the merged ShellCapability granted by matching
// BrokerPolicies, or nil when no policy declares one. Errors are
// logged and treated as deny. Wired by handleShell.
func (s *Server) allowShell(ctx context.Context, run *paddockv1alpha1.HarnessRun) *paddockv1alpha1.ShellCapability {
	logger := log.FromContext(ctx)
	tpl, _, err := policy.ResolveTemplate(ctx, s.Client, run.Namespace, run.Spec.TemplateRef)
	if err != nil {
		logger.Error(err, "resolving template for runs.shell check", "run", run.Name)
		return nil
	}
	matching, err := policy.ListMatchingPolicies(ctx, s.Client, run.Namespace, run.Spec.TemplateRef.Name)
	if err != nil {
		logger.Error(err, "listing BrokerPolicies for runs.shell check", "run", run.Name)
		return nil
	}
	return policy.IntersectMatches(matching, tpl.Requires).Shell
}

// equalLeases reports whether two slices share the same Provider,
// LeaseID, and ExpiresAt for every entry in order. Used to decide
// whether the renewal walk produced a status-relevant change.
// Order-sensitive: relies on RenewalWalker.WalkAndRenew preserving input slice order (see internal/broker/renewal.go).
func equalLeases(a, b []paddockv1alpha1.IssuedLease) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Provider != b[i].Provider || a[i].LeaseID != b[i].LeaseID {
			return false
		}
		ax, bx := a[i].ExpiresAt, b[i].ExpiresAt
		switch {
		case ax == nil && bx == nil:
			// equal
		case ax == nil || bx == nil:
			return false
		default:
			if !ax.Equal(bx) {
				return false
			}
		}
	}
	return true
}

// patchIssuedLeases updates run.Status.IssuedLeases via a merge patch.
// Falls back to Status().Update if the apiserver rejects the patch
// (e.g. fake clients without merge-patch support).
func (s *Server) patchIssuedLeases(ctx context.Context, run *paddockv1alpha1.HarnessRun, updated []paddockv1alpha1.IssuedLease) error {
	patch := client.MergeFrom(run.DeepCopy())
	run.Status.IssuedLeases = updated
	if err := s.Client.Status().Patch(ctx, run, patch); err != nil {
		return s.Client.Status().Update(ctx, run)
	}
	return nil
}

func nowMeta() metav1.Time {
	return metav1.NewTime(time.Now().UTC())
}
