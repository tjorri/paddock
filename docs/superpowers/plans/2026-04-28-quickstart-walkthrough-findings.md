# Quickstart walkthrough — findings (2026-04-28)

Live notes from validating `docs/getting-started/quickstart.md` end-to-end.
Each finding records what was observed, where in the doc it surfaced, and
the proposed revision (if any).

## Resolution status (updated 2026-05-02 — after first revision pass)

| Finding | Status | Where landed |
| --- | --- | --- |
| **F1** kind-up's "next: tilt up" / quickstart "next: make images" mismatch | Open (script) | Doc now sets correct expectation in Step 1; script's tail message unchanged. Future `hack/kind-up.sh` cleanup. |
| **F2** Control-plane Ready timeout warning during kind-up | Documented | Step 1 now notes the warning is expected on first run. |
| **F3** Cilium installed but undocumented | Resolved | Step 1 names cert-manager + Cilium explicitly. |
| **F4** Internal phase tag in script output | Open (script) | Cosmetic; future `hack/kind-up.sh` cleanup. |
| **F5** `make images` builds `paddock-evil-echo` the quickstart never uses | Open (Makefile) | Future `make images-eval` split. Doc unchanged for now. |
| **F5a** `make images` vs `make docker-build IMG=…` split | Documented | Step 1 now explains why two targets, with a sidebar callout. |
| **F6** `kind load` 9-line shell `for` loop | **Resolved** | New `make kind-load` target replaces the loop. Step 1 uses it. |
| **F7** Helm chart `NOTES.txt` diverges from quickstart | **Resolved** | Chart `NOTES.txt` rewritten to point at the canonical quickstart instead of giving its own conflicting recipe. |
| **F8** No "right cluster?" guard before `helm install` | **Resolved** | New "Heads-up — make sure you're targeting the right cluster" preamble before Step 1. |
| **F9** Manual `kubectl rollout status` calls | **Resolved** | Step 2 uses `helm install --wait --timeout 5m`; explicit rollout calls dropped. |
| **F10** Controller-manager 1–2× restart on first install (cert-manager race) | **Documented** | Step 2 notes the race is expected and self-heals. Future init-container fix in chart. |
| **F11** `make cli` writes to `./bin/` but doesn't put `kubectl-paddock` on PATH | Documented | Step 3 still requires the manual `export PATH=…` but with explicit comment. Future `make install-cli` target via `go install`. |
| **F12** Step 3 ends at "Succeeded" without showing the prompt's result | **Resolved** | Step 3 now ends with `kubectl paddock events` + `logs --result` so the user closes the loop. |
| **F13** iptables-init's noisy 7× "Bad rule" stderr | Open (code) | `cmd/iptables-init/main.go` should suppress stderr on `-C` probes. Cosmetic. |
| **F14** Step 4 doesn't disclose that `claude-code` requires a pay-per-token API key | **Resolved** | Step 4's two-path table makes the cost story explicit, plus Path B now offers an OAuth-subscription alternative. Supersedes F14 fully. |
| **F15** Step 4 should present two credential paths (API key vs OAuth) | **Resolved** | Step 4 restructured into Path A (API key, proxy MITM) and Path B (OAuth subscription, no MITM), backed by the new `claude-code-oauth` template + policy samples. |
| **F16** Step 4 broken on Cilium-with-KPR | **Resolved** | Issue [#79](https://github.com/tjorri/paddock/issues/79) → fixed in [PR #82](https://github.com/tjorri/paddock/pull/82) (controller-side CNP variant + per-run NP loopback allow). |
| **F16a** `toEntities: kube-apiserver` not honored on Cilium 1.16.5 | **Resolved** | PR #82 uses `toEntities: [kube-apiserver, remote-node]` — `remote-node` is the load-bearing piece for host-network apiserver static pods. |
| **F16b** `iptables-init` REDIRECT silently bypassed on Cilium | **Resolved (refuted)** | Empirically refuted: REDIRECT works on Cilium. Real cause was the per-run NP not allowing loopback (`127.0.0.0/8`). Fixed in PR #82. The diagnostic narrative below predates that empirical work. |
| **S1** Quickstart conflates evaluator / contributor / operator paths | Open (IA) | Deliberate decision still pending. Current revision keeps the contributor-from-source path (existing audience); evaluator-from-GHCR + operator-install paths remain a future pass. |
| **(walkthrough-found, no F-number)** Issue #83 — InContainer-delivered credentials can't be used as `Authorization` bearer tokens | **Resolved** | Issue [#83](https://github.com/tjorri/paddock/issues/83) → fixed in [PR #84](https://github.com/tjorri/paddock/pull/84). Broker now derives `SubstituteAuth: true` from credential grants' `proxyInjected.hosts`; InContainer-only grants get `false` and the proxy doesn't MITM, so the agent's real `Authorization` reaches upstream untouched. |
| **F17** TUI command palette never rendered (state opens but overlay invisible) | **Resolved** | `internal/paddocktui/ui/view.go` only switched on `m.Modal`; `PaletteView` was never invoked. Fixed by branching on `m.Palette.Open()` before the modal switch + regression test. |
| **F18** harness-claude-code launcher: `claude: command not found` after upstream native install | **Resolved** | `bootstrap.sh` now installs to `$HOME/.local/bin/claude` and only warns about PATH. `images/harness-claude-code/run.sh` now `export`s `$HOME/.local/bin` onto `PATH` after install. |
| **F19** Interactive claude-code runs are structurally non-functional | **Resolved** | Redesign landed in PR #105. Harness CLI now runs in the agent container under `paddock-harness-supervisor`; adapter is a stream-json frame proxy across a UDS pair on `/paddock`. See [design](../specs/2026-05-02-interactive-adapter-as-proxy-design.md), [plan](2026-05-02-interactive-adapter-as-proxy.md), and [`docs/contributing/harness-authoring.md`](../../contributing/harness-authoring.md). |

The diagnostic detail below is preserved as a record of how each
finding was discovered, even where the eventual fix or framing
shifted from what was first hypothesised.

---


## Structural / IA finding (S1) — `quickstart.md` conflates three audiences

The current `quickstart.md` is implicitly serving three different
audiences with one document, and the friction we hit in Step 1
(F5a, F6, F1) is largely a symptom of that conflation.

The three paths a Paddock newcomer might actually want:

| Audience | Cluster | Images | Outcome |
| --- | --- | --- | --- |
| Evaluator (try it fast) | Kind | Pulled from GHCR | A run completes; I understand the model. |
| Contributor (inner loop) | Kind | Built locally, ideally Tilt-live-updated | Code change reflects in cluster fast. |
| Operator (deploy for real) | Real cluster | Pulled from GHCR (tagged release) | Paddock runs in my prod cluster, hardened. |

The current `quickstart.md` is doing all three jobs (mostly the
contributor path, accidentally), and `installation.md` is a
placeholder that should anchor the operator path.

### Proposed IA

- `getting-started/quickstart.md` → rewritten as the **evaluator**
  path: `make kind-up` (or equivalent), `helm install paddock
  paddock/paddock` from GHCR, submit a run, observe. Short.
- `getting-started/installation.md` → filled out as the **operator**
  path: real-cluster prerequisites (cert-manager hard requirement,
  Cilium / NetworkPolicy soft recommendation), hardening, RBAC,
  observability hookup, links to architecture/concepts.
- `contributing/development.md` (or wherever the existing dev
  contributor guide lives) → owns the **contributor** path: build
  from source, `tilt up`, the manual `kind load` fallback when Tilt
  isn't an option.

### Open question for IA decision

If `quickstart.md` defaults to pulled GHCR images, an evaluator who
hits a bug fixed on `main` has no in-quickstart escape hatch. Two
ways to address:

1. Frequent release tags or a moving `:edge` / `:main` image tag, so
   the GHCR path never lags `main` by much.
2. A short "if you want to evaluate `main` instead of a release"
   pointer at the bottom of the quickstart that drops the reader
   into the contributor guide.

Recommend deciding this before revising — it changes whether the
quickstart needs to know anything about local-image-builds at all.

---


## Step 1 — Local cluster + images

### F1. `make kind-up` ends with "next: tilt up", contradicting the quickstart
The `hack/kind-up.sh` script prints `next: tilt up` on completion, but the
quickstart's next instruction is `make images` (Tilt is only mentioned as
optional in the prerequisites). New users following the doc will be told
two different things.

