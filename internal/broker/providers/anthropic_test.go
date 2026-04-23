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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func anthropicGrant() paddockv1alpha1.CredentialGrant {
	return paddockv1alpha1.CredentialGrant{
		Name: "ANTHROPIC_API_KEY",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "AnthropicAPI",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "anthropic-api", Key: "key"},
		},
	}
}

func anthropicSecret(value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "anthropic-api", Namespace: "my-team"},
		Data:       map[string][]byte{"key": []byte(value)},
	}
}

func TestAnthropicAPIProvider_IssueThenSubstitute(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(anthropicSecret("sk-real")).Build()
	now := time.Unix(1_700_000_000, 0)
	p := &AnthropicAPIProvider{Client: c, Now: func() time.Time { return now }}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName:        "demo",
		Namespace:      "my-team",
		CredentialName: "ANTHROPIC_API_KEY",
		Purpose:        paddockv1alpha1.CredentialPurposeLLM,
		Grant:          anthropicGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(res.Value, "pdk-anthropic-") {
		t.Fatalf("Value = %q, want pdk-anthropic- prefix", res.Value)
	}
	if res.ExpiresAt.Before(now) {
		t.Fatalf("ExpiresAt %v is in the past", res.ExpiresAt)
	}

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "demo", Namespace: "my-team",
		Host: "api.anthropic.com", Port: 443,
		IncomingBearer: res.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if !sub.Matched {
		t.Fatalf("Matched = false, want true")
	}
	if got := sub.SetHeaders["x-api-key"]; got != "sk-real" {
		t.Fatalf("x-api-key = %q, want sk-real", got)
	}
	foundAuthz := false
	for _, h := range sub.RemoveHeaders {
		if h == "Authorization" {
			foundAuthz = true
		}
	}
	if !foundAuthz {
		t.Fatalf("RemoveHeaders = %v, want Authorization", sub.RemoveHeaders)
	}
}

func TestAnthropicAPIProvider_SubstituteStripsBearerPrefix(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(anthropicSecret("sk-real")).Build()
	p := &AnthropicAPIProvider{Client: c}
	res, err := p.Issue(context.Background(), IssueRequest{
		Namespace: "my-team", CredentialName: "K", Grant: anthropicGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Agent sent the bearer as "Bearer pdk-anthropic-…" on Authorization.
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "my-team", Host: "api.anthropic.com", Port: 443,
		IncomingBearer: "Bearer " + res.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if !sub.Matched {
		t.Fatalf("Matched = false, want true")
	}
}

func TestAnthropicAPIProvider_SubstituteUnknownPrefix(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &AnthropicAPIProvider{Client: c}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		IncomingBearer: "Bearer sk-something-else",
	})
	if err != nil {
		t.Fatalf("expected no error on unmatched prefix, got %v", err)
	}
	if sub.Matched {
		t.Fatalf("Matched = true for foreign bearer; want false so broker tries next provider")
	}
}

func TestAnthropicAPIProvider_SubstituteUnknownBearer(t *testing.T) {
	// Prefix matches ours but the bearer isn't in the map — e.g. the broker
	// restarted and lost its in-memory leases, or an attacker guessed the
	// prefix. Provider claims Matched=true with an error so the broker
	// short-circuits rather than silently falling through.
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &AnthropicAPIProvider{Client: c}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		IncomingBearer: "pdk-anthropic-deadbeef",
	})
	if !sub.Matched {
		t.Fatalf("Matched = false; want true for our prefix")
	}
	if err == nil {
		t.Fatalf("expected error for unknown bearer")
	}
}

func TestAnthropicAPIProvider_SubstituteExpiredBearer(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(anthropicSecret("sk")).Build()
	clock := time.Unix(1_700_000_000, 0)
	p := &AnthropicAPIProvider{Client: c, Now: func() time.Time { return clock }}
	res, err := p.Issue(context.Background(), IssueRequest{
		Namespace: "my-team", CredentialName: "K", Grant: anthropicGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Jump past TTL.
	clock = clock.Add(2 * time.Hour)
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "my-team", IncomingBearer: res.Value,
	})
	if !sub.Matched {
		t.Fatalf("Matched = false, want true for expired-but-owned bearer")
	}
	if err == nil {
		t.Fatalf("expected expiry error")
	}
}

func TestAnthropicAPIProvider_IssueMissingSecret(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &AnthropicAPIProvider{Client: c}
	_, err := p.Issue(context.Background(), IssueRequest{
		Namespace: "my-team", CredentialName: "K", Grant: anthropicGrant(),
	})
	if err == nil {
		t.Fatalf("expected Issue to fail on missing Secret")
	}
}

func TestAnthropicAPIProvider_BacksOnlyLLM(t *testing.T) {
	p := &AnthropicAPIProvider{}
	purposes := p.Purposes()
	if len(purposes) != 1 || purposes[0] != paddockv1alpha1.CredentialPurposeLLM {
		t.Fatalf("Purposes = %v, want [llm]", purposes)
	}
}
