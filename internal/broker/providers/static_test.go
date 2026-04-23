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

package providers

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}
	if err := paddockv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("paddock scheme: %v", err)
	}
	return s
}

func TestStaticProvider_Issue_ReadsSecret(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "anthropic-api", Namespace: "my-team",
			ResourceVersion: "42",
		},
		Data: map[string][]byte{"key": []byte("sk-test")},
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(secret).Build()

	p := &StaticProvider{Client: c}
	res, err := p.Issue(context.Background(), IssueRequest{
		RunName:        "demo",
		Namespace:      "my-team",
		CredentialName: "ANTHROPIC_API_KEY",
		Purpose:        paddockv1alpha1.CredentialPurposeLLM,
		Grant: paddockv1alpha1.CredentialGrant{
			Name: "ANTHROPIC_API_KEY",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "Static",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "anthropic-api", Key: "key"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if res.Value != "sk-test" {
		t.Fatalf("Value = %q, want sk-test", res.Value)
	}
	if res.LeaseID == "" {
		t.Fatalf("LeaseID is empty")
	}
	// Lease IDs are deterministic for unchanged secret resourceVersions.
	res2, err := p.Issue(context.Background(), IssueRequest{
		RunName: "demo", Namespace: "my-team", CredentialName: "ANTHROPIC_API_KEY",
		Grant: paddockv1alpha1.CredentialGrant{
			Name: "ANTHROPIC_API_KEY",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "Static",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "anthropic-api", Key: "key"},
			},
		},
	})
	if err != nil {
		t.Fatalf("second Issue failed: %v", err)
	}
	if res.LeaseID != res2.LeaseID {
		t.Fatalf("LeaseID non-deterministic: %q vs %q", res.LeaseID, res2.LeaseID)
	}
}

func TestStaticProvider_Issue_MissingSecret(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &StaticProvider{Client: c}
	_, err := p.Issue(context.Background(), IssueRequest{
		Namespace:      "my-team",
		CredentialName: "X",
		Grant: paddockv1alpha1.CredentialGrant{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "Static",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "nope", Key: "k"},
			},
		},
	})
	if err == nil {
		t.Fatalf("expected error when secret is missing")
	}
}

func TestStaticProvider_Issue_MissingKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Data:       map[string][]byte{"other": []byte("x")},
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(secret).Build()
	p := &StaticProvider{Client: c}
	_, err := p.Issue(context.Background(), IssueRequest{
		Namespace:      "ns",
		CredentialName: "X",
		Grant: paddockv1alpha1.CredentialGrant{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "Static",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "key"},
			},
		},
	})
	if err == nil {
		t.Fatalf("expected error when key is missing")
	}
}

func TestStaticProvider_BacksAllPurposes(t *testing.T) {
	p := &StaticProvider{}
	got := p.Purposes()
	want := []paddockv1alpha1.CredentialPurpose{
		paddockv1alpha1.CredentialPurposeGeneric,
		paddockv1alpha1.CredentialPurposeLLM,
		paddockv1alpha1.CredentialPurposeGitForge,
	}
	if len(got) != len(want) {
		t.Fatalf("Purposes len = %d, want %d", len(got), len(want))
	}
}

func TestRegistry_DuplicateName(t *testing.T) {
	a := &StaticProvider{}
	b := &StaticProvider{}
	if _, err := NewRegistry(a, b); err == nil {
		t.Fatalf("expected duplicate-name error")
	}
}

func TestRegistry_Lookup(t *testing.T) {
	p := &StaticProvider{}
	r, err := NewRegistry(p)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got, ok := r.Lookup("Static"); !ok || got != p {
		t.Fatalf("Lookup(Static) = (%v, %v), want (p, true)", got, ok)
	}
	if _, ok := r.Lookup("Nope"); ok {
		t.Fatalf("Lookup(Nope) = ok, want false")
	}
}