**Proposed:** either (a) make the script's "next" hint context-aware /
neutral ("next: build images or run `tilt up`"), or (b) acknowledge the
script's output in the quickstart so users aren't surprised.

### F2. Control-plane Ready timeout warning during kind cluster bring-up
`kind create cluster` printed `✗ Waiting ≤ 1m0s for control-plane = Ready`
followed by `WARNING: Timed out waiting for Ready` — but the cluster
ultimately came up fine (Cilium and cert-manager both installed
successfully afterwards). Likely Cilium-CNI-init latency on cold start.

**Proposed:** worth a one-line note in the quickstart that this warning
is expected on first run and not fatal, OR investigate raising the
timeout in `hack/kind-up.sh` so it doesn't appear at all.

### F3. Cilium CNI is installed but never mentioned in the doc
`make kind-up` installs Cilium 1.16.5 as the cluster CNI. The quickstart
prerequisites and Step 1 don't mention this — readers may be surprised
to see Cilium show up, or wonder whether they need to pre-install it
(they don't; the script handles it).

**Proposed:** add a one-sentence note in Step 1 that `make kind-up` also
installs Cilium and cert-manager, so the reader has an accurate mental
model of what `make kind-up` produces.

### F4. Internal phase tag leaks into user output
`hack/kind-up.sh` prints `patching cert-manager
--cluster-resource-namespace=paddock-system (F-18 / Phase 2f)`. The
"F-18 / Phase 2f" reference is internal roadmap notation that doesn't
mean anything to a quickstart reader.

**Proposed:** strip the phase reference from the script's user-facing
output (or move it to a comment).

### F5a. `make images` and `make docker-build IMG=paddock-manager:dev` are two separate steps with no explanation

Asked why this is two commands. The answer is structural:
- `make docker-build` is the kubebuilder-scaffolded target that builds
  the *controller-manager* image from the project's root `Dockerfile`
  (the only consumer of that Dockerfile). It's parameterized by `IMG`.
- `make images` is a Paddock-added umbrella over per-component targets
  (`image-echo`, `image-broker`, `image-proxy`, `image-iptables-init`,
  …) that build from `images/<component>/Dockerfile`.

So nine images come from one target, one image (the manager) comes
from a differently-named target with a different invocation style.
That split is historical, not load-bearing — and it shows up in the
quickstart as an unexplained second command.

**Proposed (pick one):**
1. **Best:** add a `make images-all` (or extend `make images`) target
   that depends on both `docker-build IMG=paddock-manager:dev` and the
   existing per-component image targets, so the quickstart has one
   build command.
2. Alternatively, add a sentence to Step 1 of the quickstart explaining
   the split: "the controller-manager uses kubebuilder's standard
   `make docker-build` target; the reference harness/broker/proxy/
   sidecar images are built by `make images`."

### F5. `make images` builds `paddock-evil-echo:dev` that the quickstart never loads
`make images` builds nine images, including `paddock-evil-echo:dev`,
which is for security/red-team scenarios and is not referenced
anywhere in the quickstart's `kind load` loop. So users wait on an
extra build (a Go compile + Alpine pull) for an image they don't use.

**Proposed:** either (a) split the security/eval images out of
`make images` into a separate target (e.g. `make images-eval` or
`make images-security`), or (b) call out in the quickstart that
`paddock-evil-echo` is intentionally built but only used by the
security guides, so users aren't surprised when an unused image
shows up in their local docker image list.

## Step 2 — install controller + broker

### F7. Helm chart NOTES.txt diverges from quickstart.md guidance

After `helm install`, the chart's `NOTES.txt` printed a "what to do
next" block that disagrees with the quickstart in several ways:

| Topic | `quickstart.md` says | Chart `NOTES.txt` says |
| --- | --- | --- |
| Verify rollout | both `paddock-controller-manager` *and* `paddock-broker` | only `paddock-controller-manager` |
| Demo namespace | `default` (no `-n`) | `paddock-demo` |
| How to apply template | `kubectl apply -f config/samples/...yaml` (local repo) | `kubectl apply -f https://raw.githubusercontent.com/tjorri/paddock/main/config/samples/...yaml` |
| How to submit a run | `kubectl paddock submit -t echo-default --prompt "..." --name hello --wait` | raw `HarnessRun` YAML via `cat <<EOF \| kubectl apply -f -` |
| Where status/events live | `kubectl paddock status hello` (no namespace) | `kubectl paddock events hello -n paddock-demo` |

For someone following the quickstart who happens to read the chart's
NOTES at the same time, this is straight-up confusing — the doc and
the tool give two different recipes for the same outcome.

**Proposed:** pick one canonical demo flow (likely the CLI-based one
in the quickstart, since `kubectl paddock submit` is a documented UX
surface and the raw-YAML form skips the CLI entirely) and rewrite
the chart's `NOTES.txt` to match. Or: rewrite NOTES.txt to be
chart-mode neutral ("see https://...quickstart for the full
walkthrough") and only echo the verification commands.

## Step 3 — run echo pipeline end-to-end

## Step 4 — Claude Code with capability-scoped BrokerPolicy

### F16. **Quickstart Step 4 is broken on the default Kind+Cilium cluster** — known ADR-0013 issue not surfaced anywhere user-facing

This is the most important finding from the walkthrough so far.

**What happens.** A run against the `claude-code` (or our new
`claude-code-oauth`) template fails on a fresh `make kind-up` cluster.
The agent's pre-flight `curl https://downloads.claude.ai/` times
out at 10s; the collector also times out talking to the K8s API
(`10.96.0.1:443`); the proxy never logs any connection events. The
run hits `Failed` after ~1 minute.

**Root cause.** Documented in
`docs/contributing/adr/0013-proxy-interception-modes.md` (Phase 2d
update, line 49) and `docs/internal/security-audits/2026-04-25-v0.4-audit-findings.md:890`:

> Cilium does not enforce standard NetworkPolicy ipBlock rules
> against host-network destinations like the kube-apiserver static
> pod, even when the rule matches by Service ClusterIP. […] A proper
> Cilium fix uses CiliumNetworkPolicy with `toEntities: kube-apiserver`
> and is queued for a future phase.

The Phase 2b mitigation is *only* "templates with empty `requires`
skip NP emission entirely." This is why the echo template (Step 3)
works — no NP emitted. The claude-code template has non-empty
`requires`, NP is emitted, Cilium's ipBlock-on-host-network bug
silently denies traffic to the kube-apiserver and (likely) to other
internal targets the proxy depends on. The result is no connection
events at all — proxy looks like it's idle, agent times out
because nothing is forwarding its traffic.

**Why this is critical for the quickstart.** `make kind-up` installs
Cilium by default. **Every user** who follows the quickstart through
Step 4 on a fresh local cluster hits this. The quickstart doesn't
warn about it. The chart doesn't warn about it. There is no
diagnostic in the failure output that points at the Cilium ipBlock
issue — the user sees a CDN timeout and (correctly, from their
perspective) blames their network.

**Diagnostic confirmation (this walkthrough):**
- `kubectl run debug --image=alpine:3.22` in the same namespace
  reached `downloads.claude.ai:443` successfully (got HTTP 403, the
  expected response for the bare URL). Cluster networking is fine.
- Cilium config inspected:
  `kubectl -n kube-system get cm cilium-config -o jsonpath='{.data.kube-proxy-replacement}'`
  returns `true`.
- The per-run NetworkPolicy `demo-oauth-egress` was inspected and
  contains the expected ipBlock rules (TCP/443 to public IPs except
  RFC1918, TCP/443 to `10.96.0.1/32`, TCP/8443 to broker via pod
  selector). The pod-selector rules likely enforce; the ipBlock
  rules don't.

**Workaround for users today:**

```sh
make kind-down
KIND_NO_CNI=1 make kind-up
```

This skips Cilium and falls back to kindnet, which doesn't enforce
NetworkPolicy at all — so all NP rules are functionally allow-all.
The trade-off is losing the policy-enforcement story, which is part
of the security-isolation demo, but the run flow works end-to-end.

**Diagnostic update (this walkthrough, follow-up).** A hand-rolled
CiliumNetworkPolicy with `toEntities: [world, kube-apiserver]` plus
DNS + broker selectors was applied alongside the standard NP, then
both a labeled-debug-pod curl test and a real run were attempted.
Findings split the original F16 into two independent sub-issues:

#### F16a. `toEntities: kube-apiserver` is not honored on Cilium 1.16.5 + Kind

Labeled debug pod with our CNP applied still timed out connecting to
`10.96.0.1:443` (the kube-apiserver Service ClusterIP). This is the
issue ADR-0013 calls out for ipBlock; turns out the `kube-apiserver`
entity rule has the same gap — Cilium isn't classifying `10.96.0.1`
(which routes to a host-network static pod on the control-plane
node) as the `kube-apiserver` identity in this configuration.

**Likely fixes (need investigation; none are quickstart-friendly):**
- Set `policy-cidr-match-mode: nodes` in Cilium's config so ipBlock
  rules can match host-network destinations.
- Add the apiserver IP(s) explicitly via `toCIDR` in the per-run NP
  (resolved at controller startup; partially attempted in Phase 2d
  per ADR-0013 but documented as not sufficient on Cilium).
- Manually label the control-plane node and use a node-label-based
  rule.

This is a real engineering item, not a doc fix.

#### F16b. `iptables-init` REDIRECT rule is silently bypassed on Cilium

This is **new** — not mentioned in ADR-0013. The labeled-debug-pod
test confirmed the network *path* to `downloads.claude.ai:443` is
fine on Cilium with our CNP applied (HTTP 403 in 13ms — the CDN's
expected response for the bare URL). But the harness pod's agent,
which has the *same* labels and the *same* policies plus
`iptables-init`'s REDIRECT chain, times out connecting to the same
destination. The proxy (which is supposed to receive the
redirected traffic on `127.0.0.1:15001`) logs zero connection
events.

Most plausible cause: Cilium's BPF datapath in pod netns intercepts
`connect()` before the kernel's iptables nat OUTPUT chain, so the
REDIRECT rule installed by `iptables-init` is silently never
matched. This is consistent with `kube-proxy-replacement: true` and
explains why the existing iptables-init redirect mechanism is
unusable on Cilium clusters configured this way.

This affects every transparent-mode harness run on Cilium —
including ANY claude-code run, OAuth or API-key. It's the actual
root cause of why Step 4 fails, not the NetworkPolicy issue. The
ipBlock issue (F16a) blocks the collector independently.

**Likely fixes (need investigation):**
- Move the proxy interception out of pod-netns iptables and into a
  CNI-level mechanism (the `cni` mode ADR-0013 lists as deferred to
  v0.4+). Cilium can install the redirect in BPF directly via
  Cilium-specific extensions.
- Force cooperative mode (HTTPS_PROXY env var) on Cilium clusters
  as a fallback. This trades hostile-binary protection for
  reachability — already the documented trade-off in
  `cooperative` mode per ADR-0013, but currently the controller
  picks transparent whenever PSA allows NET_ADMIN, regardless of
  whether iptables redirect actually works on the CNI in use.

This is also a real engineering item.

**Updated proposed disposition:**
1. Doc fix immediately: warn in quickstart that Step 4 requires
   `KIND_NO_CNI=1`. This is the cheapest correct workaround given
   F16a + F16b both need engineering.
2. Track F16a and F16b as separate engineering items.
3. The S1 IA decision (quickstart audience) interacts strongly with
   F16b: if the quickstart targets evaluators using GHCR images,
   the same Cilium failure mode hits without the user having any
   debug context. Consider gating Step 4 on a CNI capability check.

**Proposed (in order of urgency):**

1. **Immediately (doc fix).** Add a prominent warning at the top of
   Step 4 with the workaround. *"Step 4 is currently blocked on
   clusters with Cilium installed (the default for `make kind-up`)
   due to a known ipBlock-vs-host-network issue (ADR-0013 §Phase 2d).
   To complete the walkthrough today, recreate your cluster with
   `KIND_NO_CNI=1 make kind-up`. This trades NetworkPolicy
   enforcement for a working run; track the Cilium fix at #N."*
2. **Short-term (UX).** Have the controller detect Cilium presence
   at admission and either (a) emit a CiliumNetworkPolicy alongside
   the standard NP, or (b) log a clear AuditEvent / status condition
   on the HarnessRun that says "NetworkPolicy emitted but Cilium
   ipBlock enforcement is unreliable; see ADR-0013." Either gives
   users a discoverable failure mode instead of an opaque CDN
   timeout.
3. **Proper fix.** Implement the CiliumNetworkPolicy with
   `toEntities: kube-apiserver` work that ADR-0013 names as queued.

This finding pairs with S1 (the quickstart IA): on a published-
release-from-GHCR Kind quickstart, this same blocker appears, so
the IA decision needs to consider whether the quickstart's "first
working run" depends on a stable per-run NetworkPolicy story.

### F15. Step 4 should present two credential paths: API-key (proxy MITM) and OAuth (UserSuppliedSecret + InContainer)

User suggested presenting two parallel options under Step 4, motivated
by wanting to use a Claude Max subscription rather than a pay-per-token
API key.

This is feasible with **no code changes** — the broker already
supports `UserSuppliedSecret` with `InContainer` delivery
(`internal/broker/providers/usersuppliedsecret.go:94`), which returns
the Secret value directly for env-var injection. The Claude Code CLI
honors `CLAUDE_CODE_OAUTH_TOKEN` (verify env var name against
current Claude Code docs) for OAuth-mode auth, so:

- **Path A — API key (proxy MITM):** existing flow. Template
  declares `requires.credentials: [ANTHROPIC_API_KEY]`, BrokerPolicy
  grants via `AnthropicAPIProvider`. Pod env holds an opaque Paddock
  bearer; proxy MITMs `api.anthropic.com` and swaps bearer →
  `x-api-key`. Real secret never reaches the harness.

- **Path B — OAuth token (UserSuppliedSecret + InContainer):**
  template declares `requires.credentials: [CLAUDE_CODE_OAUTH_TOKEN]`,
  BrokerPolicy grants via `UserSuppliedSecret` with
  `deliveryMode.inContainer.accepted=true` and a written `reason`. Pod
  env holds the real OAuth token in plaintext; Claude CLI uses
  OAuth-mode auth directly. No proxy substitution.

This isn't merely "API key vs subscription" — Path B exercises a
distinct Paddock feature worth demonstrating: **operator must
explicitly consent to plaintext-in-container delivery, with a
recorded reason that lands in AuditEvents**. That's a deliberate
guardrail, not an oversight. Showing both side by side teaches the
isolation story (Path A) and the audited-consent story (Path B) in
one chapter.

**What's needed to ship this:**

1. Add `config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code_oauth.yaml`
   — identical to existing `claude-code` template except
   `metadata.name: claude-code-oauth` and
   `requires.credentials: [CLAUDE_CODE_OAUTH_TOKEN]`.
   (Two templates, not one with alternatives, because admission requires
   an exact match between template requires and policy grants.)
2. Add a second sample `BrokerPolicy` wired for
   `UserSuppliedSecret` + `InContainer`, including the consent block
   with a written `reason`.
3. Restructure quickstart Step 4 to present both paths side-by-side
   with the trade-off articulated up front (proxy isolation vs
   audited plaintext-in-container).

**Caveats to note in the doc for Path B:**

- Claude Code CLI may attempt OAuth token refresh during long runs.
  If the refresh endpoint isn't on the egress allowlist, refreshes
  fail silently. The default `claude-code` template's egress
  allowlist may need extension for OAuth flows.
- Anyone with `kubectl exec` into the pod sees the real OAuth
  token; this is the entire point of `inContainer.reason` being
  a required field.

This finding supersedes F14's "use a small API credit" workaround
(F14 framing was correct but incomplete — the right answer isn't a
workaround, it's exposing the existing capability the broker already
has).

### F14. Quickstart doesn't disclose that Step 4 requires a pay-per-token Anthropic API key (not a Claude subscription)

The quickstart says:

> Secret backing the AnthropicAPI provider. The agent never sees this
> value — the proxy MITMs TLS and swaps the Paddock-issued bearer for
> the real x-api-key header at request time.

…and then has the user `kubectl create secret … --from-literal=api-key=sk-ant-...`. A user on Claude Pro / Max — likely a meaningful share of evaluators — will reach this step assuming "I have a Claude account, I should be able to use this," only to find out at secret-creation time that it requires an `sk-ant-…` API key from the Anthropic console, with **separate billing** from the subscription.

User feedback verbatim: "I am personally on a Claude Max subscription, so I would much rather use that than an actual API key here, which would cost me extra money."

Confirmed root cause:
- `internal/broker/providers/anthropic.go:247` substitutes `x-api-key`
  and explicitly removes `Authorization` — wired only for the
  REST-API-key flow.
- `docs/internal/specs/0003-broker-secret-injection-v0.4.md:34` lists
  "OAuth2 refresh-token dances" as a deferred provider kind.

So Claude subscription support isn't broken — it's not implemented.

**Proposed (doc-side, immediately actionable):**

1. Add a one-paragraph callout at the top of Step 4 that names this
   plainly: *"Step 4 calls Anthropic's REST API and needs a
   pay-per-token API key (`sk-ant-…`) from the Anthropic console.
   Claude Pro / Max subscriptions use OAuth and aren't supported by
   the current broker — see open issue #N. The prompt below costs
   roughly a fraction of a cent."*
2. Offer a "validate the policy machinery without making an API call"
   off-ramp: walk through scaffold + apply + `describe template`
   without the final `submit`. That exercises admission, BrokerPolicy
   matching, and the broker's policy-shortfall diagnostic — which is
   arguably the more interesting Paddock-specific behaviour anyway.

**Proposed (engineering-side, not a doc fix):**

3. Track an `AnthropicOAuth` (or `ClaudeSubscription`) provider as a
   roadmap item. Not trivial — needs OAuth refresh-token storage and
   a substitution that swaps `Authorization: Bearer <access-token>`
   instead of `x-api-key`. The architecture is ready for it (provider
   kinds are pluggable) but the feature itself is real engineering
   work.

### F12. Step 3 ends at "Succeeded" without ever showing the prompt's actual result

After `kubectl paddock submit ... --wait`, the user sees:

```
1:33PM  Pending    Pending
1:34PM  Running    Completed=InProgress
1:34PM  Succeeded  PodReady=Completed
```

…and that's it. The whole point of running an LLM/agent demo is to
**see what the agent produced**, but Step 3 never tells the user
how to look at the result. The observability commands
(`kubectl paddock logs hello --result`, `kubectl paddock events
hello`) are listed in Step 5, by which time a confused user has
already wondered for a while whether the demo actually did
anything.

User feedback verbatim: "as a user I see that something succeeded,
but for demo purposes, I don't understand what this echo-default
was, what my prompt did, or anything else. Should I expect to see
something?"

**Proposed (in order of effort):**

1. **Cheapest:** add a one-line follow-up to Step 3:
   ```sh
   kubectl paddock events hello       # see the four PaddockEvents
   kubectl paddock logs hello --result # see the structured result
   ```
   This makes the demo close the loop in one step instead of two.
2. **Better:** have `kubectl paddock submit ... --wait` print the
   final `Result` PaddockEvent (or the contents of `result.json`)
   when the run succeeds, so `--wait` is "wait *and* show me the
   answer" rather than "wait silently then exit." For an interactive
   demo flow, that's almost certainly what the user wants.
3. **Also:** Step 3's intro could briefly explain what `echo-default`
   *is* (a no-LLM template that echoes the prompt back through the
   adapter to demonstrate the full pipeline shape), so the user has
   accurate expectations going in.

### F13. iptables-init pod logs print 7× scary-looking "Bad rule" stderr lines on every run

`kubectl logs` on a HarnessRun's pod shows:

```
iptables-init iptables: Bad rule (does a matching rule exist in that chain?).
iptables-init iptables: Bad rule (does a matching rule exist in that chain?).
iptables-init iptables: Bad rule (does a matching rule exist in that chain?).
… (7 total)
iptables-init iptables-init: installed REDIRECT chain "PADDOCK_OUTPUT" …
```

Confirmed cause: `cmd/iptables-init/main.go:195` (`appendIfMissing`)
calls `iptables -C <rule>` to check existence before appending.
When the rule is absent — which is the entire point of the check —
iptables exits 1 and prints `Bad rule (does a matching rule exist
in that chain?)` to stderr. The runner pipes stderr straight
through (`cmd.Stderr = os.Stderr` at line 216), so the noise
surfaces in pod logs on every healthy run.

This is purely cosmetic — the code is doing the right thing — but
it looks alarming when an operator is debugging a real issue and
glances at iptables-init logs.

**Proposed:** in `cmd/iptables-init/main.go`, suppress stderr for
the check half of `appendIfMissing`. Either:

- Split the runner contract so `-C` probes use a quiet runner that
  captures stderr and only prints it on unexpected exit codes, or
- Inside `appendIfMissing`, call iptables with `cmd.Stderr =
  io.Discard` for the `-C` invocation, then run the actual `-A`
  with normal stderr passthrough.

### F11. `make cli` writes to `./bin/` but doesn't put `kubectl-paddock` on PATH

User ran `kubectl paddock` between `make cli` and the manual
`export PATH="$PWD/bin:$PATH"` step and hit:

```
error: unknown command "paddock" for "kubectl"
```

The quickstart does have the export in the right place, but it's
listed as a separate step right after `make cli`, so a user who
skims and tries the next thing they recognize hits a confusing
plugin-not-found error.

`make cli`'s own output ends with `built bin/kubectl-paddock — place
on PATH to use as 'kubectl paddock'`, which is informative but
passive — the user still has to construct the export themselves.

**Proposed (in order of effort):**

1. **Cheapest:** make `make cli`'s success line print the exact
   export command to copy/paste, e.g.
   `built bin/kubectl-paddock. Add to PATH:  export PATH="$PWD/bin:$PATH"`
2. **Better:** add a `make install-cli` (or extend `make cli`) that
   does `go install ./cmd/kubectl-paddock`, dropping the binary into
   `$GOBIN` (typically already on PATH for Go users). The quickstart
   becomes one command instead of two.
3. **Best for non-Go users:** offer a Homebrew tap or release-binary
   install path so quickstart users don't need a Go toolchain at all.
   This pairs with the S1 IA decision (whether the quickstart targets
   evaluators using GHCR images or contributors building from source).

### F10. Controller-manager restarts 1–2× on first install due to cert-manager race

User observed `paddock-controller-manager` with two restarts and a
`FailedMount` event for `webhook-server-cert`:

```
Warning  FailedMount  ... MountVolume.SetUp failed for volume "webhook-certs" : secret "webhook-server-cert" not found
Warning  BackOff      ... Back-off restarting failed container manager
```

Confirmed cause: in `charts/paddock/templates/paddock.yaml`, the
controller pod mounts the Secret `webhook-server-cert`, which is
the *output* of a cert-manager `Certificate` (`paddock-serving-cert`)
that Helm creates in the same install. The pod schedules and tries
to mount before cert-manager has issued the cert and written the
Secret. Pod backs off, kubelet retries, eventually succeeds. No
init container exists in the chart to gate this.

This isn't unique to Paddock — it's the canonical cert-manager-vs-
consumer-pod race. Same race will show up for a real-cluster
operator reading the chart, not just on Kind.

**Proposed (in order of effort):**

1. **Document it now.** Add a one-line note in the quickstart that
   the controller may restart 1–2 times during initial install
   while cert-manager issues its certs; this is normal. Combined
   with F9 (`helm install --wait`), the user wouldn't see the
   restart noise at all — Helm would block until ready.
2. **Add an init container later.** Two viable shapes:
   - *Kubectl poll:* `until kubectl get secret webhook-server-cert;
     do sleep 1; done`. Reuses the controller's existing SA — RBAC
     already grants `get`/`list` on `secrets`, confirmed in
     `charts/paddock/templates/paddock.yaml`, so no new RBAC.
   - *File poll (no API access):* mount the same Secret volume in
     the init container with `optional: true`, then
     `until [ -f /tls/tls.crt ]; do sleep 1; done`. Avoids the API
     server entirely, so it can never drift on RBAC.
   Either adds 5–30s to first install, eliminates the spurious
   restarts, and looks much cleaner to operators eyeballing pod
   events. The file-poll variant is tighter for this case; the
   kubectl-poll variant is more common in cert-manager docs.

Not worth doing: Helm install-hooks gating the Deployment on the
Secret (more machinery, more brittle than option 2).

### F9. Manual `kubectl rollout status` calls can be folded into `helm install --wait`

After `helm install`, the quickstart has the user run:

```sh
kubectl -n paddock-system rollout status deploy/paddock-controller-manager
kubectl -n paddock-system rollout status deploy/paddock-broker
```

This is Helm-idiom from before `--wait` was widely adopted. The
chart has only one template (`paddock.yaml`) and no install hooks /
Jobs, so `helm install --wait --timeout 5m ...` would block until
both deployments are ready and replace both rollout-status calls.

**Proposed:** change the quickstart's `helm install` invocation to:

```sh
helm install paddock ./charts/paddock \
  --namespace paddock-system --create-namespace \
  --wait --timeout 5m \
  --set image.tag=dev \
  …
```

…and drop the two `rollout status` lines. If users want intermediate
progress, they can add `--debug`. Worth mentioning the `--timeout`
knob in a one-line note for users on slower hardware.

### F8. No "are you pointed at the right cluster?" guard before `helm install`

User reported: "I had a small scare because I was following these but
realized I didn't know what kubeconfig I was using." Helm + kubectl
will both happily target whatever the current context is, and the
quickstart never tells the user how to verify they're on the kind
cluster before running cluster-mutating commands.

`make kind-up` does print `Set kubectl context to "kind-paddock-dev"`
in passing, but that's far up-thread by the time Step 2 runs, and
the doc doesn't refer to it.

**Proposed:** add a one-liner verification at the top of Step 2:

```sh
# Sanity check — should print "kind-paddock-dev"
kubectl config current-context
```

Optionally pair with a "if it doesn't, run `kubectl config
use-context kind-paddock-dev`" hint.

This is cheap to add and prevents a real foot-gun (e.g. a user with
both a personal Kind cluster and a work cluster in their kubeconfig
could install Paddock into the wrong place).

### F6. The `kind load` step is a 9-line copy-paste shell `for` loop

Step 1 ends with a shell `for` loop that iterates over a hard-coded
list of nine image tags and calls `kind load docker-image --name
paddock-dev "$img"` on each. This is exactly the kind of plumbing a
quickstart should hide — a first-time evaluator is now writing shell
loops to make the basic "get to a runnable cluster" path work, and
the image list is duplicated between the loop and `make images` /
`make docker-build` (so it can drift).

There is currently no `kind-load` Makefile target — confirmed via
grep. The Tiltfile handles loading natively for the inner dev loop,
but the quickstart deliberately bypasses Tilt.

**Proposed (in increasing order of impact):**
1. Add a `make kind-load` target that iterates the same image list
   `make images` + the manager build produce, and calls
   `kind load docker-image --name $(KIND_CLUSTER) ...`. Quickstart
   becomes a single line: `make kind-load`.
2. Combine with F5a: an umbrella `make dev-up` (or similar) that
   chains `kind-up` → all builds → `kind-load`, so Step 1 collapses
   from four commands to one.
3. Or, if Tilt is the intended happy-path for first-time users on
   Kind, restructure Step 1 around `tilt up` and let users opt out
   manually if they don't want the inner-loop tooling. (This would
   also resolve the `next: tilt up` mismatch noted in F1.)

The first option is low-risk and would already eliminate the
copy-paste shell loop.

## paddock-tui walkthrough (added 2026-05-02)

Walkthrough goal: validate the "interactive harness from the TUI"
flow end-to-end against a `claude-code` template, BrokerPolicy, and
Workspace. Three findings surfaced, in escalating severity.

### F17. Command palette opens but is never drawn

**Symptom.** `Ctrl-K` and `:` (the documented openers per
`docs/guides/claude-code-tui-quickstart.md:280`) appeared to do
nothing. Closer inspection showed the TUI *did* enter a state that
required `Esc` to leave — i.e., the palette state had flipped open,
keys were being claimed by `handlePaletteKey`, but the overlay was
invisible.

**Root cause.** `internal/paddocktui/ui/view.go` switched on
`m.Modal` and rendered `NewSessionModalView` /
`EndSessionModalView` / `HelpModalView` accordingly, but the
analogous `PaletteView` was never invoked. The palette renderer in
`internal/paddocktui/ui/palette.go` was complete and tested in
isolation; only the call site was missing.

**Fix.** Add a `m.Palette.Open()` branch in `View()` that returns
`overlay(composed, PaletteView(m.Palette, width))` before the modal
switch. Palette and modals are mutually exclusive at the input
layer (see the gates in `app/update.go:handleKeyMsg`), so ordering
is safe. Regression test added in `view_test.go`.

### F18. `claude: command not found` after upstream native install

**Symptom.** `make image-claude-code` + run produced agent logs
ending with:

```
agent ✔ Claude Code successfully installed!
agent   Location: ~/.local/bin/claude
agent ⚠ Setup notes:
agent   ● Native installation exists but ~/.local/bin is not in your PATH.
…
agent /usr/local/bin/paddock-claude-code: line 108: claude: command not found
```

**Root cause.** Upstream `bootstrap.sh` from
`https://downloads.claude.ai/claude-code-releases/bootstrap.sh`
changed its layout: the native build now installs to
`$HOME/.local/bin/claude` and only **warns** when that directory
isn't on `PATH`. `images/harness-claude-code/run.sh` did not
add the new location to `PATH`, so the subsequent `claude "${args[@]}"`
invocation failed at exec resolution.

**Fix.** Append `export PATH="$HOME/.local/bin:$PATH"` immediately
after the bootstrap pipeline. Defensive — it's idempotent if a
future installer reverts to a PATH-default location.

### F19. Interactive claude-code runs are structurally non-functional

**Symptom.** A user-issued Interactive run against the
`claude-code` template completes with `Phase: Succeeded` but the
TUI's event timeline is empty. The agent's stdout shows a complete
stream-json conversation (init → assistant → result, with cost and
usage metrics), so claude itself ran fine; nothing reaches the
TUI's WebSocket subscriber.

**Two independent bugs combine to produce this:**

#### F19a. The adapter container can't `exec` claude

The `paddock-adapter-claude-code` image is built `FROM
gcr.io/distroless/static:nonroot` (`images/adapter-claude-code/Dockerfile:25`).
That image has no shell, no installer, and no `claude` binary —
yet `cmd/adapter-claude-code/persistent.go:99-117` and
`cmd/adapter-claude-code/per_prompt.go:88-104` both call
`exec.Command(d.claudeBin, ...)` directly inside the adapter.
`d.claudeBin` defaults to `"claude"`, resolved via PATH.

The controller's `buildAdapterContainer`
(`internal/controller/pod_spec.go:473-516`) doesn't help:

- it mounts only `paddock-shared` and the SA token — **the
  workspace PVC is not mounted into the adapter** (see comment at
  `pod_spec.go:472`);
- it sets `PADDOCK_RAW_PATH`, `PADDOCK_EVENTS_PATH`, and
  `PADDOCK_INTERACTIVE_MODE` — but **not** `PADDOCK_WORKSPACE`,
  `HOME`, or `PADDOCK_CLAUDE_BINARY`.

So the adapter has no way to find the claude binary the agent
container installs at runtime, and even if pointed at one, has
no compatible runtime to execute it.

Observable evidence:
```
adapter adapter-claude-code: ... start persistent agent: start: exec: "claude": executable file not found in $PATH
adapter adapter-claude-code: ... interactive mode "persistent-process" listening on [::]:8431
```

The persistent driver's `NewPersistentDriver` documents this state
as "broken" and falls back to returning errors from `SubmitPrompt`
and 503 from the stream handler — there is no recovery path.

#### F19b. `images/harness-claude-code/run.sh` doesn't branch on Interactive mode

`images/harness-echo/run.sh:48-50` ends with:

```sh
if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
  exec sleep infinity
fi
```

…which keeps the agent container alive after its initial event
flush so the adapter's loopback server can drive subsequent
prompts.

`images/harness-claude-code/run.sh` has no equivalent branch. It
unconditionally:

1. Reads `$PADDOCK_PROMPT_PATH`,
2. Runs a one-shot `claude -p ... < prompt`,
3. Writes a result.json,
4. Exits 0.

So even on an Interactive run, the agent container performs a
batch invocation against the `Spec.Prompt` field (which the TUI
populates with the user's first prompt) and then exits. The pod
terminates → the adapter's WebSocket listener is killed → no
follow-up prompts can be delivered → the run reports Succeeded
because the batch path returned cleanly.

#### Why F19a + F19b together explain the empty timeline

The user's prompt did execute (agent log carries the full
stream-json transcript). But:

- the **batch-mode adapter** (`run` in `cmd/adapter-claude-code/main.go`,
  the file-tailing path at `main.go:120-174`) only starts when
  `PADDOCK_INTERACTIVE_MODE` is empty. In an Interactive run it's
  set, so `runInteractive(mode)` takes over and the file tailer
  never runs.

- `runInteractive` constructs a `persistentDriver` whose
  `start()` immediately fails (F19a). The driver lives in a broken
  state; nothing tails the agent's `/paddock/raw/out`.

- The collector only publishes `events.jsonl` — but no one wrote
  it, because the only writer for that file in Interactive mode is
  `persistent.go:demux()` (or `per_prompt.go:drainStreamJSON`),
  both of which run inside the adapter and require a working
  claude subprocess.

Net effect: claude's output landed in `/paddock/raw/out` (run.sh
teed it there) but no component converted it to PaddockEvents, so
the run's event ring is empty and the TUI displays "Succeeded"
with no body.

#### Possible fix paths (not yet decided)

1. **Bake claude into the adapter image.** Switch the adapter's
   base from `distroless/static:nonroot` to a base that can run
   the upstream native binary (or pre-install via `bootstrap.sh`
   at build time). Pros: no race with the agent's runtime install,
   no workspace mount needed. Cons: doubles the adapter's image
   size, version drift between agent (runtime install of
   `PADDOCK_CLAUDE_CODE_VERSION`) and adapter (image-pinned).

2. **Mount the workspace PVC into the adapter and reuse the
   agent's install.** Add `workspace` volume + `HOME=<mount>/.home`
   + `PADDOCK_CLAUDE_BINARY=<mount>/.home/.local/bin/claude` to
   `buildAdapterContainer`; expand the adapter image base to one
   that can exec the binary. Need a startup wait loop in
   `NewPersistentDriver` because the adapter starts before the
   agent has installed claude.

3. **Move the persistent claude process into the agent
   container.** The adapter would only proxy stream-json frames
   between the broker WebSocket and the agent's stdin/stdout —
   crossed via a named pipe on the shared `/paddock` volume, or a
   unix socket bound there. This keeps the adapter image small,
   eliminates the version-drift question, and matches the existing
   "agent owns claude, sidecars only observe" design intent
   captured in `pod_spec.go:472`. Largest implementation surface
   of the three.

Option 3 is most aligned with the existing architectural
direction. Option 1 is the quickest path to a working Interactive
run if we accept image-bloat + version-drift trade-offs.

**Whichever path is chosen, F19b is required regardless** —
`images/harness-claude-code/run.sh` has to branch on
`PADDOCK_INTERACTIVE_MODE` and either `sleep infinity` (options 1
and 2) or take ownership of a long-lived claude process (option
3). Without that, the agent container exits after the batch run
and tears down the pod.

**Walkthrough impact.** This blocks the entire "interactive TUI"
chapter of the quickstart from working today. The TUI binary
itself is fine after F17; the cluster-side machinery isn't.
Pre-PR-merge sanity testing presumably exercised the interactive
flow against `harness-echo`, whose adapter
(`cmd/adapter-echo/main.go:68-82`) handles
`PADDOCK_INTERACTIVE_MODE` synthetically — every `/prompts`
request returns 202, appends a fabricated `PaddockEvent` to
`events.jsonl`, and relays a frame to the WebSocket. There is
**no subprocess** in the echo path, so the "adapter spawns a
binary" architecture was never exercised in CI by the only
harness that requires it. The `harness-echo` `run.sh` has the
correct `sleep infinity` branch
(`images/harness-echo/run.sh:48-50`); the claude variant has
neither piece in place.

#### Resolution (2026-05-02, PR #105)

Option 3 from the "possible fix paths" list above was chosen and
implemented on `feature/interactive-adapter-as-proxy`: the harness
CLI now runs in the **agent container** under a new
harness-agnostic binary, `paddock-harness-supervisor`
(`cmd/harness-supervisor/`), which listens on
`/paddock/agent-data.sock` (data) and `/paddock/agent-ctl.sock`
(control) and bridges them to the harness CLI's stdio. Both modes
(`per-prompt-process`, `persistent-process`) live in the
supervisor. The adapter sidecar (`cmd/adapter-claude-code/`) is
now a thin shim around `internal/adapter/proxy/` — a stream-json
frame proxy that dials the supervisor's UDS pair with backoff.
The deleted files cited above (`cmd/adapter-claude-code/{per_prompt,
persistent,server,driver}.go` and their tests) no longer exist;
the diagnostic narrative is preserved as a record of how the
F19 bug was discovered. The "adapter must not see workspace"
invariant (`internal/controller/pod_spec_test.go`) is preserved.
The webhook (`internal/webhook/v1alpha1/harnessrun_webhook.go`)
now validates `template.Spec.Interactive.Mode` against the
template's `paddock.dev/adapter-interactive-modes` annotation.

See:

- [`../specs/2026-05-02-interactive-adapter-as-proxy-design.md`](../specs/2026-05-02-interactive-adapter-as-proxy-design.md) — design.
- [`2026-05-02-interactive-adapter-as-proxy.md`](2026-05-02-interactive-adapter-as-proxy.md) — implementation plan.
- [`../../contributing/harness-authoring.md`](../../contributing/harness-authoring.md) — the new harness-image author contract.
