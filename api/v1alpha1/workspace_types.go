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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspaceSpec is the desired state of a Workspace — a persistent scratch
// area backed by a PVC, shared across one or more HarnessRuns. A Workspace
// outlives the runs that reference it: runs terminate, workspaces persist.
type WorkspaceSpec struct {
	// Storage configures the backing PVC.
	// +kubebuilder:validation:Required
	Storage WorkspaceStorage `json:"storage"`

	// Seed describes how the workspace is initialised. When set, the
	// Workspace controller spawns a seed Job that populates the PVC
	// before any run may start.
	// +optional
	Seed *WorkspaceSeed `json:"seed,omitempty"`

	// Ephemeral marks a workspace auto-provisioned by a HarnessRun when
	// no workspaceRef was supplied. Ephemeral workspaces carry an
	// ownerReference to their HarnessRun and cascade-delete with it.
	// See ADR-0004.
	// +optional
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// WorkspaceStorage configures the backing PVC.
type WorkspaceStorage struct {
	// Size is the requested storage capacity (e.g. "20Gi").
	// +kubebuilder:validation:Required
	Size resource.Quantity `json:"size"`

	// StorageClass names the StorageClass for the PVC. When empty, the
	// cluster's default StorageClass is used.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// AccessMode is the PVC access mode. Defaults to ReadWriteOnce.
	// ReadWriteMany is supported but requires a networked filesystem
	// StorageClass and is not exercised on Kind.
	// +kubebuilder:default=ReadWriteOnce
	// +kubebuilder:validation:Enum=ReadWriteOnce;ReadWriteMany
	// +optional
	AccessMode corev1.PersistentVolumeAccessMode `json:"accessMode,omitempty"`
}

// WorkspaceSeed describes how a Workspace is initialised before any run.
// Exactly one seed source must be set (currently only Git; FromArchive
// lands in v0.2).
type WorkspaceSeed struct {
	// Git clones a git repository into the workspace at /workspace.
	// +optional
	Git *WorkspaceGitSource `json:"git,omitempty"`
}

// WorkspaceGitSource clones a git repository into the workspace.
type WorkspaceGitSource struct {
	// URL is the clone URL (https or ssh).
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Branch to clone. Defaults to the remote's HEAD.
	// +optional
	Branch string `json:"branch,omitempty"`

	// Depth is the shallow-clone depth. Zero or unset means a full clone.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Depth int32 `json:"depth,omitempty"`

	// CredentialsSecretRef, when set, supplies git credentials. The Secret
	// may contain either username/password or ssh-privatekey. Mounted
	// read-only into the seed Job only. See ADR-0006.
	// +optional
	CredentialsSecretRef *LocalObjectReference `json:"credentialsSecretRef,omitempty"`
}

// WorkspacePhase is the lifecycle phase of a Workspace.
// +kubebuilder:validation:Enum=Seeding;Active;Failed;Terminating
type WorkspacePhase string

const (
	WorkspacePhaseSeeding     WorkspacePhase = "Seeding"
	WorkspacePhaseActive      WorkspacePhase = "Active"
	WorkspacePhaseFailed      WorkspacePhase = "Failed"
	WorkspacePhaseTerminating WorkspacePhase = "Terminating"
)

// Condition types reported on Workspace.status.conditions.
const (
	WorkspaceConditionPVCBound = "PVCBound"
	WorkspaceConditionSeeded   = "Seeded"
	WorkspaceConditionReady    = "Ready"
)

// WorkspaceStatus reports the observed state of a Workspace.
type WorkspaceStatus struct {
	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase summarises the workspace's lifecycle.
	// +optional
	Phase WorkspacePhase `json:"phase,omitempty"`

	// PVCName is the backing PersistentVolumeClaim, once created.
	// +optional
	PVCName string `json:"pvcName,omitempty"`

	// SeedJobName is the backing seed Job, while it exists.
	// +optional
	SeedJobName string `json:"seedJobName,omitempty"`

	// ActiveRunRef names the HarnessRun currently using the workspace.
	// Empty when the workspace is idle. Used to serialise concurrent
	// runs against a ReadWriteOnce PVC without reliance on PVC attach
	// errors.
	// +optional
	ActiveRunRef string `json:"activeRunRef,omitempty"`

	// TotalRuns is a monotonic count of HarnessRuns that have bound to
	// this workspace. Informational only.
	// +optional
	TotalRuns int32 `json:"totalRuns,omitempty"`

	// LastActivity is the time of the most recent run's last event. Used
	// by future archive-on-idle logic.
	// +optional
	LastActivity *metav1.Time `json:"lastActivity,omitempty"`

	// Conditions report typed lifecycle signals. Known types: PVCBound,
	// Seeded, Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ws
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Active-Run",type=string,JSONPath=`.status.activeRunRef`
// +kubebuilder:printcolumn:name="Runs",type=integer,JSONPath=`.status.totalRuns`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Workspace is a persistent scratch area shared across HarnessRuns. The
// workspace outlives the runs that use it; at most one run mounts it at
// a time (ReadWriteOnce default).
type Workspace struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec WorkspaceSpec `json:"spec"`
	// +optional
	Status WorkspaceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace.
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Workspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workspace{}, &WorkspaceList{})
}
