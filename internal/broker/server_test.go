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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
	"paddock.dev/paddock/internal/broker"
	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/broker/providers"
)

// substituteAuthAnthropicBody is the canonical SubstituteAuth request body
// shared across substitute-auth tests that target api.anthropic.com:443.
const substituteAuthAnthropicBody = `{"host":"api.anthropic.com","port":443,"incomingXApiKey":"paddock-bearer-x"}`

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
// UserSuppliedSecretProvider (InContainer delivery), and the backing Secret.
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
						Kind:      "UserSuppliedSecret",
						SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "demo", Key: "token"},
						DeliveryMode: &paddockv1alpha1.DeliveryMode{
							InContainer: &paddockv1alpha1.InContainerDelivery{
								Accepted: true,
								Reason:   "test fixture: direct secret delivery",
							},
						},
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

	registry, err := providers.NewRegistry(&providers.UserSuppliedSecretProvider{Client: c})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}, c
}

func post(t *testing.T, srv *broker.Server, runName, runNS, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	srv.Register(mux)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathIssue, strings.NewReader(body))
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
	t.Parallel()
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
	if got.Provider != "UserSuppliedSecret" {
		t.Fatalf("Provider = %q, want UserSuppliedSecret", got.Provider)
	}
	// The setup fixture uses UserSuppliedSecret + InContainer delivery.
	if got.DeliveryMode != "InContainer" {
		t.Fatalf("DeliveryMode = %q, want InContainer", got.DeliveryMode)
	}
	if len(got.Hosts) != 0 {
		t.Fatalf("Hosts = %v, want empty for InContainer", got.Hosts)
	}
	if got.InContainerReason != "test fixture: direct secret delivery" {
		t.Fatalf("InContainerReason = %q", got.InContainerReason)
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

// errorSink wraps a real audit writer but injects an error on demand,
// for testing the broker's fail-closed behaviour on audit-write failure.
type errorSink struct {
	err error
}

func (s errorSink) Write(_ context.Context, _ *paddockv1alpha1.AuditEvent) error { return s.err }

// recordingAuditSink captures every AuditEvent written to it so tests can
// assert that the broker emitted the expected audit records.
type recordingAuditSink struct {
	mu  sync.Mutex
	all []*paddockv1alpha1.AuditEvent
	err error
}

func (r *recordingAuditSink) Write(_ context.Context, ae *paddockv1alpha1.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, ae.DeepCopy())
	return r.err
}

func (r *recordingAuditSink) events() []*paddockv1alpha1.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*paddockv1alpha1.AuditEvent, len(r.all))
	copy(out, r.all)
	return out
}

func TestIssue_AuditFailure_Returns503AndNoCredential(t *testing.T) {
	t.Parallel()
	srv, _ := setup(t)
	srv.Audit = broker.NewAuditWriter(errorSink{err: errors.New("etcd partition")})

	rr := post(t, srv, "hello", "my-team", "valid-token", `{"name":"DEMO_TOKEN"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "super-secret") {
		t.Errorf("response body leaked credential: %q", rr.Body.String())
	}
}

func TestIssue_DenyAuditFailure_Returns503(t *testing.T) {
	t.Parallel()
	srv, _ := setup(t)
	srv.Audit = broker.NewAuditWriter(errorSink{err: errors.New("etcd partition")})

	// Ask for a credential the template does not declare → CredentialNotFound.
	rr := post(t, srv, "hello", "my-team", "valid-token", `{"name":"NO_SUCH_CRED"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (audit-write failed during deny)", rr.Code)
	}
}

// TestIssue_Success_ProxyInjectedHosts swaps the setup fixture's grant
// for a UserSuppliedSecret+ProxyInjected grant and asserts the response
// carries the ProxyInjected delivery mode plus the grant's host list.
func TestIssue_Success_ProxyInjectedHosts(t *testing.T) {
	t.Parallel()
	srv, c := setup(t)

	// Replace the in-cluster BrokerPolicy's credential grant with a
	// ProxyInjected delivery over a specific host list.
	var bp paddockv1alpha1.BrokerPolicy
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: "allow-echo", Namespace: "my-team"}, &bp); err != nil {
		t.Fatalf("get bp: %v", err)
	}
	bp.Spec.Grants.Credentials[0].Provider.DeliveryMode = &paddockv1alpha1.DeliveryMode{
		ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
			Hosts:  []string{"api.example.com"},
			Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization", ValuePrefix: "Bearer "},
		},
	}
	if err := c.Update(context.Background(), &bp); err != nil {
		t.Fatalf("update bp: %v", err)
	}

	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "hello", "", "token-abc", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.IssueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DeliveryMode != "ProxyInjected" {
		t.Fatalf("DeliveryMode = %q, want ProxyInjected", got.DeliveryMode)
	}
	if len(got.Hosts) != 1 || got.Hosts[0] != "api.example.com" {
		t.Fatalf("Hosts = %v, want [api.example.com]", got.Hosts)
	}
	if got.InContainerReason != "" {
		t.Fatalf("InContainerReason = %q, want empty", got.InContainerReason)
	}
}

// TestIssue_Success_AnthropicAPI asserts the response carries the
// built-in AnthropicAPI defaults (ProxyInjected + [api.anthropic.com])
// when the grant doesn't override Hosts.
func TestIssue_Success_AnthropicAPI(t *testing.T) {
	t.Parallel()
	const ns = "my-team"

	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo",
			Image:   "paddock-echo:v1",
			Command: []string{"/bin/echo"},
			Requires: paddockv1alpha1.RequireSpec{
				Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "ANTHROPIC"}},
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
		ObjectMeta: metav1.ObjectMeta{Name: "allow-anthropic", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: []paddockv1alpha1.CredentialGrant{{
					Name: "ANTHROPIC",
					Provider: paddockv1alpha1.ProviderConfig{
						Kind:      "AnthropicAPI",
						SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "anthropic", Key: "apiKey"},
					},
				}},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "anthropic", Namespace: ns, ResourceVersion: "1"},
		Data:       map[string][]byte{"apiKey": []byte("sk-real-key")},
	}

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp, secret).
		Build()
	registry, err := providers.NewRegistry(&providers.AnthropicAPIProvider{Client: c})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}

	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "ANTHROPIC"})
	rr := post(t, srv, "hello", "", "token-abc", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.IssueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Provider != "AnthropicAPI" {
		t.Fatalf("Provider = %q, want AnthropicAPI", got.Provider)
	}
	if got.DeliveryMode != "ProxyInjected" {
		t.Fatalf("DeliveryMode = %q, want ProxyInjected", got.DeliveryMode)
	}
	if len(got.Hosts) != 1 || got.Hosts[0] != "api.anthropic.com" {
		t.Fatalf("Hosts = %v, want [api.anthropic.com]", got.Hosts)
	}
	if got.InContainerReason != "" {
		t.Fatalf("InContainerReason = %q, want empty", got.InContainerReason)
	}
}

