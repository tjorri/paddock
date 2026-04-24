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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

type fakeClientBuilder = *fake.ClientBuilder

func newFakeClientWithSecret(t *testing.T, ns, name, key, value string) fakeClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string][]byte{key: []byte(value)},
		})
}

func grantInContainer() paddockv1alpha1.CredentialGrant {
	return paddockv1alpha1.CredentialGrant{
		Name: "IN_CONTAINER",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "UserSuppliedSecret",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			DeliveryMode: &paddockv1alpha1.DeliveryMode{
				InContainer: &paddockv1alpha1.InContainerDelivery{
					Accepted: true, Reason: "Agent signs requests locally with HMAC",
				},
			},
		},
	}
}

func grantHeader(prefix string) paddockv1alpha1.CredentialGrant {
	return paddockv1alpha1.CredentialGrant{
		Name: "PROXY",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "UserSuppliedSecret",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			DeliveryMode: &paddockv1alpha1.DeliveryMode{
				ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
					Hosts:  []string{"api.example.com"},
					Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization", ValuePrefix: prefix},
				},
			},
		},
	}
}

func TestUserSuppliedSecret_InContainerReturnsSecretValue(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "real-secret-value").Build()
	p := &UserSuppliedSecretProvider{Client: c}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "IN_CONTAINER",
		Grant: grantInContainer(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if res.Value != "real-secret-value" {
		t.Fatalf("value: got %q, want real-secret-value", res.Value)
	}
	if res.LeaseID == "" {
		t.Fatal("leaseID must be set")
	}
}

func TestUserSuppliedSecret_ProxyInjectedReturnsOpaqueBearer(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "real-secret-value").Build()
	p := &UserSuppliedSecretProvider{Client: c}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "PROXY",
		Grant: grantHeader("Bearer "),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(res.Value, "pdk-usersecret-") {
		t.Fatalf("value: got %q, want prefix pdk-usersecret-", res.Value)
	}
	if res.Value == "real-secret-value" {
		t.Fatal("proxy-injected mode must not leak the real secret value")
	}
}

func TestUserSuppliedSecret_SubstituteAuth_HeaderPattern(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "real-api-key").Build()
	p := &UserSuppliedSecretProvider{Client: c, Now: func() time.Time { return time.Unix(1000, 0) }}

	issue, err := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "PROXY",
		Grant: grantHeader("Bearer "),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "api.example.com", IncomingBearer: issue.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if !sub.Matched {
		t.Fatal("expected Matched=true")
	}
	if got := sub.SetHeaders["Authorization"]; got != "Bearer real-api-key" {
		t.Fatalf("Authorization: got %q, want %q", got, "Bearer real-api-key")
	}
}

func TestUserSuppliedSecret_SubstituteAuth_QueryParamPattern(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "secret-token").Build()
	p := &UserSuppliedSecretProvider{Client: c, Now: func() time.Time { return time.Unix(1000, 0) }}

	grant := paddockv1alpha1.CredentialGrant{
		Name: "Q",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "UserSuppliedSecret",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			DeliveryMode: &paddockv1alpha1.DeliveryMode{
				ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
					Hosts:      []string{"api.example.com"},
					QueryParam: &paddockv1alpha1.QueryParamSubstitution{Name: "access_token"},
				},
			},
		},
	}
	issue, _ := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "Q", Grant: grant,
	})

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "api.example.com", IncomingBearer: issue.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if got := sub.SetQueryParam["access_token"]; got != "secret-token" {
		t.Fatalf("queryParam: got %q, want secret-token", got)
	}
}

func TestUserSuppliedSecret_SubstituteAuth_BasicAuthPattern(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "pat-value").Build()
	p := &UserSuppliedSecretProvider{Client: c, Now: func() time.Time { return time.Unix(1000, 0) }}

	grant := paddockv1alpha1.CredentialGrant{
		Name: "B",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "UserSuppliedSecret",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			DeliveryMode: &paddockv1alpha1.DeliveryMode{
				ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
					Hosts:     []string{"api.example.com"},
					BasicAuth: &paddockv1alpha1.BasicAuthSubstitution{Username: "oauth2"},
				},
			},
		},
	}
	issue, _ := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "B", Grant: grant,
	})

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "api.example.com", IncomingBearer: issue.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if sub.SetBasicAuth == nil {
		t.Fatal("SetBasicAuth must be set")
	}
	if sub.SetBasicAuth.Username != "oauth2" || sub.SetBasicAuth.Password != "pat-value" {
		t.Fatalf("basic auth: got %+v, want {oauth2, pat-value}", sub.SetBasicAuth)
	}
}

func TestUserSuppliedSecret_SubstituteAuth_HostMismatchErrors(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "v").Build()
	p := &UserSuppliedSecretProvider{Client: c, Now: func() time.Time { return time.Unix(1000, 0) }}

	issue, _ := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "PROXY",
		Grant: grantHeader("Bearer "),
	})

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "wrong.example.com", IncomingBearer: issue.Value,
	})
	if err == nil {
		t.Fatal("expected error on host mismatch")
	}
	if !sub.Matched {
		t.Fatal("Matched must be true so broker short-circuits rather than falling through")
	}
}

func TestUserSuppliedSecret_SubstituteAuth_UnknownBearerReturnsMatchedFalse(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "v").Build()
	p := &UserSuppliedSecretProvider{Client: c}

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "api.example.com", IncomingBearer: "pdk-anthropic-abc",
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if sub.Matched {
		t.Fatal("bearer with non-usersecret prefix must be Matched=false so the broker tries other providers")
	}
}
