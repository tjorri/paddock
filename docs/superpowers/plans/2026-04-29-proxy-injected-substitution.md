# ProxyInjected substitution: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore broker-driven MITM substitution for `ProxyInjected` credential grants and add the controller plumbing + hermetic e2e regression that prevent it from silently regressing again.

**Architecture:** Three coupled changes. (1) `internal/broker/server.go::handleValidateEgress` derives `SubstituteAuth: true` from any matching `BrokerPolicy`'s credential grants whose `deliveryMode.proxyInjected.hosts` covers the request host (the v0.4 commit-stated derivation that was never written). (2) New controller flag `--proxy-upstream-extra-cas-configmap` projects a ConfigMap into per-run proxy Pods and wires `--upstream-ca-bundle` so operators (and the new e2e) can trust private upstreams. (3) Hermetic e2e test deploys an in-cluster TLS echo listener with a CA-signed cert, runs a HarnessRun against it, asserts the listener received the *substituted* real-secret value (not the surrogate).

**Tech Stack:** Go 1.23, controller-runtime, Ginkgo/Gomega for e2e, kubernetes-sigs/controller-runtime fake client for unit tests, cert-manager (already installed in the e2e cluster) is *not* used by this plan — test certs are generated in-process via `crypto/x509`. Listener image: `mendhak/http-https-echo:34`.

**Spec:** `docs/superpowers/specs/2026-04-29-proxy-injected-substitution-design.md`

---

## File structure

**Create:**

- `test/utils/tls.go` — `GenerateCAAndLeaf(dnsName)` test helper (CA cert/key + leaf cert/key, all PEM).
- `test/utils/tls_test.go` — unit tests for the helper.
- `test/e2e/proxy_substitution_test.go` — the hermetic e2e regression spec.

**Modify:**

- `internal/broker/matching.go` — add `anyProxyInjectedHostCovers(ctx, c, namespace, templateName, host)`.
- `internal/broker/server.go` — extend `handleValidateEgress` allow path to call the helper and set `SubstituteAuth`.
- `internal/broker/server_test.go` — add 7 handler-level tests (covers + non-covers + wildcard + multi-policy + discovery + inContainer-only + malformed-grant).
- `internal/controller/pod_spec.go` — add field to `podSpecInputs`, add constant for the new volume name/mount path, conditionally append volume in `buildPodVolumes`, conditionally append mount + arg in `buildProxyContainer`.
- `internal/controller/pod_spec_test.go` — add tests for the new field's effect on the generated PodSpec.
- `internal/controller/harnessrun_controller.go` — add `ProxyUpstreamExtraCAsConfigMap` field on the reconciler; wire it into `podSpecInputs` from `ensureJob`.
- `cmd/main.go` — add `--proxy-upstream-extra-cas-configmap` flag; pass to reconciler.
- `config/manager/manager.yaml` — add the flag with the canonical ConfigMap name.
- `test/e2e/e2e_suite_test.go` — `BeforeSuite` ensures a dummy `paddock-proxy-upstream-cas` ConfigMap exists in `paddock-system` before `make deploy`.

---

## Task 1: Broker — derive `SubstituteAuth` from credential grants

**Files:**
- Modify: `internal/broker/matching.go` — add helper at end of file.
- Modify: `internal/broker/server.go:415-418` — extend allow path.
- Modify: `internal/broker/server_test.go` — add 7 tests.

**Test design rationale:** The helper is a private function but its only consumer is `handleValidateEgress`. Test through the HTTP handler with `postValidateEgress` (the existing test helper) — that exercises both the helper's correctness and its wiring. Pattern mirrors `TestValidateEgress_DiscoveryAllow` (server_test.go:542).

- [ ] **Step 1: Write the first failing test (basic positive case)**

Append to `internal/broker/server_test.go`:

```go
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
			Grants: paddockv1alpha1.GrantsSpec{
				Egress: []paddockv1alpha1.EgressGrant{
					{Host: "api.example.com", Ports: []int32{443}},
				},
				Credentials: []paddockv1alpha1.CredentialGrant{
					{
						Name: "TOKEN",
						Provider: paddockv1alpha1.ProviderSpec{
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
```

- [ ] **Step 2: Run the failing test**

Run: `go test ./internal/broker/ -run TestValidateEgress_SubstituteAuth_DerivedFromCredentialGrant -v`
Expected: FAIL with `SubstituteAuth = false, want true`.

- [ ] **Step 3: Add the helper in `internal/broker/matching.go`**

Append at the end of the file (after `egressCovers`):

