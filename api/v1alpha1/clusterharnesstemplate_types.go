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

// ClusterHarnessTemplateStatus reports the observed state of a
// ClusterHarnessTemplate.
type ClusterHarnessTemplateStatus struct {
	// ObservedGeneration is the last spec generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest observations of the template's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=cht
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Harness",type=string,JSONPath=`.spec.harness`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterHarnessTemplate is a cluster-scoped blueprint for running an
// agent harness. Typically published by a platform team and inherited by
// namespaced HarnessTemplates; see ADR-0003. The spec is shared with
// HarnessTemplate via HarnessTemplateSpec.
type ClusterHarnessTemplate struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec HarnessTemplateSpec `json:"spec"`
	// +optional
	Status ClusterHarnessTemplateStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterHarnessTemplateList contains a list of ClusterHarnessTemplate.
type ClusterHarnessTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ClusterHarnessTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterHarnessTemplate{}, &ClusterHarnessTemplateList{})
}