func TestIssue_MissingAuth(t *testing.T) {
	t.Parallel()
	srv, _ := setup(t)
	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "hello", "", "", string(body))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestIssue_RunNotFound(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	srv, _ := setup(t)
	// Override auth to a caller in a different namespace, non-controller.
	srv.Auth = stubAuth{identity: broker.CallerIdentity{Namespace: "other", ServiceAccount: "default"}}

	// handleIssue now shares resolveRunIdentity with the other two
	// handlers; a non-controller caller asking about another namespace's
	// run gets 400 BadRequest (the consistent shape), not the older 403
	// Forbidden the inlined code returned. B-04.
	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "DEMO_TOKEN"})
	rr := post(t, srv, "hello", "my-team", "token", string(body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestIssue_ControllerCanCrossNamespace(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	srv, _ := setup(t)
	mux := http.NewServeMux()
	srv.Register(mux)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, brokerapi.PathIssue, bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer t")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rr.Code)
	}
}

// postValidateEgress is a helper that sends a POST to /v1/validate-egress
// on the given server with the supplied run identity and body.
func postValidateEgress(t *testing.T, srv *broker.Server, runName, runNS, bearer string, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	srv.Register(mux)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathValidateEgress, strings.NewReader(body))
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

// TestValidateEgress_DiscoveryAllow asserts that when no egress grant
// covers the destination but a matching BrokerPolicy has an active
// egressDiscovery window, the broker returns Allowed=true with
// DiscoveryAllow=true so the proxy can emit an egress-discovery-allow
// AuditEvent instead of denying.
func TestValidateEgress_DiscoveryAllow(t *testing.T) {
	t.Parallel()
	const ns = "my-team"

	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo",
			Image:   "paddock-echo:v1",
			Command: []string{"/bin/echo"},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: ns},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Prompt:      "hi",
		},
	}
	// BrokerPolicy with no egress grants but an active discovery window.
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "discovery-policy", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
				Accepted:  true,
				Reason:    "testing discovery allow path in broker handleValidateEgress",
				ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)),
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp).
		Build()

	registry, err := providers.NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}

	body, _ := json.Marshal(brokerapi.ValidateEgressRequest{Host: "example.com", Port: 443})
	rr := postValidateEgress(t, srv, "hello", "", "token-abc", string(body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.ValidateEgressResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Allowed {
		t.Errorf("Allowed = false, want true (discovery window is active)")
	}
	if !got.DiscoveryAllow {
		t.Errorf("DiscoveryAllow = false, want true")
	}
	if got.Reason == "" {
		t.Errorf("Reason is empty, want non-empty discovery explanation")
	}
}

func TestIssue_GetRunInfraError_EmitsAudit(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*paddockv1alpha1.HarnessRun); ok {
					return errors.New("apiserver unreachable")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	registry, err := providers.NewRegistry(&providers.UserSuppliedSecretProvider{Client: c})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	rec := &recordingAuditSink{}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: "team-a", ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(rec),
	}
	rr := post(t, srv, "hr-1", "team-a", "valid-token", `{"name":"DEMO_TOKEN"}`)
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 500/503", rr.Code)
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(wrote), wrote)
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
	if !strings.Contains(wrote[0].Spec.Reason, "loading run") {
		t.Errorf("reason = %q, want contains 'loading run'", wrote[0].Spec.Reason)
	}
}

func TestIssue_ResolveTemplateInfraError_EmitsAudit(t *testing.T) {
	t.Parallel()
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Prompt:      "hi",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(run).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				// Let HarnessRun Get succeed (use real fake client).
				if _, ok := obj.(*paddockv1alpha1.HarnessRun); ok {
					return c.Get(ctx, key, obj, opts...)
				}
				// Fail for templates.
				if _, ok := obj.(*paddockv1alpha1.HarnessTemplate); ok {
					return errors.New("apiserver unreachable: template")
				}
				if _, ok := obj.(*paddockv1alpha1.ClusterHarnessTemplate); ok {
					return errors.New("apiserver unreachable: cluster template")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	registry, err := providers.NewRegistry(&providers.UserSuppliedSecretProvider{Client: c})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	rec := &recordingAuditSink{}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: "team-a", ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(rec),
	}
	rr := post(t, srv, "hr-1", "team-a", "valid-token", `{"name":"DEMO_TOKEN"}`)
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 500/503", rr.Code)
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(wrote), wrote)
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
	if !strings.Contains(wrote[0].Spec.Reason, "resolving template") {
		t.Errorf("reason = %q, want contains 'resolving template'", wrote[0].Spec.Reason)
	}
}

// stubSubstituter implements both providers.Provider and providers.Substituter
// for testing handleSubstituteAuth audit emission.
type stubSubstituter struct {
	name           string
	matched        bool
	subErr         error
	setHdrs        map[string]string
	removeHdr      []string
	credentialName string
}

func (s *stubSubstituter) Name() string { return s.name }

func (s *stubSubstituter) Issue(_ context.Context, _ providers.IssueRequest) (providers.IssueResult, error) {
	return providers.IssueResult{}, errors.New("stub does not implement Issue")
}

func (s *stubSubstituter) Revoke(_ context.Context, _ string) error { return nil }

func (s *stubSubstituter) SubstituteAuth(_ context.Context, _ providers.SubstituteRequest) (brokerapi.SubstituteResult, error) {
	if !s.matched {
		return brokerapi.SubstituteResult{Matched: false}, nil
	}
	return brokerapi.SubstituteResult{
		Matched:        true,
		SetHeaders:     s.setHdrs,
		RemoveHeaders:  s.removeHdr,
		CredentialName: s.credentialName,
	}, s.subErr
}

