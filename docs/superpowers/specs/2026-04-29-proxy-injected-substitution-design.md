# ProxyInjected substitution: restore broker-driven MITM trigger

- Status: Design approved
- Owner: @tjorri
- Branch: `fix/proxy-injected-substitution`
- Implements fix for: [#83](https://github.com/tjorri/paddock/issues/83) — *"ProxyInjected substitution silently no-op since v0.4 — broker never triggers proxy MITM"*
- Predecessor (regression source): [#7](https://github.com/tjorri/paddock/pull/7) (v0.4 broker secret injection core)
- Successor artifact: implementation plan at `docs/superpowers/plans/2026-04-29-proxy-injected-substitution.md` (written next)

## Summary

The broker's `handleValidateEgress` (`internal/broker/server.go:415-418`) always returns `SubstituteAuth: false`. The proxy's MITM-and-substitute path is gated on `decision.SubstituteAuth` (`internal/proxy/mitm.go:147`), so MITM never engages, and every `ProxyInjected` credential grant — UserSuppliedSecret + proxyInjected, AnthropicAPI, GitHubApp, PATPool — is silently no-op. The agent's `pdk-*` surrogate bearer reaches upstream untouched.

The fix is a derivation: after the egress grant matches, walk credential grants on every matching `BrokerPolicy` and set `SubstituteAuth: true` when any grant's `deliveryMode.proxyInjected.hosts` covers the request host. This is the v0.4 commit-message-stated behavior (*"substitution is driven implicitly by the grant's deliveryMode.proxyInjected.hosts"*) that was never actually written.

The fix bundles two adjacent concerns: (1) a controller-side knob for projecting an extra upstream-CA bundle into the per-run proxy, so operators with private upstreams (and the new hermetic e2e) can configure trust without per-fork builds; (2) a hermetic e2e regression test that proves the broker → proxy → MITM → substitute → upstream wire actually works end-to-end — the missing coverage that allowed the regression to land in the first place.

## Resolved open questions

Brainstorming settled four questions before writing this spec:

### 1. Multi-policy walk: any-wins

When multiple `BrokerPolicies` match a run's template (via `appliesToTemplates`), **any** matching policy's credential grant whose `deliveryMode.proxyInjected.hosts` covers the request host triggers `SubstituteAuth: true`. Mirrors the v0.4 ethos that matching policies compose additively, mirrors `egressDiscovery`'s any-wins semantics, and matches what admission already enforces for the safety invariant (every substitution host must have a corresponding egress grant somewhere). Operators who split policies for governance reasons (infra grants egress, product team brings credentials) get the natural composition behavior.

### 2. Match granularity: host-only

Egress grants are `(host, ports[])`; credential grants' `proxyInjected.hosts` is host-only — no port. The derivation matches on host, not on `(host, port)`. An operator who declares `proxyInjected.hosts: ["api.example.com"]` gets MITM on every port the egress grant permits to that host. There is no real-world case today where you'd want to MITM port 443 of a host but not port 8443; if one materializes, a per-host port field can be added later. Pre-1.0 evolve in place.

### 3. Discovery-window interaction

`egressDiscovery` allow paths return `SubstituteAuth: false` regardless of credential grants. Discovery is *"log this destination so I can promote it to a grant later"*, not *"swap creds for it"*. Forcing MITM on discovery-allowed destinations would tangle two distinct concerns and surprise operators bootstrapping a new surface.

### 4. CA-trust mechanism: cluster-wide ConfigMap referenced by a controller flag

For the per-run proxy to verify upstream certificates from arbitrary in-cluster or private hosts, the controller gains a new flag — `--proxy-upstream-extra-cas-configmap=<name>`. When set, the controller mounts that ConfigMap (in the controller's own namespace, by convention paddock-system) into the per-run proxy Pod and passes `--upstream-ca-bundle=/etc/paddock-proxy/extra-cas/bundle.pem`. ConfigMap (not Secret) because CAs are public material; cluster-wide (not per-`BrokerPolicy`) to keep the v0.4 surface small. Per-policy granularity can be added later via additive `BrokerPolicy.spec.upstreamExtraCAs.configMapRef`.

## Non-goals

- Per-`BrokerPolicy` upstream CA bundle (decision 4 alternative).
- Per-host or per-grant port narrowing on substitution (decision 2 alternative).
- Restoring v0.3's explicit `egress[*].substituteAuth` flag — the v0.4 implicit derivation is the intended shape; this fix completes it.
- Changes to the proxy's MITM TLS-termination machinery, `applySubstitutionToRequest` strip semantics, or any existing substitute-auth handler logic. The bug is on the producer side; consumers stay unchanged.
- Reworking how `BrokerPolicy` admission cross-checks substitution hosts against egress grants. That check already exists and stays load-bearing.

## Architecture

### 1. Broker fix

**Where:** `internal/broker/server.go::handleValidateEgress`. After the egress-grant match succeeds (currently lines 415-418), call a new helper `anyProxyInjectedHostCovers(ctx, runNamespace, templateName, host)` and set `SubstituteAuth` to its return value before writing the response.

**Helper algorithm:**

1. List `BrokerPolicies` in `runNamespace`.
2. Filter by `policy.AppliesToTemplate(bp.Spec.AppliesToTemplates, templateName)` (same call already used by `matchEgressGrant`).
3. For each surviving policy, walk `bp.Spec.Grants.Credentials`. For each grant whose `Provider.DeliveryMode != nil && Provider.DeliveryMode.ProxyInjected != nil`, run `policy.AnyHostMatches(g.Provider.DeliveryMode.ProxyInjected.Hosts, host)`.
4. Return `true` on first match. Return `false` if no policy/grant matches.

**Wire response:**

```go
writeJSON(w, http.StatusOK, brokerapi.ValidateEgressResponse{
    Allowed:        true,
    MatchedPolicy:  policyName,
    SubstituteAuth: needsSubstitute,
})
```

Discovery-allow path stays untouched (`SubstituteAuth` left at `false`).

**Unit tests** in `internal/broker/server_test.go`:

- credential grant covers host → `true`
- only egress grant matches, no proxyInjected credential → `false`
- credential grant for a *different* host → `false`
- `*.foo.com` wildcard match → `true`
- multiple matching policies, only second one has the credential → `true`
- discovery-allow path → `false` regardless of credential grants
- credential grant has `inContainer` only (no `proxyInjected`) → `false`
- credential grant has neither (malformed) → `false`, no panic

**Backward compat:** none needed. The wire field `SubstituteAuth` already exists and the proxy already reads it. We are filling in the producer side that was forgotten.

### 2. Controller plumbing (upstream CA trust)

**New controller flag:** `--proxy-upstream-extra-cas-configmap=<name>` (default empty). When non-empty, names a ConfigMap in the controller's own namespace (paddock-system by convention).

**ConfigMap shape:**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: paddock-proxy-upstream-cas
  namespace: paddock-system
data:
  bundle.pem: |
    -----BEGIN CERTIFICATE-----
    ...one or more PEM CA certs...
    -----END CERTIFICATE-----
```

Single key: `bundle.pem`. Multiple PEM-encoded CA certs concatenated.

**Per-run proxy Pod plumbing** (inside the existing reconciler that builds the proxy container):

- `volume`: `name: upstream-extra-cas`, `configMap: { name: <flag-value> }`
- `volumeMount`: `mountPath: /etc/paddock-proxy/extra-cas`, `readOnly: true`
- proxy container `args`: append `--upstream-ca-bundle=/etc/paddock-proxy/extra-cas/bundle.pem`

Only added when the flag is non-empty.

**Behaviour matrix:**

| Flag | ConfigMap state | Outcome |
| --- | --- | --- |
| empty (default) | — | No volume, no mount, no arg. System trust store only. Production default for users not needing private upstreams. |
| set | missing | kubelet refuses to start the Pod with `MountVolume.SetUp failed`. Loud failure for an operator misconfiguration. |
| set | present, key absent | proxy errors at startup with `upstream-ca-bundle: no certs parsed` (existing error from `cmd/proxy/main.go:410`). Loud. |
| set | present, key valid | upstream verification trusts system roots ∪ CAs in `bundle.pem`. Existing `buildUpstreamTLSConfig` already implements union-with-system-roots semantics. |

**RBAC:** the controller already reads ConfigMaps in paddock-system for its own config (proxy MITM CA root, etc.). No new role bindings required.

**Why ConfigMap not Secret:** CA certificates are public material. cert-manager, kube-root-ca.crt, and istio all use ConfigMap for trust bundles. Reusing the established mental model.

**Why cluster-wide not per-policy:** keeps the v0.4 surface small. A real-world need for per-tenant or per-policy CA bundles is additive — `BrokerPolicy.spec.upstreamExtraCAs.configMapRef` can be added later without breaking changes.

### 3. E2E regression test

**Lives:** `test/e2e/proxy_substitution_test.go` (new file). Mirrors the issue probe shape but hermetic.

**Fixtures created in test setup (Ginkgo `BeforeAll`):**

1. **CA + leaf keypair generated in Go** via a new `test/utils/tls.go::GenerateCAAndLeaf(dnsName) (caPEM, leafCertPEM, leafKeyPEM []byte, err error)` helper using `crypto/x509`. No cert-manager dependency for the test itself — keeps it fast and self-contained.
2. **Secret `probe-listener-tls`** in the test namespace, holding the leaf cert + key.
3. **ConfigMap `paddock-proxy-upstream-cas`** in paddock-system with the test CA cert as `bundle.pem`. Overwrites the BeforeSuite-managed dummy bundle for the duration of this test.
4. **Listener Deployment + Service** running `mendhak/http-https-echo` with the leaf cert mounted. Service hostname `probe-listener.<ns>.svc.cluster.local` — matches the cert's SAN.
5. **BrokerPolicy + ClusterHarnessTemplate + Secret + HarnessRun** — same shape as the issue's `httpbin.org` probe but pointing at the in-cluster listener; substitution sentinel is per-test-run random.

**Assertion:** read the agent Pod's stdout after the run reaches Succeeded. The listener echoes the request as JSON in the response body, the agent's `curl` writes it to stdout. Parse and assert:

```
parsed.headers.Authorization == "Bearer PADDOCK-E2E-SENTINEL-<random>"
```

Per-test-run random sentinel ensures the assertion can't accidentally match leftover state.

**Install-time wiring:** the e2e suite's controller install path (via `make test-e2e` / `hack/kind-up.sh`) sets `--proxy-upstream-extra-cas-configmap=paddock-proxy-upstream-cas` on the controller. To avoid breaking other e2e specs that don't need extra CAs, `BeforeSuite` always creates `paddock-proxy-upstream-cas` in paddock-system with a benign self-signed dummy CA — so the proxy Pod always has a valid bundle to mount. The substitution test's `BeforeAll` overwrites the data; `AfterAll` restores the dummy.

**What this catches that unit tests don't:** the broker → proxy wire (does the broker actually emit `SubstituteAuth: true`? does the proxy actually MITM on it? does the upstream-CA trust plumbing work end-to-end?). This is the layer existing e2e bypasses entirely (`test/e2e/hostile_test.go:740-742`).

## Acceptance criteria

- [ ] `handleValidateEgress` derives `SubstituteAuth: true` from credential grants on matching `BrokerPolicies` whose `deliveryMode.proxyInjected.hosts` covers the request host. Wildcard matching reuses `policy.AnyHostMatches`.
- [ ] Unit tests in `internal/broker/server_test.go` cover the eight cases listed in §1.
- [ ] Controller honours `--proxy-upstream-extra-cas-configmap=<name>`. When set, mounts the named ConfigMap into the per-run proxy Pod and passes `--upstream-ca-bundle=/etc/paddock-proxy/extra-cas/bundle.pem`.
- [ ] When the flag is empty (default), no new volume / mount / arg is added — production behavior is byte-identical to today.
- [ ] Hermetic e2e test in `test/e2e/proxy_substitution_test.go` deploys an in-cluster TLS listener with a CA-managed cert and asserts the listener received the *substituted* real-secret value, not the surrogate. The `mendhak/http-https-echo` image is reused for the listener.
- [ ] `BeforeSuite` ensures a benign dummy `paddock-proxy-upstream-cas` ConfigMap exists in paddock-system; the substitution test overwrites it in `BeforeAll` and restores it in `AfterAll`.
- [ ] No new operator-facing CRD field. The intended behaviour is already documented at `api/v1alpha1/brokerpolicy_types.go:192`.
- [ ] Pre-merge `make test-e2e` passes.

## References

- `internal/broker/server.go:415-418` — the allow path to extend
- `internal/broker/api/types.go:201-216` — `ValidateEgressResponse.SubstituteAuth` field doc
- `internal/proxy/mitm.go:147` — the gate that reads the flag
- `internal/proxy/mitm.go:38-67` — `dialUpstreamTLS` and the `UpstreamTLSConfig` it clones from
- `cmd/proxy/main.go:120, 395-410` — existing `--upstream-ca-bundle` flag and its `buildUpstreamTLSConfig` implementation
- `api/v1alpha1/brokerpolicy_types.go:192` — CRD comment stating the intended derivation
- `internal/policy::AnyHostMatches` — wildcard-aware host match helper, reusable
- `test/e2e/hostile_test.go:740-742` — comment documenting why current e2e bypasses the proxy (gap this spec closes)
- PR #7 — v0.4 broker secret injection core (the refactor that introduced the regression)
- PR #11 — Plan D / `egressDiscovery` (precedent for "any-wins" multi-policy merge)
