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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/resource"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var _ = Describe("Workspace Webhook", func() {
	var validator WorkspaceCustomValidator

	BeforeEach(func() {
		validator = WorkspaceCustomValidator{}
	})

	It("admits a workspace with git seed", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Git: &paddockv1alpha1.WorkspaceGitSource{URL: "https://example.com/foo.git"},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits a workspace with no seed", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects zero storage size", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("0")},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("size"))
	})

	It("rejects seed with no source", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed:    &paddockv1alpha1.WorkspaceSeed{},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("seed"))
	})

	It("rejects seed.git with empty url", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Git: &paddockv1alpha1.WorkspaceGitSource{},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("url"))
	})

	It("rejects updates that change storage (immutable)", func() {
		oldObj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
			},
		}
		newObj := oldObj.DeepCopy()
		newObj.Spec.Storage.Size = resource.MustParse("20Gi")
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})

	It("rejects updates that change seed (immutable)", func() {
		oldObj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Git: &paddockv1alpha1.WorkspaceGitSource{URL: "https://example.com/foo.git"},
				},
			},
		}
		newObj := oldObj.DeepCopy()
		newObj.Spec.Seed.Git.URL = "https://example.com/bar.git"
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})
})