// setupSubstituteAuth builds a Server whose Providers registry has the
// supplied substituter. Mirrors setup() but substitutes the provider list.
func setupSubstituteAuth(t *testing.T, sub *stubSubstituter) (*broker.Server, client.Client) {
	t.Helper()
	ns := "team-a"
	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns, ResourceVersion: "1"},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: ns, ResourceVersion: "1"},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
		},
	}
	// Phase 2g: handler re-validates matchPolicyGrant + matchEgressGrant
	// after the provider claims a bearer. Pre-stage a BrokerPolicy granting
	// STUB_CRED via the stub provider's kind, with an egress grant for the
	// host these tests substitute against (api.anthropic.com:443). Tests
	// exercising the revoked / mismatched paths use a different
	// credentialName / host to fail the re-check deliberately.
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "stub-allow", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: []paddockv1alpha1.CredentialGrant{{
					Name:     "STUB_CRED",
					Provider: paddockv1alpha1.ProviderConfig{Kind: sub.name},
				}},
				Egress: []paddockv1alpha1.EgressGrant{{
					Host: "api.anthropic.com", Ports: []int32{443},
				}},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp).
		WithStatusSubresource(run).
		Build()
	registry, err := providers.NewRegistry(sub)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}, c
}

func TestSubstituteAuth_GrantedEmitsCredentialIssuedAudit(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{
		name:           "anthropic-stub",
		matched:        true,
		setHdrs:        map[string]string{"x-api-key": "real-key"},
		credentialName: "STUB_CRED",
	}
	srv, _ := setupSubstituteAuth(t, sub)
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	mux := http.NewServeMux()
	srv.Register(mux)
	body := substituteAuthAnthropicBody
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathSubstituteAuth, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set(brokerapi.HeaderRun, "hr-1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialIssued {
		t.Errorf("kind = %q, want credential-issued", wrote[0].Spec.Kind)
	}
}