```go
// anyProxyInjectedHostCovers reports whether any BrokerPolicy in
// namespace that applies to templateName has a credential grant whose
// deliveryMode.proxyInjected.hosts covers host. Used by
// handleValidateEgress to decide SubstituteAuth on the allow path.
//
// Multi-policy semantics: any-wins (mirrors egressDiscovery; matches
// the v0.4 ethos that matching policies compose additively). Host
// matching reuses policy.AnyHostMatches so "*.foo.com" works.
//
// Errors propagate from List; nil error + false return on no match.
func anyProxyInjectedHostCovers(
	ctx context.Context,
	c client.Client,
	namespace, templateName, host string,
) (bool, error) {
	var list paddockv1alpha1.BrokerPolicyList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	for i := range list.Items {
		bp := &list.Items[i]
		if !policy.AppliesToTemplate(bp.Spec.AppliesToTemplates, templateName) {
			continue
		}
		for j := range bp.Spec.Grants.Credentials {
			g := &bp.Spec.Grants.Credentials[j]
			if g.Provider.DeliveryMode == nil || g.Provider.DeliveryMode.ProxyInjected == nil {
				continue
			}
			if policy.AnyHostMatches(g.Provider.DeliveryMode.ProxyInjected.Hosts, host) {
				return true, nil
			}
		}
	}
	return false, nil
}
```

- [ ] **Step 4: Wire the helper into `handleValidateEgress`**

In `internal/broker/server.go`, replace the allow-path `writeJSON` block at lines 415-418 with:

```go
needsSubstitute, err := anyProxyInjectedHostCovers(ctx, s.Client, runNamespace, run.Spec.TemplateRef.Name, req.Host)
if err != nil {
	writeError(w, http.StatusInternalServerError, "ProviderFailure", err.Error())
	return
}
writeJSON(w, http.StatusOK, brokerapi.ValidateEgressResponse{
	Allowed:        true,
	MatchedPolicy:  policyName,
	SubstituteAuth: needsSubstitute,
})
```

- [ ] **Step 5: Run the test, expect pass**

Run: `go test ./internal/broker/ -run TestValidateEgress_SubstituteAuth_DerivedFromCredentialGrant -v`
Expected: PASS.

- [ ] **Step 6: Add a shared test helper to keep follow-up cases tight**

Append to `internal/broker/server_test.go`, just below the test from step 1:

```go
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
	objs := []client.Object{tpl, run}
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
	appliesToEcho    bool                                   // sets AppliesToTemplates: ["echo"] when true
	egressHosts      []string                               // empty → no egress grants
	credentialHosts  []string                               // empty → no proxyInjected credential grant
	credentialIsBare bool                                   // if true: credential grant has provider.deliveryMode = nil (malformed)
	credentialIsInC  bool                                   // if true: credential grant has only inContainer, no proxyInjected
	discoveryActive  bool                                   // if true: sets EgressDiscovery with expiry +1h
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
				Provider: paddockv1alpha1.ProviderSpec{
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
				Provider: paddockv1alpha1.ProviderSpec{
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
				Provider: paddockv1alpha1.ProviderSpec{
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
```

Note: `client` is `sigs.k8s.io/controller-runtime/pkg/client` — already imported by `server_test.go`. If the import is missing on your branch, add it.

- [ ] **Step 7: Add the remaining 7 tests using the helper**

Append:

```go
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
```

- [ ] **Step 8: Run all 8 broker-derivation tests**

Run: `go test ./internal/broker/ -run TestValidateEgress_SubstituteAuth -v`
Expected: 8 PASS, 0 FAIL.

- [ ] **Step 9: Run the full broker package to confirm no regressions**

Run: `go test ./internal/broker/...`
Expected: PASS (every existing test still passes — we only added one allow-path branch).

- [ ] **Step 10: Commit**

```bash
git add internal/broker/matching.go internal/broker/server.go internal/broker/server_test.go
git commit -m "fix(broker): derive SubstituteAuth from credential grants on validate-egress

Restore the v0.4-stated implicit substitution trigger: handleValidateEgress
now walks matching BrokerPolicies' credential grants and sets
SubstituteAuth=true when any deliveryMode.proxyInjected.hosts covers the
requested host. Multi-policy any-wins; host-only matching via
policy.AnyHostMatches. Discovery-allow path keeps SubstituteAuth=false.

Refs #83"
```

---

## Task 2: Controller — proxy Pod plumbing for upstream extra CAs

**Files:**
- Modify: `internal/controller/pod_spec.go` (constants block + `podSpecInputs` struct + `buildPodVolumes` + `buildProxyContainer`).
- Modify: `internal/controller/pod_spec_test.go` (add 3 test cases).

- [ ] **Step 1: Write a failing test asserting the volume + mount + arg are present when the field is set**

Append to `internal/controller/pod_spec_test.go`:

