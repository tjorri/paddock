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

package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
)

// ControllerAudit wraps an auditing.Sink with the controller's per-emit
// helpers. All helpers are fail-open: errors are logged at Error level
// but never returned to the caller; status conditions are the canonical
// signal for run state. F-40.
type ControllerAudit struct {
	Sink auditing.Sink
}

func (c *ControllerAudit) write(ctx context.Context, ae *paddockv1alpha1.AuditEvent, action string) {
	if c == nil || c.Sink == nil {
		return
	}
	if err := c.Sink.Write(ctx, ae); err != nil {
		log.FromContext(ctx).Error(err, "writing AuditEvent",
			"action", action, "kind", ae.Spec.Kind)
	}
}

// EmitCredentialIssuedSummary records that the controller projected
// `count` credentials into <run>-broker-creds. Disambiguated from the
// broker's per-credential events by the paddock.dev/component label
// (set by KubeSink) and a non-zero Spec.Count.
func (c *ControllerAudit) EmitCredentialIssuedSummary(ctx context.Context, runName, namespace string, count int) {
	c.write(ctx, auditing.NewCredentialIssued(auditing.CredentialIssuedInput{
		RunName:   runName,
		Namespace: namespace,
		Reason:    "controller projected credentials into <run>-broker-creds",
		Count:     int32(count), //nolint:gosec // bounded by template's requires.credentials count
	}), "credential-issued-summary")
}

// EmitRunFailed records a terminal-failure decision (BrokerDenied,
// WorkspaceRequired, etc.). Reason carries the failure cause; message
// the user-facing detail.
func (c *ControllerAudit) EmitRunFailed(ctx context.Context, runName, namespace, reason, message string) {
	c.write(ctx, auditing.NewRunFailed(auditing.RunDecisionInput{
		RunName:   runName,
		Namespace: namespace,
		Decision:  paddockv1alpha1.AuditDecisionDenied,
		Reason:    reason + ": " + message,
	}), "run-failed")
}

// EmitRunCompleted records a terminal-phase commit (Succeeded /
// Failed / Cancelled). Decision is granted on Succeeded, denied on
// Failed, warned on Cancelled.
func (c *ControllerAudit) EmitRunCompleted(ctx context.Context, runName, namespace string, decision paddockv1alpha1.AuditDecision, reason string) {
	c.write(ctx, auditing.NewRunCompleted(auditing.RunDecisionInput{
		RunName:   runName,
		Namespace: namespace,
		Decision:  decision,
		Reason:    reason,
	}), "run-completed")
}

// EmitCAProjected records that the controller created the per-run
// <run>-broker-ca or <run>-proxy-tls Secret.
func (c *ControllerAudit) EmitCAProjected(ctx context.Context, runName, namespace, secretName string) {
	c.write(ctx, auditing.NewCAProjected(auditing.CAProjectionInput{
		RunName:    runName,
		Namespace:  namespace,
		SecretName: secretName,
		Reason:     "controller projected " + secretName,
	}), "ca-projected")
}
