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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/broker"
	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/broker/providers"
)

// stubAuth is an in-memory TokenValidator that returns a fixed identity
// for any non-empty bearer. Used to sidestep TokenReview in tests.
type stubAuth struct {
	identity broker.CallerIdentity
	err      error
}

func (s stubAuth) Authenticate(_ context.Context, bearer string) (broker.CallerIdentity, error) {
	if s.err != nil {
		return broker.CallerIdentity{}, s.err
	}
	if bearer == "" {
		return broker.CallerIdentity{}, fmt.Errorf("missing token")
	}
	return s.identity, nil
}

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1: %v", err)
	}
	if err := paddockv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("paddock: %v", err)
	}
	return s
}

// setup builds a Server wired to a fake client seeded with a run, its
// template, a BrokerPolicy granting the named credential via
// StaticProvider, and the backing Secret.
func setup(t *testing.T) (*broker.Server, client.Client) {
	t.Helper()
	const ns = "my-team"

	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo",
			Image:   "paddock-echo:v1",
			Command: []string{"/bin/echo"},
			Requires: paddockv1alpha1.RequireSpec{
				Credentials: []paddockv1alpha1.CredentialRequirement{
					{Name: "DEMO_TOKEN"},
				},
			},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: ns},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Prompt:      "hi",
		},
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-echo", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: []paddockv1alpha1.CredentialGrant{{
					Name: "DEMO_TOKEN",
					Provider: paddockv1alpha1.ProviderConfig{
						Kind:      "Static",
						SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "demo", Key: "token"},
					},
				}},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns, ResourceVersion: "1"},
		Data:       map[string][]byte{"token": []byte("super-secret")},
	}

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp, secret).
		Build()

	registry, err := providers.NewRegistry(&providers.StaticProvider{Client: c})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     &broker.AuditWriter{Client: c},
	}, c
}

func post(t *testing.T, srv *broker.Server, runName, runNS, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	srv.Register(mux)
	req := httptest.NewRequest(http.MethodPost, brokerapi.PathIssue, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set(brokerapi.HeaderRun, runName)
	if runNS != "" {
		req.Header.Set(brokerapi.HeaderNamespace, runNS)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestIssue_Success(t *testing.T) {
	srv, c := setup(t)
	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "hello", "", "token-abc", string(body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.IssueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Value != "super-secret" {
		t.Fatalf("Value = %q, want super-secret", got.Value)
	}
	if got.Provider != "Static" {
		t.Fatalf("Provider = %q, want Static", got.Provider)
	}

	// AuditEvent with kind=credential-issued should have been written.
	var aes paddockv1alpha1.AuditEventList
	if err := c.List(context.Background(), &aes); err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(aes.Items) != 1 {
		t.Fatalf("auditevents = %d, want 1", len(aes.Items))
	}
	if aes.Items[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialIssued {
		t.Fatalf("audit kind = %q, want credential-issued", aes.Items[0].Spec.Kind)
	}
}

func TestIssue_MissingAuth(t *testing.T) {
	srv, _ := setup(t)
	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "hello", "", "", string(body))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestIssue_RunNotFound(t *testing.T) {
	srv, _ := setup(t)
	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "nosuch", "", "token", string(body))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var errResp brokerapi.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != "RunNotFound" {
		t.Fatalf("code = %q, want RunNotFound", errResp.Code)
	}
}

func TestIssue_CredentialNotDeclared(t *testing.T) {
	srv, c := setup(t)
	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "OTHER"})
	rr := post(t, srv, "hello", "", "token", string(body))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var errResp brokerapi.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != "CredentialNotFound" {
		t.Fatalf("code = %q, want CredentialNotFound", errResp.Code)
	}

	// Denial still gets an AuditEvent.
	var aes paddockv1alpha1.AuditEventList
	if err := c.List(context.Background(), &aes); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(aes.Items) != 1 || aes.Items[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Fatalf("expected one credential-denied, got %+v", aes.Items)
	}
}

func TestIssue_NoMatchingPolicy(t *testing.T) {
	srv, _ := setup(t)

	// Delete the BrokerPolicy so nothing grants the credential.
	var list paddockv1alpha1.BrokerPolicyList
	if err := srv.Client.List(context.Background(), &list); err != nil {
		t.Fatalf("list bp: %v", err)
	}
	for i := range list.Items {
		if err := srv.Client.Delete(context.Background(), &list.Items[i]); err != nil {
			t.Fatalf("delete bp: %v", err)
		}
	}

	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "hello", "", "token", string(body))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var errResp brokerapi.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != "PolicyMissing" {
		t.Fatalf("code = %q, want PolicyMissing", errResp.Code)
	}
}

func TestIssue_CrossNamespaceDenied(t *testing.T) {
	srv, _ := setup(t)
	// Override auth to a caller in a different namespace, non-controller.
	srv.Auth = stubAuth{identity: broker.CallerIdentity{Namespace: "other", ServiceAccount: "default"}}

	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "hello", "my-team", "token", string(body))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestIssue_ControllerCanCrossNamespace(t *testing.T) {
	srv, _ := setup(t)
	// Controller SA is permitted to ask about runs in any namespace.
	srv.Auth = stubAuth{identity: broker.CallerIdentity{
		Namespace:      "paddock-system",
		ServiceAccount: "paddock-controller-manager",
		IsController:   true,
	}}

	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "hello", "my-team", "token", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestIssue_WrongMethodRejected(t *testing.T) {
	srv, _ := setup(t)
	mux := http.NewServeMux()
	srv.Register(mux)
	req := httptest.NewRequest(http.MethodGet, brokerapi.PathIssue, bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer t")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rr.Code)
	}
}