```go
// TestBuildPodSpec_UpstreamExtraCAs_AddsVolumeMountAndArg asserts that
// when podSpecInputs.upstreamExtraCAsConfigMap is non-empty, the
// rendered PodSpec gains:
//   - a Volume named "upstream-extra-cas" sourced from the named ConfigMap
//   - a VolumeMount on the proxy container at /etc/paddock-proxy/extra-cas
//   - a --upstream-ca-bundle=<mountpath>/bundle.pem arg on the proxy container
//
// Mirrors the spec at docs/superpowers/specs/2026-04-29-proxy-injected-substitution-design.md §2.
func TestBuildPodSpec_UpstreamExtraCAs_AddsVolumeMountAndArg(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	in := defaultInputs()
	in.proxyImage = "paddock-proxy:dev"
	in.proxyTLSSecret = "run-echo-proxy-tls"
	in.brokerEndpoint = "https://broker.example.com"
	in.upstreamExtraCAsConfigMap = "paddock-proxy-upstream-cas"

	ps := buildPodSpec(run, tpl, in)

	// Volume present.
	var vol *corev1.Volume
	for i := range ps.Volumes {
		if ps.Volumes[i].Name == "upstream-extra-cas" {
			vol = &ps.Volumes[i]
			break
		}
	}
	if vol == nil {
		t.Fatalf("Volume %q not found in PodSpec.Volumes", "upstream-extra-cas")
	}
	if vol.ConfigMap == nil {
		t.Fatalf("Volume %q is not a ConfigMap source: %+v", "upstream-extra-cas", vol)
	}
	if vol.ConfigMap.Name != "paddock-proxy-upstream-cas" {
		t.Errorf("Volume.ConfigMap.Name = %q, want %q", vol.ConfigMap.Name, "paddock-proxy-upstream-cas")
	}

	// Mount on proxy container.
	var proxy *corev1.Container
	for i := range ps.InitContainers {
		if ps.InitContainers[i].Name == proxyContainerName {
			proxy = &ps.InitContainers[i]
			break
		}
	}
	if proxy == nil {
		t.Fatalf("proxy init container %q not found", proxyContainerName)
	}
	var hasMount bool
	for _, m := range proxy.VolumeMounts {
		if m.Name == "upstream-extra-cas" && m.MountPath == "/etc/paddock-proxy/extra-cas" && m.ReadOnly {
			hasMount = true
			break
		}
	}
	if !hasMount {
		t.Errorf("proxy container missing readOnly mount for upstream-extra-cas at /etc/paddock-proxy/extra-cas; mounts=%+v", proxy.VolumeMounts)
	}

	// Arg on proxy container.
	wantArg := "--upstream-ca-bundle=/etc/paddock-proxy/extra-cas/bundle.pem"
	var hasArg bool
	for _, a := range proxy.Args {
		if a == wantArg {
			hasArg = true
			break
		}
	}
	if !hasArg {
		t.Errorf("proxy container missing arg %q; args=%v", wantArg, proxy.Args)
	}
}

// TestBuildPodSpec_UpstreamExtraCAs_EmptyOmitsAll asserts the default
// (production) shape: when the field is empty, no volume/mount/arg
// related to upstream-extra-cas is added — byte-identical to today's
// PodSpec for proxy-using runs.
func TestBuildPodSpec_UpstreamExtraCAs_EmptyOmitsAll(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	in := defaultInputs()
	in.proxyImage = "paddock-proxy:dev"
	in.proxyTLSSecret = "run-echo-proxy-tls"
	in.brokerEndpoint = "https://broker.example.com"
	// upstreamExtraCAsConfigMap intentionally empty

	ps := buildPodSpec(run, tpl, in)

	for _, v := range ps.Volumes {
		if v.Name == "upstream-extra-cas" {
			t.Errorf("Volume %q present when upstreamExtraCAsConfigMap is empty", v.Name)
		}
	}
	for i := range ps.InitContainers {
		c := &ps.InitContainers[i]
		if c.Name != proxyContainerName {
			continue
		}
		for _, m := range c.VolumeMounts {
			if m.Name == "upstream-extra-cas" {
				t.Errorf("proxy container has upstream-extra-cas mount when field is empty")
			}
		}
		for _, a := range c.Args {
			if strings.HasPrefix(a, "--upstream-ca-bundle=") {
				t.Errorf("proxy container has --upstream-ca-bundle arg when field is empty: %q", a)
			}
		}
	}
}

// TestBuildPodSpec_UpstreamExtraCAs_NoProxyOmitsAll asserts that when
// the proxy sidecar isn't configured at all (proxyImage empty), the
// upstream-extra-cas plumbing is absent regardless of the field value.
func TestBuildPodSpec_UpstreamExtraCAs_NoProxyOmitsAll(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	in := defaultInputs()
	// proxyImage intentionally empty — no proxy sidecar
	in.upstreamExtraCAsConfigMap = "paddock-proxy-upstream-cas"

	ps := buildPodSpec(run, tpl, in)
	for _, v := range ps.Volumes {
		if v.Name == "upstream-extra-cas" {
			t.Errorf("Volume %q present without proxy sidecar", v.Name)
		}
	}
}
```

