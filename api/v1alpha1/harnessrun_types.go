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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HarnessRunSpec is the desired state of a HarnessRun — a single,
// terminating invocation of a harness against an optional workspace.
// The spec is immutable after creation (enforced by the admission webhook):
// to change the configuration of a run, submit a new one.
type HarnessRunSpec struct {
	// TemplateRef identifies which HarnessTemplate or ClusterHarnessTemplate
	// the run uses. Resolution tries namespaced first, then cluster.
	// +kubebuilder:validation:Required
	TemplateRef TemplateRef `json:"templateRef"`

	// WorkspaceRef names the Workspace this run mounts. Required when the
	// resolved template declares workspace.required=true and auto-provision
	// is disabled; otherwise the controller creates an ephemeral Workspace
	// (see ADR-0004).
	// +optional
	WorkspaceRef string `json:"workspaceRef,omitempty"`

	// Prompt is the inline prompt supplied to the agent. Exactly one of
	// Prompt or PromptFrom must be set (enforced by admission webhook).
	// Capped at 256 KiB — use PromptFrom for anything larger.
	//
	// Regardless of source, the controller materialises the prompt into
	// an owned Secret (<run>-prompt, SecretTypeOpaque, key "prompt.txt")
	// and mounts it at /paddock/prompt/prompt.txt. See ADR-0011.
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// PromptFrom sources the prompt from a ConfigMap or Secret. The
	// resolved content is copied into an owned Secret (see Prompt).
	// +optional
	PromptFrom *PromptSource `json:"promptFrom,omitempty"`

	// Timeout overrides the template's default timeout.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Retries is the Job backoffLimit. Defaults to 0 — agent failures do
	// not re-run unless explicitly requested.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	Retries int32 `json:"retries,omitempty"`

	// Resources override the template's default resource requests/limits.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ExtraEnv adds env vars to the agent container, merged after the
	// template's defaults and the PADDOCK_* standard variables.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`

	// Model overrides the template's default model (exported as
	// PADDOCK_MODEL).
	// +optional
	Model string `json:"model,omitempty"`

	// TTLSecondsAfterFinished, when set, deletes the HarnessRun that many
	// seconds after its terminal phase is reached. No default — matches
	// the batch/v1 Job convention.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// Mode selects Batch (default — one-shot) or Interactive (long-lived
	// pod, multi-prompt). When Interactive, the resolved template's
	// spec.interactive.mode must be non-empty (admission webhook
	// enforces). Immutable after creation, like the rest of the spec.
	// +optional
	Mode HarnessRunMode `json:"mode,omitempty"`

	// InteractiveOverrides allows per-run overrides of the template's
	// interactive timing values. Each override is bounded by the
	// template's value (override may not exceed the template's bound).
	// Ignored unless Mode == Interactive.
	// +optional
	InteractiveOverrides *InteractiveOverrides `json:"interactiveOverrides,omitempty"`
}

// InteractiveOverrides are per-run knobs for an Interactive run.
type InteractiveOverrides struct {
	// +optional
	IdleTimeout *metav1.Duration `json:"idleTimeout,omitempty"`
	// +optional
	DetachIdleTimeout *metav1.Duration `json:"detachIdleTimeout,omitempty"`
	// +optional
	DetachTimeout *metav1.Duration `json:"detachTimeout,omitempty"`
	// +optional
	MaxLifetime *metav1.Duration `json:"maxLifetime,omitempty"`
}

// TemplateRef identifies a HarnessTemplate or ClusterHarnessTemplate.
type TemplateRef struct {
	// Name of the template.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Kind restricts resolution. When empty, a namespaced HarnessTemplate
	// with this name is preferred over a cluster one.
	// +kubebuilder:validation:Enum=HarnessTemplate;ClusterHarnessTemplate
	// +optional
	Kind string `json:"kind,omitempty"`
}

