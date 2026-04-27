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

// HarnessTemplateSpec is the shared spec for ClusterHarnessTemplate and
// namespaced HarnessTemplate. A namespaced HarnessTemplate may reference a
// ClusterHarnessTemplate via BaseTemplateRef to inherit its pod shape; in
// that case only Defaults, Requires, and PodTemplateOverlay may be set.
// See docs/contributing/adr/0003-template-override-semantics.md.
type HarnessTemplateSpec struct {
	// BaseTemplateRef, when set on a namespaced HarnessTemplate, inherits
	// the referenced ClusterHarnessTemplate's pod shape. Locked fields
	// (Image, Command, Args, EventAdapter, Workspace) must be empty on
	// the inheriting template. Not valid on ClusterHarnessTemplate.
	// +optional
	BaseTemplateRef *LocalObjectReference `json:"baseTemplateRef,omitempty"`

	// Harness is a free-form label identifying the agent (codex, claude-code,
	// opencode, etc.). Used for observability and filtering only — the
	// controller has no per-harness logic.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Harness string `json:"harness,omitempty"`

	// Image is the agent container image. Required when BaseTemplateRef is
	// not set. Locked (must be empty) when inheriting.
	// +optional
	Image string `json:"image,omitempty"`

	// Command overrides the image's entrypoint. Env-var expansion via
	// $(VAR) is supported; the controller injects PADDOCK_PROMPT_PATH,
	// PADDOCK_RAW_PATH, PADDOCK_EVENTS_PATH, PADDOCK_RESULT_PATH,
	// PADDOCK_WORKSPACE, PADDOCK_RUN_NAME, and PADDOCK_MODEL. Required
	// when BaseTemplateRef is not set. Locked when inheriting.
	// +optional
	Command []string `json:"command,omitempty"`

	// Args are merged after Command. Locked when inheriting.
	// +optional
	Args []string `json:"args,omitempty"`

	// Defaults are per-template values that a HarnessRun may override.
	// Always overridable on namespaced templates.
	// +optional
	Defaults HarnessTemplateDefaults `json:"defaults,omitempty"`

	// EventAdapter is the per-harness sidecar image that converts raw
	// harness output to PaddockEvents. When unset, events.jsonl is not
	// produced and status.recentEvents carries only lifecycle events.
	// Locked when inheriting.
	// +optional
	EventAdapter *EventAdapterSpec `json:"eventAdapter,omitempty"`

	// Requires declares the capabilities the agent will exercise at
	// runtime: credentials it expects to be injected, and upstream
	// destinations it will connect to. A HarnessRun against this template
	// is admitted only if one or more BrokerPolicies in the run's
	// namespace grant a superset of Requires. See ADR-0014 and spec 0002
	// §8. Always overridable on namespaced templates.
	// +optional
	Requires RequireSpec `json:"requires,omitempty"`

	// Workspace declares the template's workspace requirement. Locked when
	// inheriting.
	// +optional
	Workspace WorkspaceRequirement `json:"workspace,omitempty"`

	// PodTemplateOverlay is strategically merged into the generated
	// PodSpec. Escape hatch for scheduling hints, tolerations, or extra
	// volumes. Always overridable on namespaced templates.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	PodTemplateOverlay *corev1.PodTemplateSpec `json:"podTemplateOverlay,omitempty"`
}

// HarnessTemplateDefaults are the run-time defaults a template applies.
type HarnessTemplateDefaults struct {
	// Model is the default model identifier exported to the agent as
	// PADDOCK_MODEL. Overridable per-run.
	// +optional
	Model string `json:"model,omitempty"`

	// Timeout is the default active deadline for a run. Overridable per-run.
	// +kubebuilder:default="30m"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Resources are the default resource requests/limits for the agent
	// container. Overridable per-run.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// TerminationGracePeriodSeconds is the grace period for SIGTERM →
	// SIGKILL when a run is cancelled or times out.
	// +kubebuilder:default=60
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`
}

// EventAdapterSpec identifies the adapter sidecar image.
type EventAdapterSpec struct {
	// Image is the adapter sidecar image reference.
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// ImagePullPolicy overrides the default pull policy for the adapter.
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
}

// RequireSpec declares the capabilities a template's agent exercises.
// Templates state needs; BrokerPolicies grant them; admission intersects.
// See ADR-0014.
type RequireSpec struct {
	// Credentials are the credentials the agent expects to be injected
	// as env vars. Name is the env-var key inside the agent container.
	// Templates never name a provider directly — that is the
	// operator's choice via BrokerPolicy.
	// +optional
	Credentials []CredentialRequirement `json:"credentials,omitempty"`

	// Egress lists the upstream destinations the agent will open
	// connections to. A leading "*." on Host permits any subdomain.
	// +optional
	Egress []EgressRequirement `json:"egress,omitempty"`
}

// CredentialRequirement names one credential a template needs at runtime.
type CredentialRequirement struct {
	// Name is the env-var key the agent reads. The broker-issued value
	// is exposed under this name inside the agent container.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// EgressRequirement names one upstream destination a template needs.
type EgressRequirement struct {
	// Host is a destination hostname. A leading "*." permits any
	// subdomain (e.g. "*.anthropic.com"). Case-insensitive.
	// +kubebuilder:validation:Required
	Host string `json:"host"`

	// Ports lists the TCP ports this destination uses. Empty or [0]
	// means any port; otherwise the run's proxy will only permit
	// connections on these ports.
	// +optional
	Ports []int32 `json:"ports,omitempty"`
}

// WorkspaceRequirement describes whether and how a run uses a Workspace.
type WorkspaceRequirement struct {
	// Required indicates the template must run against a Workspace. When
	// true and a HarnessRun omits workspaceRef, the controller provisions
	// an ephemeral Workspace (see ADR-0004).
	// +kubebuilder:default=true
	// +optional
	Required bool `json:"required,omitempty"`

	// MountPath is where the workspace PVC is mounted in the agent
	// container. Defaults to /workspace.
	// +kubebuilder:default="/workspace"
	// +optional
	MountPath string `json:"mountPath,omitempty"`
}

// LocalObjectReference references another resource by name in the same
// namespace (or cluster scope for cluster-scoped kinds).
type LocalObjectReference struct {
	// Name of the referenced object.
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// HarnessTemplateStatus reports the observed state of a HarnessTemplate.
type HarnessTemplateStatus struct {
	// ObservedGeneration is the last generation of the spec that the
	// controller has reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest observations of the template's state.
	// Known types: Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for HarnessTemplate.
const (
	HarnessTemplateConditionReady = "Ready"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ht
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Harness",type=string,JSONPath=`.spec.harness`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Base",type=string,JSONPath=`.spec.baseTemplateRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HarnessTemplate is a namespaced blueprint for running an agent harness
// as a HarnessRun. It may inherit a pod shape from a ClusterHarnessTemplate
// via baseTemplateRef; see ADR-0003.
type HarnessTemplate struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec HarnessTemplateSpec `json:"spec"`
	// +optional
	Status HarnessTemplateStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// HarnessTemplateList contains a list of HarnessTemplate.
type HarnessTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []HarnessTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HarnessTemplate{}, &HarnessTemplateList{})
}