func TestSubstituteAuth_SubstituteFailedEmitsCredentialDeniedAudit(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{
		name:           "anthropic-stub",
		matched:        true,
		subErr:         errors.New("token expired"),
		credentialName: "STUB_CRED",
	}
	srv, _ := setupSubstituteAuth(t, sub)
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	mux := http.NewServeMux()
	srv.Register(mux)
	body := substituteAuthAnthropicBody
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathSubstituteAuth, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set(brokerapi.HeaderRun, "hr-1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
	if !strings.Contains(wrote[0].Spec.Reason, "token expired") {
		t.Errorf("reason = %q, want contains 'token expired'", wrote[0].Spec.Reason)
	}
}

func TestSubstituteAuth_BearerUnknownEmitsCredentialDeniedAudit(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{name: "anthropic-stub", matched: false}
	srv, _ := setupSubstituteAuth(t, sub)
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	mux := http.NewServeMux()
	srv.Register(mux)
	body := `{"host":"api.anthropic.com","port":443,"incomingXApiKey":"unknown-bearer"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathSubstituteAuth, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set(brokerapi.HeaderRun, "hr-1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
}

func TestSubstituteAuth_RunNotFound_DeniesAndAudits(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{name: "anthropic-stub", matched: true,
		setHdrs: map[string]string{"x-api-key": "real-key"}}
	srv, _ := setupSubstituteAuth(t, sub)
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	mux := http.NewServeMux()
	srv.Register(mux)
	body := substituteAuthAnthropicBody
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathSubstituteAuth, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set(brokerapi.HeaderRun, "no-such-run")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (RunTerminated/no-such-run); body=%s", rr.Code, rr.Body.String())
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
	if !strings.Contains(wrote[0].Spec.Reason, "run not found") {
		t.Errorf("reason = %q, want contains 'run not found'", wrote[0].Spec.Reason)
	}
}

func TestSubstituteAuth_RunCancelled_DeniesAndAudits(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{name: "anthropic-stub", matched: true,
		setHdrs: map[string]string{"x-api-key": "real-key"}}
	srv, c := setupSubstituteAuth(t, sub)
	// Cancel the run that setupSubstituteAuth created.
	var run paddockv1alpha1.HarnessRun
	if err := c.Get(context.Background(), types.NamespacedName{Name: "hr-1", Namespace: "team-a"}, &run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	run.Status.Phase = paddockv1alpha1.HarnessRunPhaseCancelled
	if err := c.Status().Update(context.Background(), &run); err != nil {
		t.Fatalf("update run status: %v", err)
	}
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	mux := http.NewServeMux()
	srv.Register(mux)
	body := substituteAuthAnthropicBody
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathSubstituteAuth, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set(brokerapi.HeaderRun, "hr-1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (RunTerminated); body=%s", rr.Code, rr.Body.String())
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
	if !strings.Contains(strings.ToLower(wrote[0].Spec.Reason), "cancelled") {
		t.Errorf("reason = %q, want contains 'cancelled'", wrote[0].Spec.Reason)
	}
}

func TestIssue_ListBrokerPoliciesInfraError_EmitsAudit(t *testing.T) {
	t.Parallel()
	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: "team-a"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Prompt:      "hi",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*paddockv1alpha1.BrokerPolicyList); ok {
					return errors.New("apiserver unreachable: list bp")
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()
	registry, err := providers.NewRegistry(&providers.UserSuppliedSecretProvider{Client: c})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	rec := &recordingAuditSink{}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: "team-a", ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(rec),
	}
	rr := post(t, srv, "hr-1", "team-a", "valid-token", `{"name":"DEMO_TOKEN"}`)
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 500/503", rr.Code)
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(wrote), wrote)
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
	if !strings.Contains(wrote[0].Spec.Reason, "listing BrokerPolicies") {
		t.Errorf("reason = %q, want contains 'listing BrokerPolicies'", wrote[0].Spec.Reason)
	}
}

func TestSubstituteAuth_PolicyRevoked_DeniesAndAudits(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{
		name:           "anthropic-stub",
		matched:        true,
		credentialName: "ANTHROPIC_API_KEY",
		setHdrs:        map[string]string{"x-api-key": "real-key"},
	}
	srv, c := setupSubstituteAuth(t, sub)
	// setupSubstituteAuth creates a BrokerPolicy granting STUB_CRED. Override
	// the test stub's credentialName to ANTHROPIC_API_KEY (no matching grant
	// in the fixture) so the re-check fails with PolicyRevoked.
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	mux := http.NewServeMux()
	srv.Register(mux)
	body := substituteAuthAnthropicBody
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathSubstituteAuth, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set(brokerapi.HeaderRun, "hr-1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (PolicyRevoked); body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "PolicyRevoked") {
		t.Errorf("response body = %s, want contains PolicyRevoked", rr.Body.String())
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
	_ = c // unused; kept to mirror other tests' signature shape
}

func TestSubstituteAuth_EgressRevoked_DeniesAndAudits(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{
		name:           "anthropic-stub",
		matched:        true,
		credentialName: "STUB_CRED",
		setHdrs:        map[string]string{"x-api-key": "real-key"},
	}
	srv, c := setupSubstituteAuth(t, sub)
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	mux := http.NewServeMux()
	srv.Register(mux)
	// Use a host that is NOT in the policy's egress grants (api.anthropic.com is granted; evil.com is not).
	body := `{"host":"evil.com","port":443,"incomingXApiKey":"paddock-bearer-x"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathSubstituteAuth, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set(brokerapi.HeaderRun, "hr-1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (EgressRevoked); body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "EgressRevoked") {
		t.Errorf("response body = %s, want contains EgressRevoked", rr.Body.String())
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d events, want 1", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", wrote[0].Spec.Kind)
	}
	_ = c
}

// recordingProvider is a minimal Provider stub that records Revoke calls
// so TestHandleRevoke_* tests can assert that the handler dispatched to
// the correct provider.
type recordingProvider struct {
	name        string
	revokeCalls int
	lastLeaseID string
	revokeErr   error
}

func (r *recordingProvider) Name() string { return r.name }

func (r *recordingProvider) Issue(_ context.Context, _ providers.IssueRequest) (providers.IssueResult, error) {
	return providers.IssueResult{}, fmt.Errorf("recordingProvider does not implement Issue")
}

func (r *recordingProvider) Revoke(_ context.Context, leaseID string) error {
	r.revokeCalls++
	r.lastLeaseID = leaseID
	return r.revokeErr
}

// setupRevoke builds a minimal Server wired to the given recordingProvider.
// The caller identity defaults to the controller-manager SA (IsController=true)
// unless overridden by the test after this returns.
func setupRevoke(t *testing.T, prov *recordingProvider) *broker.Server {
	t.Helper()
	registry, err := providers.NewRegistry(prov)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	return &broker.Server{
		Client: c,
		Auth: stubAuth{identity: broker.CallerIdentity{
			Namespace:      broker.ControllerSystemNamespace,
			ServiceAccount: broker.ControllerServiceAccount,
			IsController:   true,
		}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}
}

// postRevoke sends a POST /v1/revoke request to the server and returns the recorder.
func postRevoke(t *testing.T, srv *broker.Server, runName, runNS, bearer string, body brokerapi.RevokeRequest) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	srv.Register(mux)
	b, _ := json.Marshal(body)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathRevoke, bytes.NewReader(b))
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

func TestHandleRevoke_Success_EmitsCredentialRevoked(t *testing.T) {
	t.Parallel()
	prov := &recordingProvider{name: "stub-prov"}
	srv := setupRevoke(t, prov)
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	rr := postRevoke(t, srv, "hr-1", "team-a", "ctrl-token", brokerapi.RevokeRequest{
		Provider:       "stub-prov",
		LeaseID:        "lease-x",
		CredentialName: "CRED",
	})

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
	if prov.revokeCalls != 1 {
		t.Errorf("Revoke called %d times, want 1", prov.revokeCalls)
	}
	if prov.lastLeaseID != "lease-x" {
		t.Errorf("lastLeaseID = %q, want lease-x", prov.lastLeaseID)
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("got %d audit events, want 1", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialRevoked {
		t.Errorf("audit kind = %q, want credential-revoked", wrote[0].Spec.Kind)
	}
}

func TestHandleRevoke_NonControllerCaller_Forbidden(t *testing.T) {
	t.Parallel()
	prov := &recordingProvider{name: "stub-prov"}
	srv := setupRevoke(t, prov)
	// Override auth to return a non-controller caller.
	srv.Auth = stubAuth{identity: broker.CallerIdentity{
		Namespace:      "team-a",
		ServiceAccount: "default",
		IsController:   false,
	}}
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	rr := postRevoke(t, srv, "hr-1", "team-a", "run-token", brokerapi.RevokeRequest{
		Provider:       "stub-prov",
		LeaseID:        "lease-x",
		CredentialName: "CRED",
	})

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if prov.revokeCalls != 0 {
		t.Errorf("Revoke called %d times, want 0", prov.revokeCalls)
	}
	if len(rec.events()) != 0 {
		t.Errorf("got %d audit events, want 0", len(rec.events()))
	}
}

// Covers the success-path 503 (revoke succeeded, audit failed).
// The failure-path 503 is covered by TestHandleRevoke_RevokeFailsAndAuditFails_Returns503.
func TestHandleRevoke_AuditWriteFailure_Returns503(t *testing.T) {
	t.Parallel()
	prov := &recordingProvider{name: "stub-prov"}
	srv := setupRevoke(t, prov)
	srv.Audit = broker.NewAuditWriter(errorSink{err: errors.New("etcd unavailable")})

	rr := postRevoke(t, srv, "hr-1", "team-a", "ctrl-token", brokerapi.RevokeRequest{
		Provider:       "stub-prov",
		LeaseID:        "lease-x",
		CredentialName: "CRED",
	})

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
	var errResp brokerapi.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Code != brokerapi.CodeAuditUnavailable {
		t.Errorf("code = %q, want AuditUnavailable", errResp.Code)
	}
}

func TestHandleRevoke_RevokeFails_Returns500ProviderFailure(t *testing.T) {
	t.Parallel()
	prov := &recordingProvider{name: "stub-prov", revokeErr: errors.New("provider boom")}
	srv := setupRevoke(t, prov)
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)

	rr := postRevoke(t, srv, "hr-1", "team-a", "ctrl-token", brokerapi.RevokeRequest{
		Provider:       "stub-prov",
		LeaseID:        "lease-x",
		CredentialName: "cred",
	})

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s; want 500", rr.Code, rr.Body.String())
	}
	if prov.revokeCalls != 1 {
		t.Fatalf("provider Revoke calls = %d; want 1", prov.revokeCalls)
	}
	events := rec.events()
	if len(events) != 1 {
		t.Fatalf("expected exactly one audit event, got %d", len(events))
	}
	if events[0].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Fatalf("audit kind = %s; want credential-denied (denial-shape on revoke failure)", events[0].Spec.Kind)
	}
}