If `strings` is not yet imported in `pod_spec_test.go`, add it.

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/controller/ -run TestBuildPodSpec_UpstreamExtraCAs -v`
Expected: 3 FAIL with "Volume not found" / "missing mount" / "missing arg" — and a compile error on `in.upstreamExtraCAsConfigMap` because the field doesn't exist yet.

- [ ] **Step 3: Add the field to `podSpecInputs`**

In `internal/controller/pod_spec.go`, after the `proxyDenyCIDR` field (around line 222), add:

```go
	// upstreamExtraCAsConfigMap, when non-empty, names a ConfigMap in the
	// controller's own namespace whose `bundle.pem` key holds extra CA
	// certs for the proxy's upstream verification. Surfaced as the new
	// --proxy-upstream-extra-cas-configmap controller flag (cmd/main.go).
	// Empty (production default) → no volume / mount / arg added.
	upstreamExtraCAsConfigMap string
```

- [ ] **Step 4: Add the constants**

In `internal/controller/pod_spec.go`, near the existing volume-name constants (around line 80-91), add:

```go
	// upstreamExtraCAsVolumeName is the volume that mounts the operator-
	// configured extra-CAs ConfigMap into the proxy container so the
	// proxy can verify private upstream certs via --upstream-ca-bundle.
	upstreamExtraCAsVolumeName = "upstream-extra-cas"
	upstreamExtraCAsMountPath  = "/etc/paddock-proxy/extra-cas"
	upstreamExtraCAsBundleKey  = "bundle.pem"
```

- [ ] **Step 5: Append the volume in `buildPodVolumes`**

In `internal/controller/pod_spec.go`, locate the `if proxyEnabled(in) { vols = append(vols, corev1.Volume{Name: proxyCAVolumeName, ...})}` block (around line 544). Immediately after that closing `}`, add:

```go
	if proxyEnabled(in) && in.upstreamExtraCAsConfigMap != "" {
		vols = append(vols, corev1.Volume{
			Name: upstreamExtraCAsVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: in.upstreamExtraCAsConfigMap,
					},
				},
			},
		})
	}
```

- [ ] **Step 6: Append the mount + arg in `buildProxyContainer`**

In `internal/controller/pod_spec.go::buildProxyContainer`, locate the `mounts := []corev1.VolumeMount{...}` block (around line 673). After the existing `if in.brokerEndpoint != "" { mounts = append(...) }` block, add:

```go
	if in.upstreamExtraCAsConfigMap != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      upstreamExtraCAsVolumeName,
			MountPath: upstreamExtraCAsMountPath,
			ReadOnly:  true,
		})
		args = append(args,
			fmt.Sprintf("--upstream-ca-bundle=%s/%s", upstreamExtraCAsMountPath, upstreamExtraCAsBundleKey))
	}
```

- [ ] **Step 7: Run the tests, expect pass**

Run: `go test ./internal/controller/ -run TestBuildPodSpec_UpstreamExtraCAs -v`
Expected: 3 PASS.

- [ ] **Step 8: Run the full controller package to confirm no regressions**

Run: `go test ./internal/controller/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/controller/pod_spec.go internal/controller/pod_spec_test.go
git commit -m "feat(controller): plumb --upstream-ca-bundle into per-run proxy

Adds upstreamExtraCAsConfigMap to podSpecInputs. When set, buildPodSpec
mounts the named ConfigMap at /etc/paddock-proxy/extra-cas/ on the proxy
container and appends --upstream-ca-bundle=<path>/bundle.pem to its args.
Empty default → no plumbing added (production behaviour unchanged).

Lets operators trust private upstream CAs (and lets the new e2e test in
test/e2e/proxy_substitution_test.go trust an in-cluster echo listener).

Refs #83"
```

---

## Task 3: Controller — wire reconciler field + manager flag

**Files:**
- Modify: `internal/controller/harnessrun_controller.go` (struct field + `ensureJob` plumb).
- Modify: `cmd/main.go` (flag + reconciler init).
- Modify: `config/manager/manager.yaml` (canonical flag value for cluster-installed manager).

- [ ] **Step 1: Add the field to `HarnessRunReconciler`**

In `internal/controller/harnessrun_controller.go`, locate the `IPTablesInitImage string` field (around line 125). Immediately after that field, add:

```go
	// ProxyUpstreamExtraCAsConfigMap, when non-empty, names a ConfigMap
	// in the controller's own namespace whose `bundle.pem` key carries
	// extra CA certs the per-run proxy trusts on the upstream side.
	// Surfaced as --proxy-upstream-extra-cas-configmap on the manager.
	ProxyUpstreamExtraCAsConfigMap string
```

- [ ] **Step 2: Plumb it into `ensureJob`**

In `internal/controller/harnessrun_controller.go::ensureJob` (around line 1175), inside the `if r.proxyConfigured() { ... }` block, after the existing assignments and before `desired := buildJob(...)`, add:

```go
		in.upstreamExtraCAsConfigMap = r.ProxyUpstreamExtraCAsConfigMap