// PromptSource sources the run's prompt from a ConfigMap or Secret.
// Exactly one field must be set (enforced by admission webhook).
type PromptSource struct {
	// +optional
	ConfigMapKeyRef *corev1.ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`
	// +optional
	SecretKeyRef *corev1.SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// HarnessRunPhase is the lifecycle phase of a HarnessRun.
// +kubebuilder:validation:Enum=Pending;Running;Idle;Succeeded;Failed;Cancelled
type HarnessRunPhase string

const (
	HarnessRunPhasePending   HarnessRunPhase = "Pending"
	HarnessRunPhaseRunning   HarnessRunPhase = "Running"
	// HarnessRunPhaseIdle indicates an Interactive run is alive and waiting
	// for the next prompt. The pod is running; no turn is in progress.
	HarnessRunPhaseIdle      HarnessRunPhase = "Idle"
	HarnessRunPhaseSucceeded HarnessRunPhase = "Succeeded"
	HarnessRunPhaseFailed    HarnessRunPhase = "Failed"
	HarnessRunPhaseCancelled HarnessRunPhase = "Cancelled"
)

// HarnessRunMode is the run-mode selector. Empty (default) means Batch —
// today's behaviour, one prompt and the run terminates. Interactive runs
// keep the pod alive and accept multiple prompts over time via the
// broker's /v1/runs/{ns}/{name}/prompts endpoint.
// +kubebuilder:validation:Enum="";Batch;Interactive
type HarnessRunMode string

const (
	HarnessRunModeBatch       HarnessRunMode = "Batch"
	HarnessRunModeInteractive HarnessRunMode = "Interactive"
)

// Condition types reported on HarnessRun.status.conditions.
const (
	HarnessRunConditionTemplateResolved = "TemplateResolved"
	HarnessRunConditionWorkspaceBound   = "WorkspaceBound"
	HarnessRunConditionPromptResolved   = "PromptResolved"
	HarnessRunConditionJobCreated       = "JobCreated"
	HarnessRunConditionPodReady         = "PodReady"
	HarnessRunConditionCompleted        = "Completed"
	// BrokerReady indicates the broker issued every credential the
	// template's requires block declares. Wired in v0.3 M3 with the
	// broker skeleton.
	HarnessRunConditionBrokerReady = "BrokerReady"
	// EgressConfigured indicates the proxy sidecar's CA bundle is
	// mounted and the interception mode has been resolved (transparent
	// or cooperative). Wired in v0.3 M4 with the proxy sidecar.
	HarnessRunConditionEgressConfigured = "EgressConfigured"
	// BrokerCredentialsReady summarises whether all requires.credentials
	// were issued, and on True carries a short message like
	// "3 credentials issued: 2 proxy-injected, 1 in-container".
	HarnessRunConditionBrokerCredentialsReady = "BrokerCredentialsReady"
	// InterceptionUnavailable signals that the BrokerPolicy (explicitly
	// or by default) required transparent interception but the run's
	// namespace PSA or the manager's configuration cannot provide it.
	// The run is terminal Failed; no fallback to cooperative.
	HarnessRunConditionInterceptionUnavailable = "InterceptionUnavailable"
)

// Condition types specific to Interactive HarnessRuns.
const (
	// HarnessRunConditionAttached is True when at least one client session
	// is currently attached to the Interactive run's prompt or shell
	// endpoint. Message breaks down session counts when more than one is
	// attached.
	HarnessRunConditionAttached = "Attached"
	// HarnessRunConditionIdle is True while the Interactive run is in the
	// Idle phase — pod alive, no prompt turn in progress.
	HarnessRunConditionIdle = "Idle"
	// HarnessRunConditionCredentialsRenewed is True after the broker has
	// completed at least one credential renewal for this Interactive run.
	HarnessRunConditionCredentialsRenewed = "CredentialsRenewed"
)

// MaxInlinePromptBytes caps spec.prompt at 256 KiB, well under the
// 1 MiB ConfigMap/Secret ceiling and leaving headroom for the
// materialisation wrapper. promptFrom sources are not size-checked at
// admission time — doing so would require cluster reads and make
// validation non-static; oversized Secret/ConfigMap-sourced prompts
// fail later at the reconciler's materialise step.
//
// The CLI reuses this value to cap --prompt-file/stdin reads
// client-side (see internal/cli/submit.go) so an oversized file
// errors before being POSTed.
const MaxInlinePromptBytes = 256 * 1024

// HarnessRunStatus reports the observed state of a HarnessRun.
type HarnessRunStatus struct {
	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase summarises the run's lifecycle in a single token.
	// +optional
	Phase HarnessRunPhase `json:"phase,omitempty"`

	// Conditions report typed lifecycle signals. Known types:
	// TemplateResolved, WorkspaceBound, JobCreated, PodReady, Completed.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// JobName is the name of the backing batch/v1 Job, once created.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// WorkspaceRef records which Workspace this run ended up bound to
	// (either user-supplied or ephemerally provisioned).
	// +optional
	WorkspaceRef string `json:"workspaceRef,omitempty"`

	// StartTime is when the run's Job entered Running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the run reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// RecentEvents is a ring buffer of the most recent PaddockEvents
	// emitted during the run (capped by controller flag, default 50).
	// Full event history lives on the Workspace PVC at
	// /workspace/.paddock/runs/<name>/events.jsonl.
	// +optional
	RecentEvents []PaddockEvent `json:"recentEvents,omitempty"`

	// Outputs are structured outputs reported by the harness on exit.
	// +optional
	Outputs *HarnessRunOutputs `json:"outputs,omitempty"`

	// Credentials reports, per requires.credentials[*].name, which
	// provider backed it and how the value was delivered. Populated by
	// the controller after a successful Issue call to the broker. Lets
	// the user verify at runtime that the actual delivery matches the
	// policy's declaration.
	// +listType=map
	// +listMapKey=name
	// +optional
	Credentials []CredentialStatus `json:"credentials,omitempty"`

	// NetworkPolicyEnforced records whether per-run NetworkPolicy
	// enforcement was active when this run was admitted. Immutable after
	// admission. The reconciler honours this for the run's lifetime, so a
	// flag flip on the controller-manager
	// (--networkpolicy-enforce=on → off) does not weaken running pods.
	// F-43 / Phase 2d.
	// +optional
	NetworkPolicyEnforced *bool `json:"networkPolicyEnforced,omitempty"`

	// IssuedLeases records every credential lease the broker has minted
	// for this run. Populated by the controller after each successful
	// Issue call; consumed by the controller's broker-leases finalizer
	// to revoke leases at run-delete time, and by the broker on startup
	// to reconstruct PATPool slot reservations across restarts. F-11, F-14.
	// +listType=map
	// +listMapKey=leaseID
	// +optional
	IssuedLeases []IssuedLease `json:"issuedLeases,omitempty"`

	// Interactive carries live-session counters and timestamps for
	// Interactive runs. Nil for Batch runs.
	// +optional
	Interactive *InteractiveStatus `json:"interactive,omitempty"`
}

// InteractiveStatus carries counters and timestamps for an Interactive run.
// Populated and updated by the controller as prompts arrive and sessions
// attach/detach.
type InteractiveStatus struct {
	// PromptCount is the total number of prompt turns submitted since the
	// run started. Always serialized (no omitempty) — zero is a real
	// observable value for status consumers.
	PromptCount int32 `json:"promptCount"`

	// LastPromptAt is the time the most recent prompt was received.
	// +optional
	LastPromptAt *metav1.Time `json:"lastPromptAt,omitempty"`

	// AttachedSessions is the current number of client sessions attached
	// to the run's prompt stream or shell endpoint. Always serialized.
	AttachedSessions int32 `json:"attachedSessions"`

	// LastAttachedAt is the time the most recent session attached.
	// +optional
	LastAttachedAt *metav1.Time `json:"lastAttachedAt,omitempty"`

	// IdleSince is the time the run entered the Idle phase most recently.
	// Nil if the run has never been idle.
	// +optional
	IdleSince *metav1.Time `json:"idleSince,omitempty"`

	// CurrentTurnSeq is the monotonically increasing sequence number of
	// the prompt turn currently in progress. Nil when no turn is active.
	// +optional
	CurrentTurnSeq *int32 `json:"currentTurnSeq,omitempty"`

	// RenewalCount is the total number of credential renewals completed
	// for this run. Always serialized.
	RenewalCount int32 `json:"renewalCount"`

	// LastRenewalAt is the time of the most recent credential renewal.
	// +optional
	LastRenewalAt *metav1.Time `json:"lastRenewalAt,omitempty"`
}

// PaddockEvent is a structured event emitted by an adapter sidecar and
// persisted to the Workspace PVC as events.jsonl. A ring buffer of the
// most recent events is also surfaced on HarnessRun.status.recentEvents.
//
// See docs/contributing/adr/0001-paddockevent-schema-version.md for versioning rules.
type PaddockEvent struct {
	// SchemaVersion governs the semantics of this event. Bump when the
	// semantics of existing fields change; add optional fields without
	// bumping. See ADR-0001.
	// +kubebuilder:default="1"
	SchemaVersion string `json:"schemaVersion"`

	// Timestamp is when the event was produced.
	Timestamp metav1.Time `json:"ts"`

	// Type identifies the event category. Known types: ToolUse, Message,
	// FileEdit, Commit, Elicitation, Error, Result. Adapters may emit
	// custom types for harness-specific events; consumers should tolerate
	// unknown types.
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Summary is a one-line human-readable description of the event.
	// Suitable for display in kubectl output without interpreting the
	// type-specific fields.
	// +optional
	Summary string `json:"summary,omitempty"`

	// Fields carries event-specific details as string-valued key/value
	// pairs. Its schema is determined by Type and SchemaVersion; consumers
	// should tolerate unknown keys.
	// +optional
	Fields map[string]string `json:"fields,omitempty"`
}

// HarnessRunOutputs are structured outputs reported by the harness.
// Populated by the controller from the harness's result.json on the
// workspace at Job completion.
type HarnessRunOutputs struct {
	// PullRequests opened by the run.
	// +optional
	PullRequests []string `json:"pullRequests,omitempty"`

	// FilesChanged is the count of files modified by the run.
	// +optional
	FilesChanged int32 `json:"filesChanged,omitempty"`

	// Summary is a human-readable summary of what the run accomplished.
	// +optional
	Summary string `json:"summary,omitempty"`

	// Artifacts are additional named outputs (URIs or file paths).
	// +optional
	Artifacts []string `json:"artifacts,omitempty"`
}

// CredentialStatus describes one issued credential from the run's
// perspective.
type CredentialStatus struct {
	// Name matches the template's requires.credentials[*].name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Provider is the backing provider kind (e.g. "UserSuppliedSecret",
	// "AnthropicAPI"). Copied from the matched grant.
	Provider string `json:"provider"`

	// DeliveryMode is "ProxyInjected" or "InContainer".
	// +kubebuilder:validation:Enum=ProxyInjected;InContainer
	DeliveryMode DeliveryModeName `json:"deliveryMode"`

	// Hosts lists the destination hostnames this credential substitutes
	// on, for ProxyInjected delivery. Empty for InContainer.
	// +optional
	Hosts []string `json:"hosts,omitempty"`

	// InContainerReason mirrors the policy grant's
	// deliveryMode.inContainer.reason when DeliveryMode is InContainer.
	// +optional
	InContainerReason string `json:"inContainerReason,omitempty"`
}

// IssuedLease records one credential lease the broker minted for this
// run. The controller appends one entry per successful broker.Issue call;
// reconcileDelete walks the slice and posts /v1/revoke for each entry
// before removing the broker-leases finalizer. Pre-1.0 evolves in place.
type IssuedLease struct {
	// Provider is the provider kind (matches BrokerPolicy
	// grant.provider.kind). The broker dispatches /v1/revoke to the
	// named provider's Revoke method.
	// +kubebuilder:validation:Required
	Provider string `json:"provider"`

	// LeaseID is the provider-supplied identifier returned from
	// IssueResult.LeaseID. Opaque to the controller; passed back unchanged.
	// +kubebuilder:validation:Required
	LeaseID string `json:"leaseID"`

	// CredentialName is the requirement name from the template's
	// spec.requires.credentials list. Used for audit correlation only —
	// never load-bearing for revocation.
	// +kubebuilder:validation:Required
	CredentialName string `json:"credentialName"`

	// ExpiresAt mirrors IssueResult.ExpiresAt. Reconstruction skips
	// entries with ExpiresAt < now (no point rebuilding state for an
	// already-dead lease). Optional: nil means "no expiry".
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// PoolRef carries PATPool-specific reconstruction metadata.
	// Populated only when Provider == "PATPool"; nil otherwise.
	// Anonymous tagged-union pattern; future providers add their own
	// optional ref alongside without breaking pre-1.0 in-place evolution.
	// +optional
	PoolRef *PoolLeaseRef `json:"poolRef,omitempty"`
}

// PoolLeaseRef is PATPool-specific metadata required to reconstruct
// in-memory pool state at broker startup. (secretRef, slotIndex) lets
// the broker re-acquire the slot; the existing LeasedPAT byte-equality
// check at substitute time catches any pool-edit drift between Issue
// and reconstruction.
type PoolLeaseRef struct {
	// +kubebuilder:validation:Required
	SecretRef SecretKeyReference `json:"secretRef"`
	// +kubebuilder:validation:Required
	SlotIndex int `json:"slotIndex"`
}

// DeliveryModeName names one of the two status-reported delivery modes.
type DeliveryModeName string

const (
	DeliveryModeProxyInjected DeliveryModeName = "ProxyInjected"
	DeliveryModeInContainer   DeliveryModeName = "InContainer"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=hr
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateRef.name`
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.status.workspaceRef`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Credentials",type=string,JSONPath=`.status.conditions[?(@.type=="BrokerCredentialsReady")].message`

// HarnessRun is a single invocation of a harness. It materialises into a
// batch/v1 Job with an agent container, an optional adapter sidecar, and
// a collector sidecar. Runs terminate — continuity across follow-up runs
// comes from the shared Workspace, not from long-lived processes.
type HarnessRun struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec HarnessRunSpec `json:"spec"`
	// +optional
	Status HarnessRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// HarnessRunList contains a list of HarnessRun.
type HarnessRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []HarnessRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HarnessRun{}, &HarnessRunList{})
}