func TestHandleRevoke_RevokeFailsAndAuditFails_Returns503(t *testing.T) {
	t.Parallel()
	prov := &recordingProvider{name: "stub-prov", revokeErr: errors.New("provider boom")}
	srv := setupRevoke(t, prov)
	srv.Audit = broker.NewAuditWriter(errorSink{err: errors.New("audit boom")})

	rr := postRevoke(t, srv, "hr-1", "team-a", "ctrl-token", brokerapi.RevokeRequest{
		Provider:       "stub-prov",
		LeaseID:        "lease-x",
		CredentialName: "cred",
	})

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s; want 503 (failure-path Phase 2c contract)", rr.Code, rr.Body.String())
	}
	// Body should carry CodeAuditUnavailable, not CodeProviderFailure.
	if !strings.Contains(rr.Body.String(), brokerapi.CodeAuditUnavailable) {
		t.Fatalf("body missing AuditUnavailable code: %s", rr.Body.String())
	}
}

func TestAuditWriter_NewAuditWriter_PanicsOnNilSink(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("NewAuditWriter(nil) did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %T, want string", r)
		}
		if !strings.Contains(msg, "sink must not be nil") {
			t.Errorf("panic msg = %q, want contains 'sink must not be nil'", msg)
		}
	}()
	_ = broker.NewAuditWriter(nil)
}

func TestAuditWriter_ZeroValue_PanicsOnFirstSink(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("(&AuditWriter{}).sink() via Write did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %T, want string", r)
		}
		if !strings.Contains(msg, "NewAuditWriter") {
			t.Errorf("panic msg = %q, want contains 'NewAuditWriter'", msg)
		}
	}()
	w := &broker.AuditWriter{}
	_ = w.CredentialIssued(context.Background(), broker.CredentialAudit{})
}

// TestSubstituteAuth_Core_TableDriven exercises Server.substituteAuth
// directly (via the SubstituteAuthForTest shim in internal_test.go),
// so the F-10/F-21/F-25 re-validation paths are unit-tested without
// going through the HTTP shell. The HTTP-shell behaviour is covered
// by the existing TestSubstituteAuth_*_DeniesAndAudits tests.
//
// Added in B-01 part 2.
func TestSubstituteAuth_Core_TableDriven(t *testing.T) {
	const (
		ns      = "team-a"
		runName = "hr-1"
	)
	type want struct {
		isAllow      bool
		appErrCode   string
		appErrStatus int
		auditReason  string // substring match
	}
	// happyPathServer returns a server where substituteAuth succeeds:
	// stub provider matches, BrokerPolicy grants STUB_CRED, egress grant
	// covers api.anthropic.com:443.
	happyPathServer := func(t *testing.T) *broker.Server {
		t.Helper()
		sub := &stubSubstituter{
			name:           "anthropic-stub",
			matched:        true,
			credentialName: "STUB_CRED",
			setHdrs:        map[string]string{"x-api-key": "real-key"},
		}
		srv, _ := setupSubstituteAuth(t, sub)
		return srv
	}
	cases := []struct {
		name    string
		setup   func(t *testing.T) *broker.Server
		runName string // override for RunNotFound
		req     brokerapi.SubstituteAuthRequest
		want    want
	}{
		{
			name:    "AllowPath",
			setup:   happyPathServer,
			runName: runName,
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "paddock-bearer-x",
			},
			want: want{isAllow: true, auditReason: "substituted upstream credential"},
		},
		{
			name:    "RunNotFound",
			setup:   happyPathServer,
			runName: "no-such-run", // the run hr-1 exists; ask for a different one
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "paddock-bearer-x",
			},
			want: want{
				appErrCode: "RunTerminated", appErrStatus: http.StatusNotFound,
				auditReason: "run not found",
			},
		},
		{
			name: "RunCancelled",
			setup: func(t *testing.T) *broker.Server {
				t.Helper()
				sub := &stubSubstituter{
					name:           "anthropic-stub",
					matched:        true,
					credentialName: "STUB_CRED",
					setHdrs:        map[string]string{"x-api-key": "real-key"},
				}
				srv, c := setupSubstituteAuth(t, sub)
				var run paddockv1alpha1.HarnessRun
				if err := c.Get(context.Background(),
					types.NamespacedName{Name: runName, Namespace: ns}, &run); err != nil {
					t.Fatalf("get run: %v", err)
				}
				run.Status.Phase = paddockv1alpha1.HarnessRunPhaseCancelled
				if err := c.Status().Update(context.Background(), &run); err != nil {
					t.Fatalf("update run status: %v", err)
				}
				return srv
			},
			runName: runName,
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "paddock-bearer-x",
			},
			want: want{
				appErrCode: "RunTerminated", appErrStatus: http.StatusForbidden,
				auditReason: "run terminated",
			},
		},
		{
			name: "BearerUnknown",
			setup: func(t *testing.T) *broker.Server {
				t.Helper()
				sub := &stubSubstituter{name: "anthropic-stub", matched: false}
				srv, _ := setupSubstituteAuth(t, sub)
				return srv
			},
			runName: runName,
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "no-such-prefix-bearer",
			},
			want: want{
				appErrCode: "BearerUnknown", appErrStatus: http.StatusNotFound,
				auditReason: "no registered provider",
			},
		},
		{
			name: "PolicyRevoked",
			setup: func(t *testing.T) *broker.Server {
				t.Helper()
				// Stub claims the bearer with credentialName=ANTHROPIC_API_KEY,
				// but the BrokerPolicy in setupSubstituteAuth only grants
				// STUB_CRED — so the F-10 policy re-check fails.
				sub := &stubSubstituter{
					name:           "anthropic-stub",
					matched:        true,
					credentialName: "ANTHROPIC_API_KEY",
					setHdrs:        map[string]string{"x-api-key": "real-key"},
				}
				srv, _ := setupSubstituteAuth(t, sub)
				return srv
			},
			runName: runName,
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "paddock-bearer-x",
			},
			want: want{
				appErrCode: "PolicyRevoked", appErrStatus: http.StatusForbidden,
				auditReason: "policy revoked",
			},
		},
		{
			name:    "EgressRevoked",
			setup:   happyPathServer,
			runName: runName,
			req: brokerapi.SubstituteAuthRequest{
				// evil.com is not in the policy's egress grants.
				Host: "evil.com", Port: 443,
				IncomingXAPIKey: "paddock-bearer-x",
			},
			want: want{
				appErrCode: "EgressRevoked", appErrStatus: http.StatusForbidden,
				auditReason: "egress revoked",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.setup(t)
			resp, audit, err := srv.SubstituteAuthForTest(context.Background(), ns, tc.runName, tc.req)
			switch {
			case tc.want.isAllow:
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if audit == nil || !strings.Contains(audit.Reason, tc.want.auditReason) {
					t.Fatalf("audit = %+v, want reason contains %q", audit, tc.want.auditReason)
				}
				if resp.SetHeaders == nil && resp.RemoveHeaders == nil {
					t.Errorf("response had neither SetHeaders nor RemoveHeaders; expected substituted credential")
				}
			default:
				if err == nil {
					t.Fatalf("err = nil, want app error %s", tc.want.appErrCode)
				}
				var appErr *broker.ApplicationErrorForTest
				if !errors.As(err, &appErr) {
					t.Fatalf("err = %v, want *applicationError", err)
				}
				if appErr.Code() != tc.want.appErrCode {
					t.Errorf("code = %q, want %q", appErr.Code(), tc.want.appErrCode)
				}
				if appErr.Status() != tc.want.appErrStatus {
					t.Errorf("status = %d, want %d", appErr.Status(), tc.want.appErrStatus)
				}
				if audit == nil || !strings.Contains(strings.ToLower(audit.Reason), tc.want.auditReason) {
					t.Fatalf("audit = %+v, want reason contains %q", audit, tc.want.auditReason)
				}
			}
		})
	}
}