```

- [ ] **Step 3: Add the flag in `cmd/main.go`**

In `cmd/main.go`, locate the existing proxy-related flags (around lines 127-128, the `--proxy-image` flag). Immediately after the `--proxy-image` flag declaration, add:

```go
	var proxyUpstreamExtraCAsConfigMap string
	flag.StringVar(&proxyUpstreamExtraCAsConfigMap, "proxy-upstream-extra-cas-configmap", "",
		"Optional ConfigMap name (in the controller's namespace) whose `bundle.pem` "+
			"key holds extra CA certs the per-run proxy trusts on its upstream side. "+
			"Empty disables (production default).")
```

- [ ] **Step 4: Pass the flag value to the reconciler**

In `cmd/main.go`, locate the `hrReconciler := &controller.HarnessRunReconciler{...}` block (around line 364). Add a field initialisation alongside the existing `ProxyImage`/`IPTablesInitImage`/etc. lines:

```go
		ProxyUpstreamExtraCAsConfigMap: proxyUpstreamExtraCAsConfigMap,
```

- [ ] **Step 5: Build & vet**

Run: `go vet -tags=e2e ./... && go build ./cmd/...`
Expected: PASS (no vet warnings, binary builds).

- [ ] **Step 6: Run the controller package tests to confirm no regression**

Run: `go test ./internal/controller/...`
Expected: PASS.

- [ ] **Step 7: Add the flag in `config/manager/manager.yaml`**

In `config/manager/manager.yaml`, locate the existing `--proxy-image=paddock-proxy:dev` line (around line 95). Immediately after it, add (matching the existing 10-space indent):

```yaml
          - --proxy-upstream-extra-cas-configmap=paddock-proxy-upstream-cas
```

- [ ] **Step 8: Commit**

```bash
git add internal/controller/harnessrun_controller.go cmd/main.go config/manager/manager.yaml
git commit -m "feat(manager): --proxy-upstream-extra-cas-configmap flag

Wires the new podSpecInputs field through the reconciler and exposes a
controller-manager flag that names a ConfigMap whose bundle.pem holds
extra CAs the per-run proxy trusts upstream. config/manager/manager.yaml
sets the flag to the canonical name 'paddock-proxy-upstream-cas' so the
e2e suite (and any operator using the default install) can populate
that ConfigMap.

Refs #83"
```

---

## Task 4: Test util — `GenerateCAAndLeaf` helper

**Files:**
- Create: `test/utils/tls.go`.
- Create: `test/utils/tls_test.go`.

- [ ] **Step 1: Write the failing test**

Create `test/utils/tls_test.go`:

```go
package utils

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

// TestGenerateCAAndLeaf_RoundTrip asserts the helper returns a CA that
// successfully verifies the leaf, with the leaf's CN/SAN equal to the
// dnsName argument. Without this, the e2e test would silently produce
// untrusted certs.
func TestGenerateCAAndLeaf_RoundTrip(t *testing.T) {
	const dnsName = "probe-listener.paddock-test-substitution.svc.cluster.local"
	caPEM, leafPEM, leafKeyPEM, err := GenerateCAAndLeaf(dnsName)
	if err != nil {
		t.Fatalf("GenerateCAAndLeaf: %v", err)
	}
	if !strings.HasPrefix(string(caPEM), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("caPEM does not look like PEM: %q", caPEM[:40])
	}
	if !strings.HasPrefix(string(leafPEM), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("leafPEM does not look like PEM: %q", leafPEM[:40])
	}
	if !strings.Contains(string(leafKeyPEM), "PRIVATE KEY") {
		t.Errorf("leafKeyPEM does not contain a private key: %q", leafKeyPEM[:40])
	}

	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		t.Fatalf("caPEM did not parse")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(ca): %v", err)
	}
	leafBlock, _ := pem.Decode(leafPEM)
	if leafBlock == nil {
		t.Fatalf("leafPEM did not parse")
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(leaf): %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{
		Roots:       pool,
		DNSName:     dnsName,
		CurrentTime: time.Now(),
	}); err != nil {
		t.Errorf("leaf failed to verify under CA: %v", err)
	}
}
```

- [ ] **Step 2: Run the test, expect compile failure**

Run: `go test ./test/utils/ -run TestGenerateCAAndLeaf -v`
Expected: FAIL with `undefined: GenerateCAAndLeaf`.

- [ ] **Step 3: Implement the helper**

Create `test/utils/tls.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// GenerateCAAndLeaf builds an in-memory test CA and a leaf certificate
// signed by it, with the given dnsName as the leaf's Subject Alternative
// Name. Returns three PEM blobs (CA cert, leaf cert, leaf private key).
//
// Test-only — do not use in production. P256 keys, 24h validity, no
// CRL/OCSP, no key usages beyond what's needed for serverAuth.
func GenerateCAAndLeaf(dnsName string) (caPEM, leafPEM, leafKeyPEM []byte, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating CA key: %w", err)
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "paddock-e2e-test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating CA cert: %w", err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating leaf key: %w", err)
	}
	leafTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parsing CA back: %w", err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating leaf cert: %w", err)
	}
	leafPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})

	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshalling leaf key: %w", err)
	}
	leafKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER})

	return caPEM, leafPEM, leafKeyPEM, nil
}
```

- [ ] **Step 4: Run the test, expect pass**

Run: `go test ./test/utils/ -run TestGenerateCAAndLeaf -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add test/utils/tls.go test/utils/tls_test.go
git commit -m "test(utils): add GenerateCAAndLeaf helper for in-process e2e certs

