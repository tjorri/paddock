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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Standard label keys on every AuditEvent, intended for ad-hoc queries
// (`kubectl get auditevents -l paddock.dev/run=…`). Keep in sync with
// the emitter code in the broker and proxy.
const (
	AuditEventLabelRun       = "paddock.dev/run"
	AuditEventLabelDecision  = "paddock.dev/decision"
	AuditEventLabelKind      = "paddock.dev/kind"
	AuditEventLabelComponent = "paddock.dev/component"
)

// AuditDecision is the outcome recorded on an AuditEvent.
// +kubebuilder:validation:Enum=granted;denied;warned
type AuditDecision string

const (
	AuditDecisionGranted AuditDecision = "granted"
	AuditDecisionDenied  AuditDecision = "denied"
	AuditDecisionWarned  AuditDecision = "warned"
)

// AuditKind names the category of a recorded decision. See spec 0002 §9
// for the full taxonomy.
// +kubebuilder:validation:Enum=credential-issued;credential-denied;credential-renewed;credential-revoked;egress-allow;egress-block;egress-block-summary;egress-discovery-allow;policy-applied;policy-rejected;broker-unavailable;run-failed;run-completed;ca-projected;network-policy-enforcement-withdrawn;ca-misconfigured
type AuditKind string

const (
	AuditKindCredentialIssued                  AuditKind = "credential-issued"
	AuditKindCredentialDenied                  AuditKind = "credential-denied"
	AuditKindCredentialRenewed                 AuditKind = "credential-renewed"
	AuditKindCredentialRevoked                 AuditKind = "credential-revoked"
	AuditKindEgressAllow                       AuditKind = "egress-allow"
	AuditKindEgressBlock                       AuditKind = "egress-block"
	AuditKindEgressBlockSummary                AuditKind = "egress-block-summary"
	AuditKindEgressDiscoveryAllow              AuditKind = "egress-discovery-allow"
	AuditKindPolicyApplied                     AuditKind = "policy-applied"
	AuditKindPolicyRejected                    AuditKind = "policy-rejected"
	AuditKindBrokerUnavailable                 AuditKind = "broker-unavailable"
	AuditKindRunFailed                         AuditKind = "run-failed"
	AuditKindRunCompleted                      AuditKind = "run-completed"
	AuditKindCAProjected                       AuditKind = "ca-projected"
	AuditKindNetworkPolicyEnforcementWithdrawn AuditKind = "network-policy-enforcement-withdrawn"
	AuditKindCAMisconfigured                   AuditKind = "ca-misconfigured"
)

// AuditEventSpec records one security-relevant decision. Write-once:
// the admission webhook rejects updates to spec. Status is intentionally
// empty. See ADR-0016.
type AuditEventSpec struct {
	// RunRef identifies the HarnessRun this decision pertains to. May be
	// empty for events emitted outside a run context (e.g. broker
	// startup diagnostics — not currently emitted).
	//
	// Names prefixed "seed-" denote a workspace-seed-time decision; the
	// suffix is the Workspace name (F-52).
	// +optional
	RunRef *LocalObjectReference `json:"runRef,omitempty"`

	// Decision is the outcome: granted, denied, or warned.
	// +kubebuilder:validation:Required
	Decision AuditDecision `json:"decision"`

	// Kind categorises the event. Shape of Destination, Credential, and
	// Policy fields depends on Kind.
	// +kubebuilder:validation:Required
	Kind AuditKind `json:"kind"`

	// Timestamp is when the decision was taken. Set by the emitter —
	// not metadata.creationTimestamp, which records when the object
	// landed in etcd (can lag materially under load).
	// +kubebuilder:validation:Required
	Timestamp metav1.Time `json:"timestamp"`

	// Destination is set for egress-* and credential-* kinds that target
	// an upstream service.
	// +optional
	Destination *AuditDestination `json:"destination,omitempty"`

	// Credential is set for credential-* kinds; names the logical
	// credential and the backing provider.
	// +optional
	Credential *AuditCredentialRef `json:"credential,omitempty"`

	// MatchedPolicy is the BrokerPolicy whose grant covered this
	// decision, or nil on a deny.
	// +optional
	MatchedPolicy *LocalObjectReference `json:"matchedPolicy,omitempty"`

	// Reason is a human-readable explanation. For denials, includes the
	// specific rule that failed.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Reason string `json:"reason,omitempty"`

	// Count is the number of events collapsed into this record. Set
	// only for egress-block-summary and similar summary kinds; otherwise
	// implicitly 1.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count,omitempty"`

	// SampleDestinations carries up to three example destinations when
	// Kind is a summary. Purely diagnostic.
	// +optional
	// +kubebuilder:validation:MaxItems=3
	SampleDestinations []AuditDestination `json:"sampleDestinations,omitempty"`

	// WindowStart and WindowEnd delimit the time range a summary event
	// covers. Set only for summary kinds.
	// +optional
	WindowStart *metav1.Time `json:"windowStart,omitempty"`

	// +optional
	WindowEnd *metav1.Time `json:"windowEnd,omitempty"`
}

// AuditDestination describes an upstream target.
type AuditDestination struct {
	// Host is the destination hostname.
	// +kubebuilder:validation:Required
	Host string `json:"host"`

	// Port is the destination TCP port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
}

// AuditCredentialRef names a logical credential involved in a decision.
type AuditCredentialRef struct {
	// Name is the credential's logical name (matches the template's
	// requires.credentials[*].name).
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Provider is the provider kind that handled (or would have handled)
	// the request. See ADR-0015.
	// +optional
	Provider string `json:"provider,omitempty"`

	// Purpose is the requested purpose ("llm", "gitforge", "generic").
	// +optional
	Purpose string `json:"purpose,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ae
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="Decision",type=string,JSONPath=`.spec.decision`
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.spec.runRef.name`
// +kubebuilder:printcolumn:name="When",type=date,JSONPath=`.spec.timestamp`

// AuditEvent records one security-relevant decision made by the broker,
// proxy, webhook, or reconciler. Write-once: spec is set at creation and
// immutable. A TTL reconciler in the controller-manager reaps events
// older than --audit-retention-days (default 30). See ADR-0016.
type AuditEvent struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec AuditEventSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// AuditEventList contains a list of AuditEvent.
type AuditEventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AuditEvent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuditEvent{}, &AuditEventList{})
}
