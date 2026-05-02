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

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/auditing"
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

// EmitCAProjected records the controller's first touch of per-run CA
// material in a tenant namespace — either creating the
// <run>-broker-ca Secret directly (broker-ca path), or creating the
// cert-manager Certificate that produces <run>-proxy-tls (proxy-tls
// path). Same operator-visible audit semantics across both paths;
// different K8s resource. F-18 / Phase 2f flipped the proxy-tls path
// from a Secret-byte-copy to a Certificate-create.
func (c *ControllerAudit) EmitCAProjected(ctx context.Context, runName, namespace, secretName string) {
	c.write(ctx, auditing.NewCAProjected(auditing.CAProjectionInput{
		RunName:    runName,
		Namespace:  namespace,
		SecretName: secretName,
		Reason:     "controller projected " + secretName,
	}), "ca-projected")
}

// EmitWorkspaceCAMisconfigured records a terminal CA-misconfigured event
// for a Workspace whose source-Secret key is missing/empty (typo'd key,
// blanked source) or whose cert-manager Certificate has hit a permanent
// failure. The wsName argument is the Workspace name; it is prefixed
// seed- here to match the F-52 runRef convention. F-51.
//
// Named EmitWorkspaceCAMisconfigured (not EmitCAMisconfigured) so Theme 6
// can add EmitRunCAMisconfigured without prefix duplication. I2.
func (c *ControllerAudit) EmitWorkspaceCAMisconfigured(ctx context.Context, wsName, namespace, reason string) {
	c.write(ctx, auditing.NewCAMisconfigured(auditing.CAMisconfiguredInput{
		Name:      "seed-" + wsName,
		Namespace: namespace,
		Reason:    reason,
	}), "ca-misconfigured")
}

// EmitRunCAMisconfigured records a terminal CA-misconfigured event for
// a HarnessRun whose source-Secret key is missing/empty (typo'd key,
// blanked source) or whose cert-manager Certificate has hit a permanent
// failure. F-44.
//
// Companion to EmitWorkspaceCAMisconfigured; both share the
// AuditKindCAMisconfigured kind and "ca-misconfigured" log action.
func (c *ControllerAudit) EmitRunCAMisconfigured(ctx context.Context, runName, namespace, reason string) {
	c.write(ctx, auditing.NewCAMisconfigured(auditing.CAMisconfiguredInput{
		Name:      runName,
		Namespace: namespace,
		Reason:    reason,
	}), "ca-misconfigured")
}

// EmitNetworkPolicyEnforcementWithdrawn is called when the reconciler
// observes that a run admitted with enforcement=true no longer has its
// NetworkPolicy (e.g., operator deleted it via kubectl). The reconciler
// re-creates the NP on the same pass; this audit records the
// withdrawal attempt for the operator's trail. F-43 / Phase 2d.
func (c *ControllerAudit) EmitNetworkPolicyEnforcementWithdrawn(ctx context.Context, runName, namespace, reason string) {
	c.write(ctx, auditing.NewNetworkPolicyEnforcementWithdrawn(auditing.NetworkPolicyEnforcementWithdrawnInput{
		RunName:   runName,
		Namespace: namespace,
		Reason:    reason,
	}), "network-policy-enforcement-withdrawn")
}

// EmitInteractiveRunTerminated records a watchdog-driven termination of an
// Interactive run. Reason is one of "idle", "detach", "max-lifetime"
// (matching watchdogAction.Reason()). Decision is always Granted because
// these are planned terminations under operator-configured timeouts; an
// error-driven termination would route through EmitRunFailed instead.
func (c *ControllerAudit) EmitInteractiveRunTerminated(ctx context.Context, runName, namespace, reason string) {
	c.write(ctx, auditing.NewInteractiveRunTerminated(auditing.InteractiveRunTerminatedInput{
		RunName:   runName,
		Namespace: namespace,
		Reason:    reason,
		Decision:  paddockv1alpha1.AuditDecisionGranted,
	}), "interactive-run-terminated")
}

// EmitBrokerCredsTampered records that the controller detected
// unexpected keys on a run's broker-creds Secret (e.g., a tenant
// `kubectl edit secret <run>-broker-creds` injecting an extra envFrom
// key) and pruned them via CreateOrUpdate. prunedKeys is the sorted
// list of removed key names; values are never recorded. F-41 residual.
func (c *ControllerAudit) EmitBrokerCredsTampered(ctx context.Context, runName, namespace string, prunedKeys []string) {
	c.write(ctx, auditing.NewBrokerCredsTampered(auditing.BrokerCredsTamperedInput{
		RunName:    runName,
		Namespace:  namespace,
		PrunedKeys: prunedKeys,
	}), "broker-creds-tampered")
}