In-memory CA + leaf keypair generator (P256, 24h validity, serverAuth)
used by the proxy-substitution e2e test to seed an in-cluster TLS
listener without depending on cert-manager.

Refs #83"
```

---

## Task 5: E2E suite — dummy upstream-CAs ConfigMap in `BeforeSuite`

**Why:** The controller flag `--proxy-upstream-extra-cas-configmap=paddock-proxy-upstream-cas` is set in `config/manager/manager.yaml`, so every per-run proxy Pod expects that ConfigMap to exist. Without a `BeforeSuite` seed, every other e2e spec's runs would fail to mount the volume. The seed creates a benign self-signed dummy CA so the proxy starts cleanly; the substitution test overwrites the data in its own `BeforeAll`/`AfterAll`.

**Files:**
- Modify: `test/e2e/e2e_suite_test.go`.

- [ ] **Step 1: Add the seed ConfigMap before `make deploy`**

In `test/e2e/e2e_suite_test.go::BeforeSuite`, immediately *after* the `make install` step and *before* the `make deploy` step (around lines 117-120), add:

```go
	By("seeding paddock-proxy-upstream-cas ConfigMap (dummy CA so per-run proxy Pods can mount it)")
	dummyCA, _, _, err := utils.GenerateCAAndLeaf("paddock-e2e-dummy.invalid")
	Expect(err).NotTo(HaveOccurred(), "GenerateCAAndLeaf for dummy upstream CA")
	dummyCMYaml := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: paddock-proxy-upstream-cas
  namespace: paddock-system
data:
  bundle.pem: |
%s`, indent4(string(dummyCA)))
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(dummyCMYaml)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "seeding dummy upstream-CAs ConfigMap")
```

The `paddock-system` namespace is created by `make install` (CRD install) so it must exist by the time we apply the ConfigMap.

- [ ] **Step 2: Add the `indent4` helper at the bottom of the file**

```go
// indent4 prepends four spaces to every line in s. Used to embed a PEM
// CA into a YAML literal-block scalar.
func indent4(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 3: Add `strings` and `fmt` imports if not already present**

Verify that `test/e2e/e2e_suite_test.go` imports both `fmt` and `strings`. If either is missing, add it.

- [ ] **Step 4: Build the e2e package to confirm no compile errors**

Run: `go test -tags=e2e -c -o /tmp/paddock-e2e ./test/e2e/`
Expected: compile succeeds (binary written).

- [ ] **Step 5: Smoke the seed via a one-shot e2e run**

This is a slow check (full suite); skip if local iteration is tight, but worth it before commit.

Run: `KIND_CLUSTER=paddock-test-e2e make test-e2e 2>&1 | tee /tmp/e2e-task5.log`
Expected: BeforeSuite logs `seeding paddock-proxy-upstream-cas ConfigMap`; the suite continues into existing specs which all still pass (they don't use the ConfigMap content but the controller now sets the flag, so the proxy Pod must be able to mount it).

- [ ] **Step 6: Commit**

```bash
git add test/e2e/e2e_suite_test.go
git commit -m "test(e2e): seed dummy paddock-proxy-upstream-cas ConfigMap in BeforeSuite

Now that the controller manager always sets
--proxy-upstream-extra-cas-configmap=paddock-proxy-upstream-cas, every
per-run proxy Pod tries to mount that ConfigMap. BeforeSuite seeds it
with a benign self-signed dummy CA so unrelated e2e specs don't fail
to start their proxy. The substitution spec (next commit) overwrites
the data in BeforeAll and restores it in AfterAll.

Refs #83"
```

---

## Task 6: E2E — hermetic substitution regression test

**Files:**
- Create: `test/e2e/proxy_substitution_test.go`.

- [ ] **Step 1: Scaffold the spec file**

Create `test/e2e/proxy_substitution_test.go` with the package boilerplate, namespace constant, and per-test-run sentinel constant:

```go
//go:build e2e

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tjorri/paddock/test/utils"
)

