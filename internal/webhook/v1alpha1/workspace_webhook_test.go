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

	It("admits a workspace with a single-repo seed", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://example.com/foo.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits a workspace with multiple repos with unique paths", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://example.com/frontend.git", Path: "frontend"},
						{URL: "https://example.com/backend.git", Path: "backend"},
					},
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

	It("rejects seed with an empty repos list", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed:    &paddockv1alpha1.WorkspaceSeed{},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("repos"))
	})

	It("rejects a repo with empty url", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{{}},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("url"))
	})

	It("rejects multiple repos when any path is missing", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://example.com/a.git", Path: "a"},
						{URL: "https://example.com/b.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("path"))
	})

	It("rejects duplicate repo paths", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://example.com/a.git", Path: "src"},
						{URL: "https://example.com/b.git", Path: "src"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("collides"))
	})

	It("rejects duplicate repo paths that only collide after path.Clean", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://example.com/a.git", Path: "src"},
						{URL: "https://example.com/b.git", Path: "./src"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("collides"))
	})

	It("rejects an absolute repo path", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://example.com/a.git", Path: "/etc/a"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("relative"))
	})

	It("rejects a repo path with '..'", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://example.com/a.git", Path: "a/../../etc"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(".."))
	})

	It("admits a broker-backed seed repo", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{{
						URL: "https://github.com/org/repo.git",
						BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
							Name: "hr-1-broker-creds", Key: "GITHUB_TOKEN",
						},
					}},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects a repo that sets both CredentialsSecretRef and BrokerCredentialRef", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{{
						URL:                  "https://github.com/org/repo.git",
						CredentialsSecretRef: &paddockv1alpha1.LocalObjectReference{Name: "legacy"},
						BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
							Name: "hr-1-broker-creds", Key: "GITHUB_TOKEN",
						},
					}},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("credentialsSecretRef"))
	})

	It("rejects BrokerCredentialRef on an ssh URL", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{{
						URL: "git@github.com:org/repo.git",
						BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
							Name: "hr-1-broker-creds", Key: "GITHUB_TOKEN",
						},
					}},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("https"))
	})

	It("rejects BrokerCredentialRef with an empty key", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{{
						URL: "https://github.com/org/repo.git",
						BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
							Name: "hr-1-broker-creds",
						},
					}},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("key"))
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
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://example.com/foo.git"},
					},
				},
			},
		}
		newObj := oldObj.DeepCopy()
		newObj.Spec.Seed.Repos[0].URL = "https://example.com/bar.git"
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})

	It("rejects http:// seed repo URL", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "http://example.com/foo.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("https:// or ssh://"))
	})

	It("rejects git:// seed repo URL", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "git://example.com/foo.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("https:// or ssh://"))
	})

	It("rejects file:// seed repo URL", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "file:///etc/passwd"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("https:// or ssh://"))
	})

	It("admits ssh:// seed repo URL", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "ssh://git@example.com/org/repo.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits scp-style seed repo URL", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "git@example.com:org/repo.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects mixed-case https scheme (HTTPS://)", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "HTTPS://example.com/foo.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("https:// or ssh://"))
	})

	It("rejects https URL with userinfo", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://user:token@example.com/foo.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("userinfo"))
	})

	It("rejects https URL with username only (no password)", func() {
		obj := &paddockv1alpha1.Workspace{
			Spec: paddockv1alpha1.WorkspaceSpec{
				Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
				Seed: &paddockv1alpha1.WorkspaceSeed{
					Repos: []paddockv1alpha1.WorkspaceGitSource{
						{URL: "https://user@example.com/foo.git"},
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("userinfo"))
	})
})