func TestAuditWriter_NewAuditWriter_HappyPath(t *testing.T) {
	rec := &recordingAuditSink{}
	w := broker.NewAuditWriter(rec)
	if err := w.CredentialIssued(context.Background(), broker.CredentialAudit{
		RunName: "hr-1", Namespace: "team-a", CredentialName: "DEMO",
	}); err != nil {
		t.Fatalf("CredentialIssued: %v", err)
	}
	if got := len(rec.events()); got != 1 {
		t.Errorf("recorded %d events, want 1", got)
	}
}

func TestHandleIssue_OversizeBody_BadRequest(t *testing.T) {
	t.Parallel()
	srv, _ := setup(t)
	mux := http.NewServeMux()
	srv.Register(mux)

	body := bytes.Repeat([]byte("x"), 100<<10) // 100 KiB > 64 KiB cap
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, brokerapi.PathIssue, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer t")
	r.Header.Set(brokerapi.HeaderRun, "hello")
	r.Header.Set(brokerapi.HeaderNamespace, "my-team")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

// TestHandleSubstituteAuth_RateLimited_AuditFirst verifies that when the
// per-run substitute bucket is exhausted the handler emits a
// credential-denied AuditEvent BEFORE returning 429 (Phase 2c
// fail-closed-on-audit-failure contract). Also verifies 503 is returned
// if the audit write itself fails.
func TestHandleSubstituteAuth_RateLimited_AuditFirst(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{
		name:           "anthropic-stub",
		matched:        true,
		setHdrs:        map[string]string{"x-api-key": "real-key"},
		credentialName: "STUB_CRED",
	}
	srv, _ := setupSubstituteAuth(t, sub)
	rec := &recordingAuditSink{}
	srv.Audit = broker.NewAuditWriter(rec)
	srv.RunLimiter = broker.NewRunLimiterRegistry()

	// Drain the substitute burst using direct Allow() calls so we don't
	// need full broker semantics for each drain request.
	for i := 0; i < broker.SubstituteBurstForTest; i++ {
		srv.RunLimiter.Allow("team-a", "hr-1", "substitute")
	}

	body, _ := json.Marshal(brokerapi.SubstituteAuthRequest{
		Host: "api.anthropic.com", Port: 443,
		IncomingXAPIKey: "paddock-bearer-x",
	})
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		brokerapi.PathSubstituteAuth, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer valid-token")
	r.Header.Set(brokerapi.HeaderRun, "hr-1")
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	srv.Register(mux)
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	events := rec.events()
	if len(events) == 0 {
		t.Fatalf("audit write did not happen before 429")
	}
	if events[len(events)-1].Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Fatalf("audit kind = %s; want credential-denied", events[len(events)-1].Spec.Kind)
	}
	var errResp brokerapi.ErrorResponse
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp.Code != brokerapi.CodeRateLimited {
		t.Errorf("code = %q, want %q", errResp.Code, brokerapi.CodeRateLimited)
	}
}

// TestHandleSubstituteAuth_RateLimited_AuditFail_Returns503 verifies that
// when the rate-limit audit write fails the broker returns 503 (fail-closed
// on audit write failure — Phase 2c) rather than 429.
func TestHandleSubstituteAuth_RateLimited_AuditFail_Returns503(t *testing.T) {
	t.Parallel()
	sub := &stubSubstituter{
		name:           "anthropic-stub",
		matched:        true,
		setHdrs:        map[string]string{"x-api-key": "real-key"},
		credentialName: "STUB_CRED",
	}
	srv, _ := setupSubstituteAuth(t, sub)
	srv.Audit = broker.NewAuditWriter(errorSink{err: errors.New("etcd partition")})
	srv.RunLimiter = broker.NewRunLimiterRegistry()

	for i := 0; i < broker.SubstituteBurstForTest; i++ {
		srv.RunLimiter.Allow("team-a", "hr-1", "substitute")
	}

	body, _ := json.Marshal(brokerapi.SubstituteAuthRequest{
		Host: "api.anthropic.com", Port: 443,
		IncomingXAPIKey: "paddock-bearer-x",
	})
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		brokerapi.PathSubstituteAuth, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer valid-token")
	r.Header.Set(brokerapi.HeaderRun, "hr-1")
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	srv.Register(mux)
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s; want 503 when audit fails on rate-limited path", w.Code, w.Body.String())
	}
}