var _ = Describe("proxy MITM substitution (hermetic)", Ordered, func() {
	const (
		ns           = "paddock-test-substitution"
		listenerHost = "probe-listener.paddock-test-substitution.svc.cluster.local"
	)
	var sentinel string

	BeforeAll(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Per-run random sentinel ensures the assertion can't match
		// leftover state from a previous run.
		buf := make([]byte, 8)
		_, err := rand.Read(buf)
		Expect(err).NotTo(HaveOccurred())
		sentinel = "PADDOCK-E2E-SENTINEL-" + hex.EncodeToString(buf)

		// 1. Generate CA + leaf for the listener's Service hostname.
		caPEM, leafPEM, leafKeyPEM, err := utils.GenerateCAAndLeaf(listenerHost)
		Expect(err).NotTo(HaveOccurred())

		// 2. Create the test namespace.
		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", ns, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "create", "ns", ns))
		Expect(err).NotTo(HaveOccurred(), "create ns %s", ns)

		// 3. Listener TLS Secret.
		applyManifest(ctx, fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: probe-listener-tls
  namespace: %s
type: kubernetes.io/tls
stringData:
  tls.crt: |
%s
  tls.key: |
%s`, ns, indent4(string(leafPEM)), indent4(string(leafKeyPEM))))

		// 4. Overwrite the suite-level dummy ConfigMap with the test CA
		//    so the per-run proxy trusts the listener's leaf.
		applyManifest(ctx, fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: paddock-proxy-upstream-cas
  namespace: paddock-system
data:
  bundle.pem: |
%s`, indent4(string(caPEM))))

		// 5. Listener Deployment + Service running mendhak/http-https-echo.
		applyManifest(ctx, fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: probe-listener
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels: {app: probe-listener}
  template:
    metadata:
      labels: {app: probe-listener}
    spec:
      containers:
        - name: echo
          image: mendhak/http-https-echo:34
          env:
            - name: HTTPS_PORT
              value: "443"
            - name: HTTP_PORT
              value: "8080"
          volumeMounts:
            - name: tls
              mountPath: /app/cert.pem
              subPath: tls.crt
              readOnly: true
            - name: tls
              mountPath: /app/key.pem
              subPath: tls.key
              readOnly: true
          ports:
            - {containerPort: 443, name: https}
      volumes:
        - name: tls
          secret:
            secretName: probe-listener-tls
---
apiVersion: v1
kind: Service
metadata:
  name: probe-listener
  namespace: %[1]s
spec:
  selector: {app: probe-listener}
  ports:
    - {port: 443, targetPort: 443, name: https}`, ns))

		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "-n", ns,
			"rollout", "status", "deployment/probe-listener", "--timeout=120s"))
		Expect(err).NotTo(HaveOccurred(), "listener rollout")

		// 6. Test Secret carrying the sentinel value.
		applyManifest(ctx, fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: probe-secret
  namespace: %s
stringData:
  token: %q`, ns, sentinel))

		// 7. BrokerPolicy.
		applyManifest(ctx, fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: probe-policy
  namespace: %s
spec:
  appliesToTemplates: ["probe-curl"]
  grants:
    credentials:
      - name: PROBE_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef: {name: probe-secret, key: token}
          deliveryMode:
            proxyInjected:
              hosts: [%q]
              header: {name: Authorization, valuePrefix: "Bearer "}
    egress:
      - host: %q
        ports: [443]`, ns, listenerHost, listenerHost))

		// 8. ClusterHarnessTemplate.
		applyManifest(ctx, fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata:
  name: probe-curl
spec:
  harness: probe-curl
  image: curlimages/curl:8.10.1
  command: ["sh", "-c"]
  args:
    - |
      curl -sS -H "Authorization: Bearer $PROBE_TOKEN" https://%s/anything
  requires:
    credentials:
      - name: PROBE_TOKEN
    egress:
      - host: %q
        ports: [443]
  defaults:
    timeout: 90s
  workspace:
    required: true
    mountPath: /workspace`, listenerHost, listenerHost))
	})

	AfterAll(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", ns, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl", "delete",
			"clusterharnesstemplate", "probe-curl", "--ignore-not-found"))

		// Restore the suite-level dummy ConfigMap so other Describes
		// that follow this one don't see the test CA in their proxy
		// Pods. (Idempotent — re-applies the dummy.)
		dummyCA, _, _, err := utils.GenerateCAAndLeaf("paddock-e2e-dummy.invalid")
		Expect(err).NotTo(HaveOccurred())
		applyManifest(ctx, fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: paddock-proxy-upstream-cas
  namespace: paddock-system
data:
  bundle.pem: |
%s`, indent4(string(dummyCA))))
	})

	It("substitutes the surrogate bearer for the real secret value at the proxy", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		runName := "substitute-probe"
		applyManifest(ctx, fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: probe-curl
    kind: ClusterHarnessTemplate
  prompt: "probe-substitution"`, runName, ns))

		Eventually(func() string {
			return runPhase(ctx, ns, runName)
		}, 4*time.Minute, 5*time.Second).Should(Equal("Succeeded"),
			"HarnessRun did not reach Succeeded; run state: %s", dumpRun(ctx, ns, runName))

		// Read the agent stdout — listener echoes the request as JSON.
		out := readRunOutput(ctx, ns, runName)
		var resp struct {
			Headers map[string]string `json:"headers"`
		}
		Expect(json.Unmarshal([]byte(extractJSON(out)), &resp)).To(Succeed(),
			"listener response was not valid JSON: %s", out)

		gotAuth := resp.Headers["Authorization"]
		wantAuth := "Bearer " + sentinel

		Expect(gotAuth).To(Equal(wantAuth),
			"listener received Authorization=%q, want %q — substitution did not fire (surrogate likely reached upstream verbatim)",
			gotAuth, wantAuth)
	})
})

