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

package broker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
	"paddock.dev/paddock/internal/broker"
	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/broker/providers"
)

// setupAnthropic builds a broker wired to UserSuppliedSecretProvider + AnthropicAPIProvider
// with an anthropic-api Secret, a BrokerPolicy granting the LLM credential,
// and an egress grant for api.anthropic.com:443.
func setupAnthropic(t *testing.T) (*broker.Server, *providers.AnthropicAPIProvider) {
	t.Helper()
	const ns = "my-team"

	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "claude-code", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "claude-code",
			Image:   "paddock-claude-code:v0.3",
			Command: []string{"/run"},
			Requires: paddockv1alpha1.RequireSpec{
				Credentials: []paddockv1alpha1.CredentialRequirement{
					{Name: "ANTHROPIC_API_KEY"},
				},
				Egress: []paddockv1alpha1.EgressRequirement{
					{Host: "api.anthropic.com", Ports: []int32{443}},
				},
			},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "cc-1", Namespace: ns},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "claude-code"},
			Prompt:      "hi",
		},
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-cc", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"claude-code"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: []paddockv1alpha1.CredentialGrant{{
					Name: "ANTHROPIC_API_KEY",
					Provider: paddockv1alpha1.ProviderConfig{
						Kind:      "AnthropicAPI",
						SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "anthropic-api", Key: "key"},
					},
				}},
				Egress: []paddockv1alpha1.EgressGrant{{
					Host: "api.anthropic.com", Ports: []int32{443},
				}},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "anthropic-api", Namespace: ns},
		Data:       map[string][]byte{"key": []byte("sk-real-42")},
	}

	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp, secret).Build()

	ap := &providers.AnthropicAPIProvider{Client: c}
	registry, err := providers.NewRegistry(&providers.UserSuppliedSecretProvider{Client: c}, ap)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}, ap
}

func postTo(t *testing.T, srv *broker.Server, path, runName, runNS, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	srv.Register(mux)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set(brokerapi.HeaderRun, runName)
	if runNS != "" {
		req.Header.Set(brokerapi.HeaderNamespace, runNS)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestValidateEgress_Allow(t *testing.T) {
	t.Parallel()
	srv, _ := setupAnthropic(t)
	body, _ := json.Marshal(brokerapi.ValidateEgressRequest{Host: "api.anthropic.com", Port: 443})
	rr := postTo(t, srv, brokerapi.PathValidateEgress, "cc-1", "", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.ValidateEgressResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if !got.Allowed {
		t.Fatalf("Allowed = false, want true")
	}
	if got.MatchedPolicy != "allow-cc" {
		t.Fatalf("MatchedPolicy = %q, want allow-cc", got.MatchedPolicy)
	}
}

func TestValidateEgress_Deny(t *testing.T) {
	t.Parallel()
	srv, _ := setupAnthropic(t)
	body, _ := json.Marshal(brokerapi.ValidateEgressRequest{Host: "evil.com", Port: 443})
	rr := postTo(t, srv, brokerapi.PathValidateEgress, "cc-1", "", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.ValidateEgressResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Allowed {
		t.Fatalf("Allowed = true for evil.com; want false")
	}
}

func TestSubstituteAuth_Success(t *testing.T) {
	t.Parallel()
	srv, ap := setupAnthropic(t)

	// Issue a bearer first.
	issued, err := ap.Issue(context.Background(), providers.IssueRequest{
		RunName: "cc-1", Namespace: "my-team", CredentialName: "ANTHROPIC_API_KEY",
		Grant: paddockv1alpha1.CredentialGrant{
			Name: "ANTHROPIC_API_KEY",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "AnthropicAPI",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "anthropic-api", Key: "key"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Agent presents the bearer on Authorization header.
	body, _ := json.Marshal(brokerapi.SubstituteAuthRequest{
		Host: "api.anthropic.com", Port: 443,
		IncomingAuthorization: "Bearer " + issued.Value,
	})
	rr := postTo(t, srv, brokerapi.PathSubstituteAuth, "cc-1", "", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.SubstituteAuthResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.SetHeaders["x-api-key"] != "sk-real-42" {
		t.Fatalf("x-api-key = %q, want sk-real-42", got.SetHeaders["x-api-key"])
	}
	foundAuthz := false
	for _, h := range got.RemoveHeaders {
		if h == "Authorization" {
			foundAuthz = true
		}
	}
	if !foundAuthz {
		t.Fatalf("RemoveHeaders = %v, want Authorization", got.RemoveHeaders)
	}
}

func TestSubstituteAuth_UnknownBearer(t *testing.T) {
	t.Parallel()
	srv, _ := setupAnthropic(t)
	body, _ := json.Marshal(brokerapi.SubstituteAuthRequest{
		Host: "api.anthropic.com", Port: 443,
		IncomingAuthorization: "Bearer sk-something-foreign",
	})
	rr := postTo(t, srv, brokerapi.PathSubstituteAuth, "cc-1", "", string(body))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestSubstituteAuth_MissingBearer(t *testing.T) {
	t.Parallel()
	srv, _ := setupAnthropic(t)
	body, _ := json.Marshal(brokerapi.SubstituteAuthRequest{
		Host: "api.anthropic.com", Port: 443,
	})
	rr := postTo(t, srv, brokerapi.PathSubstituteAuth, "cc-1", "", string(body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}
