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
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// PromptFrom sources the prompt from a ConfigMap or Secret.
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
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Cancelled
type HarnessRunPhase string

const (
	HarnessRunPhasePending   HarnessRunPhase = "Pending"
	HarnessRunPhaseRunning   HarnessRunPhase = "Running"
	HarnessRunPhaseSucceeded HarnessRunPhase = "Succeeded"
	HarnessRunPhaseFailed    HarnessRunPhase = "Failed"
	HarnessRunPhaseCancelled HarnessRunPhase = "Cancelled"
)

// Condition types reported on HarnessRun.status.conditions.
const (
	HarnessRunConditionTemplateResolved = "TemplateResolved"
	HarnessRunConditionWorkspaceBound   = "WorkspaceBound"
	HarnessRunConditionPromptResolved   = "PromptResolved"
	HarnessRunConditionJobCreated       = "JobCreated"
	HarnessRunConditionPodReady         = "PodReady"
	HarnessRunConditionCompleted        = "Completed"
)

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
}

// PaddockEvent is a structured event emitted by an adapter sidecar and
// persisted to the Workspace PVC as events.jsonl. A ring buffer of the
// most recent events is also surfaced on HarnessRun.status.recentEvents.
//
// See docs/adr/0001-paddockevent-schema-version.md for versioning rules.
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

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=hr
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.templateRef.name`
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.status.workspaceRef`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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