// extractJSON returns the substring from out starting at the first '{'
// and ending at the matching '}'. mendhak/http-https-echo prints JSON
// preceded by some HTTP status banner; this trims to the JSON body.
func extractJSON(out string) string {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return out
	}
	return out[start : end+1]
}

// applyManifest pipes the YAML to `kubectl apply -f -` and fails the
// spec on non-zero exit.
func applyManifest(ctx context.Context, yaml string) {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "kubectl apply failed for: %s", yaml)
}
```

- [ ] **Step 2: Confirm dependencies**

The spec uses `runPhase`, `dumpRun`, and `readRunOutput`. Verify these helpers already exist in the e2e package — they're used by `hostile_test.go`. If `dumpRun` does not exist, replace its single use in the `Eventually(...).Should(Equal("Succeeded"), ...)` failure message with a simpler `kubectl get harnessrun -o yaml` shell-out, or omit the diagnostic argument.

Search: `grep -n 'func runPhase\|func readRunOutput\|func dumpRun' test/e2e/*.go`
- If `dumpRun` is missing, change the line to `}, 4*time.Minute, 5*time.Second).Should(Equal("Succeeded"))` (no failure-formatter arg).

- [ ] **Step 3: Build the e2e binary**

Run: `go test -tags=e2e -c -o /tmp/paddock-e2e ./test/e2e/`
Expected: compile succeeds.

- [ ] **Step 4: Run the new spec on `paddock-test-e2e` (or `paddock-dev`)**

Run: `KIND_CLUSTER=paddock-test-e2e FAIL_FAST=1 GINKGO_FOCUS="proxy MITM substitution" make test-e2e 2>&1 | tee /tmp/e2e-task6.log`
Expected: spec passes — listener received `Authorization: Bearer PADDOCK-E2E-SENTINEL-<hex>`.

If it fails:
- Inspect the run's pod logs (`kubectl logs -n paddock-test-substitution -l paddock.dev/run=substitute-probe --all-containers --tail=500`).
- If `Authorization` shows the `pdk-usersecret-*` surrogate, the broker derivation isn't firing — recheck Task 1.
- If the Pod failed to start with `MountVolume.SetUp failed`, the ConfigMap wasn't seeded — recheck Task 5.
- If `curl` returned a TLS error, the proxy didn't trust the listener's leaf — recheck Task 4 (CA gen) and Task 5 (ConfigMap overwrite in `BeforeAll`).

- [ ] **Step 5: Run the full e2e suite to confirm no other spec regressed**

Run: `KIND_CLUSTER=paddock-test-e2e make test-e2e 2>&1 | tee /tmp/e2e-full.log`
Expected: all specs pass.

- [ ] **Step 6: Commit**

```bash
git add test/e2e/proxy_substitution_test.go
git commit -m "test(e2e): hermetic regression for proxy MITM substitution (#83)

Deploys an in-cluster TLS echo listener with a CA-signed cert (CA
generated in-process via test/utils.GenerateCAAndLeaf, projected into
the proxy's trust bundle via the suite-level paddock-proxy-upstream-cas
ConfigMap), runs a HarnessRun with a UserSuppliedSecret + ProxyInjected
grant whose surrogate bearer the proxy must swap for a per-test-run
random sentinel, and asserts the listener received the substituted
real value — not the surrogate.

Closes the e2e coverage gap documented at hostile_test.go:740-742 that
allowed #83 to ship undetected for an entire release cycle.

Refs #83"
```

---

## Cleanup

- [ ] **Final step: Verify the issue's repro on the fix branch**

The probe fixture from issue #83 (against `httpbin.org`) should now report the substituted real value:

Run:
```bash
kubectl apply -f /tmp/probe-substitute.yaml  # the original issue fixture
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded harnessrun/probe-run -n probe-sub --timeout=120s
POD=$(kubectl get pod -n probe-sub -l paddock.dev/run=probe-run -o name | head -1)
kubectl logs -n probe-sub "$POD" --all-containers | grep '"Authorization"'
```

Expected: `"Authorization": "Bearer PADDOCK-PROBE-SENTINEL-12345"` — the real secret value, not the `pdk-usersecret-*` surrogate.

This is a manual validation, not committed; it's the human-facing equivalent of the e2e regression test.