// TestHandleIssue_PATPool_PopulatesPoolSecretRefAndSlotIndex is a regression
// test for the F-14 end-to-end gap: populateDeliveryMetadata initially omitted
// PoolSecretRef and PoolSlotIndex, so the wire response always carried nil pool
// fields even though PATPoolProvider.Issue populated them on IssueResult. This
// test drives the full HTTP handler path (no FakeBroker) and asserts the wire
// response carries both pool fields.
func TestHandleIssue_PATPool_PopulatesPoolSecretRefAndSlotIndex(t *testing.T) {
	t.Parallel()
	const ns = "my-team"

	poolSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: ns},
		Data:       map[string][]byte{"pats": []byte("ghp_alice\nghp_bob\n")},
	}
	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo",
			Image:   "paddock-echo:v1",
			Command: []string{"/bin/echo"},
			Requires: paddockv1alpha1.RequireSpec{
				Credentials: []paddockv1alpha1.CredentialRequirement{
					{Name: "GITHUB_TOKEN"},
				},
			},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-pat", Namespace: ns},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Prompt:      "hi",
		},
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-pat", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: []paddockv1alpha1.CredentialGrant{{
					Name: "GITHUB_TOKEN",
					Provider: paddockv1alpha1.ProviderConfig{
						Kind:      "PATPool",
						SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "pool", Key: "pats"},
						Hosts:     []string{"github.com"},
					},
				}},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp, poolSecret).
		Build()

	registry, err := providers.NewRegistry(
		&providers.UserSuppliedSecretProvider{Client: c},
		&providers.PATPoolProvider{Client: c},
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}

	body, _ := json.Marshal(brokerapi.IssueRequest{Name: "GITHUB_TOKEN"})
	rr := post(t, srv, "hr-pat", ns, "token-abc", string(body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.IssueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PoolSecretRef == nil {
		t.Fatalf("PoolSecretRef = nil; populateDeliveryMetadata did not copy pool fields onto wire response")
	}
	if got.PoolSecretRef.Name != "pool" {
		t.Errorf("PoolSecretRef.Name = %q, want %q", got.PoolSecretRef.Name, "pool")
	}
	if got.PoolSecretRef.Key != "pats" {
		t.Errorf("PoolSecretRef.Key = %q, want %q", got.PoolSecretRef.Key, "pats")
	}
	if got.PoolSlotIndex == nil {
		t.Fatalf("PoolSlotIndex = nil; populateDeliveryMetadata did not copy pool slot index onto wire response")
	}
	if *got.PoolSlotIndex < 0 {
		t.Errorf("PoolSlotIndex = %d, want >= 0", *got.PoolSlotIndex)
	}
}

// TestValidateEgress_SubstituteAuth_DerivedFromCredentialGrant asserts
// that handleValidateEgress sets SubstituteAuth=true on the allow
// response when a matching BrokerPolicy has a credential grant with
// deliveryMode.proxyInjected.hosts covering the request host. Mirrors
// the v0.4 design comment at api/v1alpha1/brokerpolicy_types.go:192.
func TestValidateEgress_SubstituteAuth_DerivedFromCredentialGrant(t *testing.T) {
	t.Parallel()
	const ns = "my-team"

	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo", Image: "paddock-echo:v1", Command: []string{"/bin/echo"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Egress: []paddockv1alpha1.EgressGrant{
					{Host: "api.example.com", Ports: []int32{443}},
				},
				Credentials: []paddockv1alpha1.CredentialGrant{
					{
						Name: "TOKEN",
						Provider: paddockv1alpha1.ProviderConfig{
							Kind:      "UserSuppliedSecret",
							SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
							DeliveryMode: &paddockv1alpha1.DeliveryMode{
								ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
									Hosts:  []string{"api.example.com"},
									Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization", ValuePrefix: "Bearer "},
								},
							},
						},
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(tpl, run, bp).Build()
	registry, err := providers.NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv := &broker.Server{
		Client: c, Auth: stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}

	body, _ := json.Marshal(brokerapi.ValidateEgressRequest{Host: "api.example.com", Port: 443})
	rr := postValidateEgress(t, srv, "hello", "", "token-abc", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.ValidateEgressResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Allowed {
		t.Errorf("Allowed = false, want true")
	}
	if !got.SubstituteAuth {
		t.Errorf("SubstituteAuth = false, want true (credential grant covers api.example.com)")
	}
}

// substituteAuthTestHelper builds a fake client with the standard
// (echo template, hello run) fixture plus the supplied policies, posts
// the supplied (host, port) at /v1/validate-egress, and returns the
// decoded ValidateEgressResponse. Used by the SubstituteAuth derivation
// tests below to keep each case to its own BrokerPolicy fixture.
func substituteAuthTestHelper(t *testing.T, host string, port int, policies ...*paddockv1alpha1.BrokerPolicy) brokerapi.ValidateEgressResponse {
	t.Helper()
	const ns = "my-team"
	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo", Image: "paddock-echo:v1", Command: []string{"/bin/echo"},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: ns},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Prompt:      "hi",
		},
	}
	objs := make([]client.Object, 0, 2+len(policies))
	objs = append(objs, tpl, run)
	for _, p := range policies {
		objs = append(objs, p)
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(objs...).Build()
	registry, err := providers.NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv := &broker.Server{
		Client: c, Auth: stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"}),
	}
	body, _ := json.Marshal(brokerapi.ValidateEgressRequest{Host: host, Port: port})
	rr := postValidateEgress(t, srv, "hello", "", "token-abc", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got brokerapi.ValidateEgressResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// brokerPolicyFixture is a small DSL for the SubstituteAuth tests.
// fields zero-value to "no grant of this kind"; pass non-nil to enable.
type brokerPolicyFixture struct {
	name             string
	appliesToEcho    bool     // sets AppliesToTemplates: ["echo"] when true
	egressHosts      []string // empty → no egress grants
	credentialHosts  []string // empty → no proxyInjected credential grant
	credentialIsBare bool     // if true: credential grant has provider.deliveryMode = nil (malformed)
	credentialIsInC  bool     // if true: credential grant has only inContainer, no proxyInjected
	discoveryActive  bool     // if true: sets EgressDiscovery with expiry +1h
}

// makePolicy builds a BrokerPolicy from f. Field combinations are
// deliberately permissive — admission would reject some shapes, but
// the runtime helper must handle them defensively.
func makePolicy(f brokerPolicyFixture) *paddockv1alpha1.BrokerPolicy {
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: f.name, Namespace: "my-team"},
		Spec:       paddockv1alpha1.BrokerPolicySpec{},
	}
	if f.appliesToEcho {
		bp.Spec.AppliesToTemplates = []string{"echo"}
	}
	for _, h := range f.egressHosts {
		bp.Spec.Grants.Egress = append(bp.Spec.Grants.Egress,
			paddockv1alpha1.EgressGrant{Host: h, Ports: []int32{443}})
	}
	if len(f.credentialHosts) > 0 {
		bp.Spec.Grants.Credentials = append(bp.Spec.Grants.Credentials,
			paddockv1alpha1.CredentialGrant{
				Name: "TOKEN",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind:      "UserSuppliedSecret",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
					DeliveryMode: &paddockv1alpha1.DeliveryMode{
						ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
							Hosts:  f.credentialHosts,
							Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization", ValuePrefix: "Bearer "},
						},
					},
				},
			})
	}
	if f.credentialIsInC {
		bp.Spec.Grants.Credentials = append(bp.Spec.Grants.Credentials,
			paddockv1alpha1.CredentialGrant{
				Name: "INC_TOKEN",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind:      "UserSuppliedSecret",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
					DeliveryMode: &paddockv1alpha1.DeliveryMode{
						InContainer: &paddockv1alpha1.InContainerDelivery{
							Accepted: true, Reason: "inline plaintext for the inContainer test fixture",
						},
					},
				},
			})
	}
	if f.credentialIsBare {
		bp.Spec.Grants.Credentials = append(bp.Spec.Grants.Credentials,
			paddockv1alpha1.CredentialGrant{
				Name: "BARE_TOKEN",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind:      "UserSuppliedSecret",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
					// DeliveryMode intentionally nil
				},
			})
	}
	if f.discoveryActive {
		bp.Spec.EgressDiscovery = &paddockv1alpha1.EgressDiscoverySpec{
			Accepted:  true,
			Reason:    "testing discovery branch leaves SubstituteAuth false",
			ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)),
		}
	}
	return bp
}

