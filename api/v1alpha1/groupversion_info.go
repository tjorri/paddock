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

// Package v1alpha1 contains API Schema definitions for the paddock v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=paddock.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "paddock.dev", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Builder maps Go types to a Kubernetes GroupVersionKind scheme. It mirrors
// sigs.k8s.io/controller-runtime/pkg/scheme.Builder, kept locally so the api
// package depends only on k8s.io/apimachinery — the upstream type was
// deprecated in controller-runtime v0.24 for that reason.
//
// +kubebuilder:object:generate=false
type Builder struct {
	GroupVersion schema.GroupVersion
	runtime.SchemeBuilder
}

// Register adds one or more objects to the SchemeBuilder so they can be added
// to a Scheme. Register mutates b.
func (b *Builder) Register(objects ...runtime.Object) *Builder {
	b.SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(b.GroupVersion, objects...)
		metav1.AddToGroupVersion(s, b.GroupVersion)
		return nil
	})
	return b
}

// AddToScheme adds all registered types to s.
func (b *Builder) AddToScheme(s *runtime.Scheme) error {
	return b.SchemeBuilder.AddToScheme(s)
}