// TestValidateEgress_SubstituteAuth_NoCredentialGrant — egress only.
func TestValidateEgress_SubstituteAuth_NoCredentialGrant(t *testing.T) {
	t.Parallel()
	got := substituteAuthTestHelper(t, "api.example.com", 443,
		makePolicy(brokerPolicyFixture{
			name: "p", appliesToEcho: true,
			egressHosts: []string{"api.example.com"},
		}))
	if got.SubstituteAuth {
		t.Errorf("SubstituteAuth = true, want false (no credential grant)")
	}
}

// TestValidateEgress_SubstituteAuth_CredentialForDifferentHost — credential
// grant covers a host other than the request host.
func TestValidateEgress_SubstituteAuth_CredentialForDifferentHost(t *testing.T) {
	t.Parallel()
	got := substituteAuthTestHelper(t, "api.example.com", 443,
		makePolicy(brokerPolicyFixture{
			name: "p", appliesToEcho: true,
			egressHosts:     []string{"api.example.com"},
			credentialHosts: []string{"other.example.com"},
		}))
	if got.SubstituteAuth {
		t.Errorf("SubstituteAuth = true, want false (credential covers other.example.com)")
	}
}

// TestValidateEgress_SubstituteAuth_WildcardMatch — *.foo.com matches api.foo.com.
func TestValidateEgress_SubstituteAuth_WildcardMatch(t *testing.T) {
	t.Parallel()
	got := substituteAuthTestHelper(t, "api.foo.com", 443,
		makePolicy(brokerPolicyFixture{
			name: "p", appliesToEcho: true,
			egressHosts:     []string{"*.foo.com"},
			credentialHosts: []string{"*.foo.com"},
		}))
	if !got.SubstituteAuth {
		t.Errorf("SubstituteAuth = false, want true (*.foo.com covers api.foo.com)")
	}
}

// TestValidateEgress_SubstituteAuth_MultiPolicyAnyWins — egress on policy 1,
// credential on policy 2, both apply to "echo".
func TestValidateEgress_SubstituteAuth_MultiPolicyAnyWins(t *testing.T) {
	t.Parallel()
	bp1 := makePolicy(brokerPolicyFixture{
		name: "p-egress", appliesToEcho: true,
		egressHosts: []string{"api.example.com"},
	})
	bp2 := makePolicy(brokerPolicyFixture{
		name: "p-cred", appliesToEcho: true,
		credentialHosts: []string{"api.example.com"},
	})
	got := substituteAuthTestHelper(t, "api.example.com", 443, bp1, bp2)
	if !got.SubstituteAuth {
		t.Errorf("SubstituteAuth = false, want true (second policy has covering credential)")
	}
}

// TestValidateEgress_SubstituteAuth_DiscoveryAllowReturnsFalse — the
// discovery branch of handleValidateEgress must NOT derive SubstituteAuth,
// even when a covering credential grant exists on the same policy.
func TestValidateEgress_SubstituteAuth_DiscoveryAllowReturnsFalse(t *testing.T) {
	t.Parallel()
	got := substituteAuthTestHelper(t, "api.example.com", 443,
		makePolicy(brokerPolicyFixture{
			name: "p", appliesToEcho: true,
			// no egress grants → forces discovery branch
			credentialHosts: []string{"api.example.com"},
			discoveryActive: true,
		}))
	if !got.DiscoveryAllow {
		t.Fatalf("DiscoveryAllow = false; precondition for this test is the discovery branch")
	}
	if got.SubstituteAuth {
		t.Errorf("SubstituteAuth = true on discovery path, want false")
	}
}

// TestValidateEgress_SubstituteAuth_InContainerOnlyReturnsFalse — a
// credential grant with only inContainer delivery does NOT trigger MITM.
func TestValidateEgress_SubstituteAuth_InContainerOnlyReturnsFalse(t *testing.T) {
	t.Parallel()
	got := substituteAuthTestHelper(t, "api.example.com", 443,
		makePolicy(brokerPolicyFixture{
			name: "p", appliesToEcho: true,
			egressHosts:     []string{"api.example.com"},
			credentialIsInC: true,
		}))
	if got.SubstituteAuth {
		t.Errorf("SubstituteAuth = true for inContainer-only credential, want false")
	}
}

// TestValidateEgress_SubstituteAuth_MalformedGrantReturnsFalse — a
// credential grant with neither inContainer nor proxyInjected delivery
// must not panic and must not trigger MITM. Admission should reject this
// shape; the runtime must be defensive.
func TestValidateEgress_SubstituteAuth_MalformedGrantReturnsFalse(t *testing.T) {
	t.Parallel()
	got := substituteAuthTestHelper(t, "api.example.com", 443,
		makePolicy(brokerPolicyFixture{
			name: "p", appliesToEcho: true,
			egressHosts:      []string{"api.example.com"},
			credentialIsBare: true,
		}))
	if got.SubstituteAuth {
		t.Errorf("SubstituteAuth = true on malformed grant, want false")
	}
}
