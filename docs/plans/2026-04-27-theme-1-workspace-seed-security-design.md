# Theme 1 — Workspace seed surface security (F-46..F-52) — design

- Owner: @tjorri
- Date: 2026-04-27
- Status: Design (spec). Plan to follow at `docs/plans/2026-04-27-theme-1-workspace-seed-security.md`.
- Tracks: GitHub issue #42.
- Predecessors: Phase 2c (audit fail-closed), 2d (Owns()), 2e (per-container PSS), 2f (per-run intermediate CA), 2g (substitute-auth hygiene + admission AuditEvents).
- Related: `docs/plans/2026-04-26-v0.4-security-recheck-roadmap.md` (Theme 1 section); `docs/security/2026-04-25-v0.4-audit-findings.md` (F-46–F-52 detail blocks).

## Context

The Workspace seed surface holds the largest concentration of Open security findings after the 2026-04-26 recheck (7 of 8 workspace findings). All seven share one code surface — the Workspace admission webhook plus `internal/controller/workspace_seed.go` — and the seed Pod's elevated trust bundle (broker-leased token, projected `paddock-broker`-audience SA token, MITM CA private key for `https://` repo clones). The recheck roadmap groups them as a single bundle on that basis.

Pre-1.0, breaking changes are accepted in place rather than flag-aliased (CLAUDE.md). This design takes the breaks the seven findings imply and documents them in CHANGELOG, rather than introducing migration shims.

## Goals

- Close F-46 through F-52 in a single PR, commit-decomposed per finding so each `feat(security)!:` Conventional Commit lands its own breaking-change marker for `release-please`.
- Reuse Phase 2 patterns wherever possible (per-container PSS envelope, cert-manager-driven Certificate, fail-closed audit, `Owns()` registration). No new architectural primitives.
- Articulate a written third-party-image policy as an ADR so future image choices reference a stated criterion rather than ad-hoc judgement.

## Non-goals

- Building a Paddock-authored `paddock-workspace-seed` image. Logged as a follow-up; this PR keeps `alpine/git` and tightens the trust around it.
- Theme 3 webhook tightening (BrokerPolicy / HarnessRun admission). Different code surface, different theme.
- F-44 (the run-Pod analogue of F-51's bug pattern). Tracked under Theme 6; the F-51 fix here is intended to share its shape so the Theme 6 fix is a copy-and-adapt.
- Operator-configurable seed deadline. Default 600 s is hard-coded for now; revisit only if a real user hits the cap.

## Open-question decisions (resolved at design time)

Three open questions were deferred from the roadmap memo. Decisions taken at design review on 2026-04-27:

1. **F-46 SSH scope.** Allow `ssh://user@host/repo` and scp-style `user@host:path` unconditionally (alongside `https://`). The seed proxy's substitute-auth path is HTTPS-only by design, so SSH lives outside the MITM trust model — a per-host allowlist on the admission side is rules-without-teeth. Host reachability is constrained by the per-seed-Pod NetworkPolicy.
2. **F-49 seed image source.** Keep `alpine/git` as the default, digest-pinned. Add a `--seed-image` manager flag + Helm value (`controller.seedImage`) so air-gapped operators can point at an internal mirror. A first-party `paddock-workspace-seed` image becomes a follow-up.
3. **F-52 audit identity.** Use `runRef.name=seed-<workspace>` (the proxy's `--run-name=seed-<ws>` flag populates the emitted AuditEvent's `spec.runRef.name`) and reuse the run-Pod's existing AuditEvent kinds (`egress-allow`, `egress-block`, etc.). Promote the `seed-` prefix from incidental to documented convention via a one-line note on `AuditEventSpec.RunRef`'s godoc + spec 0002.

## Architecture

Same files the issue calls out, plus a small refactor and four new docs:

- `internal/webhook/v1alpha1/workspace_webhook.go` — admission tightening (F-46 + F-50 admission half).
- `internal/controller/workspace_seed.go` — Pod-shape changes (F-47, F-48, F-49, F-50 controller half, F-52). Pure-helper functions split into a sibling `workspace_seed_helpers.go` so the file containing the Job-shaping logic stays under ~600 lines after the changes.
- `internal/controller/workspace_broker.go` — terminal failure for missing/empty source-Secret keys (F-51).
- `internal/controller/workspace_controller.go` — `Owns(&corev1.ServiceAccount{}, &rbacv1.Role{}, &rbacv1.RoleBinding{})` for the new per-Workspace seed RBAC bundle; reaction to the new terminal `BrokerCAMisconfigured` / `ProxyCAMisconfigured` reasons.
- `cmd/main.go` — `--seed-image` flag (F-49).
- `charts/paddock/values.yaml` + templates — `controller.seedImage` value (F-49).
- `docs/adr/0018-third-party-image-policy.md` — new ADR articulating the third-party image trust criteria (F-49).
- `docs/adr/0006-git-credentials.md` — trailing "Phase 2h update (2026-04-27)" section noting "URLs must not contain userinfo" (F-50).
- `docs/specs/0002-broker-proxy-v0.3.md` — one-line addition to the AuditEvent section: `runRef.name` values prefixed `seed-` denote workspace-seed-time decisions (F-52).
- `docs/security/threat-model.md` — T-5 row updated from "Paddock-authored seed image" framing to a citation of the new ADR.
- `CONTRIBUTING.md` — one-line pointer to the new ADR for image-choice decisions.
- `CHANGELOG.md` — owned by `release-please`; not edited manually. Conventional Commit messages provide the entries.

Pattern reuse from Phase 2:

- **Phase 2g webhook validators.** F-46 + F-50 add `validateRepoURL` alongside the existing `validateRepoPath`/`validateWorkspaceRepos` helpers; same `field.ErrorList` shape, no new admission machinery.
- **Phase 2e per-container envelope.** F-47/F-48 fold into the existing `seedPodSecurityContext` + `seedContainerSecurityContext` helpers — `automountServiceAccountToken: false` at the Pod level, deadlines on the Job + Pod, explicit per-container token projection only where needed (the proxy sidecar).
- **Phase 2f cert-manager Certificate.** F-51 fix fits inside the existing `ensureSeedProxyTLS` / `ensureSeedBrokerCA` paths — no new resource types.
- **Phase 2c fail-closed-on-audit-failure.** F-52 audit drops `--disable-audit` and inherits the run-Pod's `ClientAuditSink` behaviour as-is.
- **Phase 2d Owns().** A new per-Workspace `paddock-workspace-seed-<ws>` SA + Role + RoleBinding (for F-48 + F-52 together) get registered under the Workspace reconciler's `SetupWithManager` so mid-run mutation is detected.

## Components

### F-46 — URL scheme allowlist (admission + controller defence-in-depth)

- New helper `validateRepoURL(p *field.Path, raw string) field.ErrorList` in `workspace_webhook.go`, called per-repo from `validateWorkspaceRepos`.
- Allowlist: `https://`, `ssh://`, scp-style `user@host:path`. Rejects: `file://`, `git://`, `http://`, anything else (e.g. `data://`).
- Parsing via `net/url.Parse` for scheme detection, plus the existing `isSSHURLLocal` for scp-style.
- Distinct `field.Invalid` error messages so users know which scheme tripped.
- Defence-in-depth in `seedJobForWorkspace`: the controller calls the same `validateRepoURL` helper (lifted into a shared package or duplicated locally to avoid an import cycle — same pattern used today by `isSSHURL` / `isSSHURLLocal`). A non-allowlisted URL surfaces a permanent `SeedRejected` condition rather than rendering the Job spec.

### F-47 — Seed Job deadlines

- `seedJobForWorkspace` sets:
  - `Job.Spec.ActiveDeadlineSeconds: 600`
  - `Pod.Spec.ActiveDeadlineSeconds: 600`
  - `Pod.Spec.TerminationGracePeriodSeconds: 30`
  - `Job.Spec.TTLSecondsAfterFinished: 3600` (operability — completed seed Jobs auto-reap after 1 h).
- Named constant `seedActiveDeadlineSeconds = 600` with a one-line comment ("≈10× typical clone time; bounded well under the 3600 s broker-token TTL").
- Not configurable yet; revisit if a real user hits the cap. Pre-1.0, hard default.

### F-48 — Default-SA token off + dedicated SA

- Pod template gains `AutomountServiceAccountToken: ptr.To(false)` and `ServiceAccountName: paddock-workspace-seed-<ws>`.
- New per-Workspace `corev1.ServiceAccount` `paddock-workspace-seed-<ws>`. No automount on the SA either.
- New `rbacv1.Role` `paddock-workspace-seed-<ws>` with verbs `create` on `auditevents.paddock.dev` (for F-52); namespace-scoped to the Workspace's namespace.
- New `rbacv1.RoleBinding` binding the SA to the Role.
- Proxy sidecar gains an explicit `projected: serviceAccountToken` volume mounted at `/var/run/secrets/kubernetes.io/serviceaccount` so `rest.InClusterConfig` finds the K8s API token. alpine/git containers do *not* get this projection.
- The existing `paddock-broker`-audience projected token (volume `paddock-broker-token`) stays untouched on the proxy sidecar.
- All three new objects (SA, Role, RoleBinding) are owner-ref'd to the Workspace and registered via `Owns()` so mid-life mutation is caught.

### F-49 — Seed image hygiene

- `defaultSeedImage` becomes `alpine/git@sha256:<digest>` — the digest is captured from the current `:v2.52.0` manifest at plan-writing time, frozen in source.
- New manager flag `--seed-image=<ref>` in `cmd/main.go`; threaded through `WorkspaceReconciler.SeedImage` (which already exists, today documented "test-only").
- Helm value `controller.seedImage` plumbs to the same flag; default empty, falls back to the in-source digest pin.
- `ImagePullPolicy`:
  - Default (digest-pinned) → `IfNotPresent` is safe; digest is content-addressed.
  - Operator override with a tag-only ref → reconciler emits a startup-time `WARN third-party-image-policy: --seed-image is tag-pinned, not digest-pinned` and forces `ImagePullPolicy: Always`.
- New ADR `docs/adr/0018-third-party-image-policy.md`:
  - Criteria: audited (manifest + base layers reviewed against published vendor advisories); digest-pinned in source code, not just tag; operator override available; ImagePullPolicy=Always when override drops back to a tag-only ref; CI vulnerability-scanned where the image is bundled into a Paddock-built layer (Trivy on first-party images covers transitive base layers; direct-use third-party images like `alpine/git` rely on the vendor's advisory feed plus a stated audit cadence captured in the ADR).
  - Sweep: at plan-writing time, audit `images/*/Dockerfile` base layers, every image string in `internal/controller/`, and any test-helper images. Capture the audit result in the ADR's "Initial sweep" section.
- Threat model T-5 row updated from "Paddock-authored seed image" framing to "third-party seed image used under the policy in `docs/adr/0018-third-party-image-policy.md`".
- `CONTRIBUTING.md` gets a one-line pointer.

### F-50 — Userinfo in URL

- Webhook: `validateRepoURL` (the F-46 helper) also rejects URLs with userinfo on `https://` (`*url.URL.User != nil`). Distinct error message.
- Controller: `repoManifestJSON` scrubs userinfo before marshaling. Helper `scrubURLUserinfo(string) string` lives in `workspace_seed_helpers.go`.
- Seed init container: when broker-backed creds are in play, the post-clone shell snippet appends `&& git -C <target> remote set-url origin <scrubbed-URL>`. Wrapped inside the existing `sh -c …` so a clone failure does not trigger the rewrite (the `&&` chain short-circuits).
- Static-creds (`credentialsSecretRef`) path does not need the post-clone rewrite — those users were already directed to keep creds out of the URL, and userinfo is now rejected at admission.
- ADR-0006 trailing update: explicit "URLs must not contain userinfo" line.

### F-51 — Terminal failure for missing/empty source-Secret keys

- Distinguish *transient* from *terminal*:
  - **Transient.** Source Secret `IsNotFound`, or cert-manager Certificate Ready=Unknown / Ready=False with a transient reason (e.g. issuing-in-flight). Keep the current "Pending" condition + 10 s requeue.
  - **Terminal.** Source Secret found but key missing/empty (typo'd key, blanked by external actor), or cert-manager Certificate Ready=False with a permanent reason (`IssuerNotFound`, `IssuerNotReady` with a stable cert-manager status, or others to be enumerated at plan-writing time). New typed errors `errSourceCAMisconfigured`, `errProxyCertPermanentFailure` returned from `ensureSeedBrokerCA` / `ensureSeedProxyTLS`.
- Reconciler maps the terminal errors to `Seeded=False reason=BrokerCAMisconfigured` / `Seeded=False reason=ProxyCAMisconfigured`, no requeue, AuditEvent + Recorder warning event.
- AuditEvent kind: a new `ca-misconfigured` value is added to the `AuditKind` enum in `api/v1alpha1/auditevent_types.go` (CRD schema change — pre-1.0 in-place evolution per CLAUDE.md). `decision: denied`.
- Operator clears it by fixing the source Secret + bumping `metadata.generation` (or deleting the Workspace if pre-active).
- This shape is intended to be lifted by Theme 6's F-44 fix on the run-Pod side. Same code-shape, different reconciler.

### F-52 — Audit on seed proxy

- Drop `--disable-audit` from `buildSeedProxySidecar`'s args slice.
- `--run-name=seed-<ws>` and `--run-namespace=<ns>` already populate `ClientAuditSink.RunName` / `Namespace` correctly; the resulting AuditEvent's `spec.runRef.name` is `seed-<ws>`. No proxy-side code change beyond the flag removal.
- AuditEvent godoc on `AuditEventSpec.RunRef` (`api/v1alpha1/auditevent_types.go`) gets a one-line addition: "Names prefixed `seed-` denote a workspace-seed-time decision; the suffix is the Workspace name." Same line added to spec `docs/specs/0002-broker-proxy-v0.3.md`'s AuditEvent section.
- RBAC: covered by the F-48 Role (`auditevents.paddock.dev` `create`).
- Inherits Phase 2c fail-closed semantics for free — no new error-handling code.

## Data flow (under the new constraints)

1. User creates a Workspace with `spec.seed.repos`. **Admission** runs `validateWorkspaceRepos` → `validateRepoURL` per repo. `https://` w/ userinfo → reject (F-50). `file://` / `git://` / `http://` / unknown → reject (F-46). `https://` clean or `ssh://` / scp-style → accept.
2. **Reconciler** ensures PVC, then for broker-backed seeds:
   - per-Workspace cert-manager `Certificate` (`ensureSeedProxyTLS`)
   - `<ws>-broker-ca` Secret (`ensureSeedBrokerCA`)
   - `paddock-workspace-seed-<ws>` ServiceAccount + Role + RoleBinding (F-48 + F-52)
   All four are owner-ref'd to the Workspace.
3. **Seed Job** is constructed via `seedJobForWorkspace`. New shape: `AutomountServiceAccountToken: false`; `ServiceAccountName: paddock-workspace-seed-<ws>`; deadlines (Job + Pod + grace + TTL); digest-pinned image (or `--seed-image` override). Proxy sidecar: K8s-API projected token at `/var/run/secrets/kubernetes.io/serviceaccount`; `--disable-audit` removed.
4. **Seed init container** clones into `/workspace/<path>`. For broker-backed repos, after the clone: `git remote set-url origin <scrubbed-URL>` (F-50 defence-in-depth).
5. **Manifest container** writes `/workspace/.paddock/repos.json` from `repoManifestJSON`, with userinfo scrubbed from each `URL` (F-50 defence-in-depth).
6. **Proxy sidecar** emits `AuditEvent`s for every CONNECT decision and substitution outcome with `runName: seed-<ws>` (F-52). Failures fail-closed per Phase 2c.
7. **F-51 terminal failure path:** if source CA Secret has missing/empty key, reconciler flips Workspace `Seeded=False reason=BrokerCAMisconfigured` (terminal, not requeued indefinitely) and emits `auditevents.paddock.dev` + Recorder warning event.

## Error handling

- **Webhook rejection messages** are explicit per-cause:
  - `spec.seed.repos[0].url: Unsupported value: "git://github.com/foo/bar": only https:// and ssh:// schemes are accepted`
  - `spec.seed.repos[1].url: Invalid value: "https://x:y@github.com/foo/bar": URLs must not contain userinfo; use credentialsSecretRef or brokerCredentialRef`
- **F-51 transient vs terminal split.** Source Secret `IsNotFound` → keep current "Pending" condition + 10 s requeue. Source Secret found, key missing/empty → typed `errSourceCAMisconfigured`, condition flips to terminal `BrokerCAMisconfigured` / `ProxyCAMisconfigured`, no requeue, AuditEvent (`kind: ca-misconfigured`, `decision: denied`).
- **F-52 audit emission failure.** `ClientAuditSink.RecordEgress` already returns errors; the proxy already fail-closes on deny-path errors and log+counters on allow-path errors (Phase 2c contract). Seed inherits this.
- **Image-flag fat-finger.** `--seed-image` accepts any non-empty string. Tag-only ref → startup `WARN` + force `ImagePullPolicy: Always`. We do not block startup; operators with a private mirror serving a content-immutable tag may have valid reasons.
- **Hard break for `git://` / `http://` / `file://` users.** CHANGELOG entry under "Breaking" calls out the rejected schemes explicitly with a migration line ("switch to `https://` with `credentialsSecretRef` or `brokerCredentialRef`"). Pre-1.0 hard break; no flag-aliasing or grace period.

## Testing

Unit-level (per finding):

- **F-46 / F-50** — `workspace_webhook_test.go`: table tests for accepted URLs (`https://h/r`, `ssh://u@h/r`, `git@h:r`) and rejected URLs (`http://`, `git://`, `file://`, `https://u:p@h/r`). Closes TG-24 + TG-25.
- **F-47** — `workspace_seed_test.go`: assert `Job.Spec.ActiveDeadlineSeconds`, `Pod.Spec.ActiveDeadlineSeconds`, `Pod.Spec.TerminationGracePeriodSeconds`, and `Job.Spec.TTLSecondsAfterFinished` are set on the rendered Job. Closes TG-23.
- **F-48** — `workspace_seed_test.go`: `AutomountServiceAccountToken=false`; `ServiceAccountName` set; alpine/git containers have no token volume mount; proxy sidecar has the explicit projected-token mount at the standard path. New `workspace_seed_rbac_test.go`: assert SA + Role + RoleBinding rendering and `Owns()` registration. Closes TG-7 (seed variant).
- **F-49** — `workspace_seed_test.go`: default image is digest-pinned (regex `@sha256:[0-9a-f]{64}`); `--seed-image` flag override is honoured; tag-only override warns and forces `Always`. Closes TG-20.
- **F-50 controller half** — `workspace_seed_test.go`: `repoManifestJSON` scrubs userinfo; rendered seed init container includes `git remote set-url origin <scrubbed>` for broker-backed repos.
- **F-51** — `workspace_broker_test.go` (new test cases): empty `ca.crt` in source Secret → terminal `BrokerCAMisconfigured` condition + AuditEvent. Same for the proxy-TLS path with a permanently-failed cert-manager Certificate.
- **F-52** — `workspace_seed_test.go`: rendered proxy sidecar args do *not* contain `--disable-audit`. Sink integration covered by existing run-Pod proxy audit tests.

E2E:

- Existing seed-Pod e2e (PSS test, broker test) get a focused extension:
  - A Workspace pointing at `git://github.com/foo` is rejected by admission (no Pod ever runs).
  - A clean Workspace produces an AuditEvent stream tagged `runName=seed-<ws>` during clone.
- Skip a "hostile slow git host" e2e — the deadline is unit-asserted; e2e doesn't need its own slow-host scaffolding.

Doc tests:

- ADR-0006 update + new third-party-image-policy ADR build cleanly into the docs site (no new linting infra).

## Risks and trade-offs

- **F-46 hard break.** Users with `git://` / `http://` / `file://` repos cannot create a Workspace after this PR. Pre-1.0 cost is acceptable per CLAUDE.md; mitigated by an explicit CHANGELOG migration line and a precise webhook error message.
- **F-49 keeps third-party trust.** We tighten the trust (digest pin, override, ADR) but do not eliminate it. A future first-party image is logged as a follow-up; the policy ADR keeps the criteria stated rather than implicit.
- **F-51 terminal condition is sticky.** Once flipped to `BrokerCAMisconfigured`, the Workspace requires explicit operator action (fix the source Secret + bump generation, or recreate). This is the intended trade — the previous behaviour was indefinite silent looping.
- **F-52 audit volume.** Seed clones generate per-CONNECT AuditEvents. Volume is bounded (one Job per Workspace, ~seconds-long) and covered by existing AuditEvent retention.
- **`workspace_seed.go` split.** Pure-helper extraction is targeted at this file; not a free-form refactor sweep. Reviewers who prefer single-PR-single-purpose may push back. The split serves the goal (post-PR readability) and stays inside the `internal/controller/` package, so the diff is contained.

## Commit decomposition

One PR, one commit per finding (or per tight group), each as a `feat(security)!: ...` Conventional Commit so `release-please` picks per-finding breaking-change markers. Final shape decided at plan-writing time, but the rough order:

1. `refactor(controller): split workspace_seed.go helpers` — pre-factor; no behaviour change.
2. `feat(security)!: F-46 reject non-https/ssh seed repo URLs at admission` (admission half + controller defence-in-depth).
3. `feat(security)!: F-50 reject userinfo in seed repo URLs + scrub on PVC` (admission half + manifest scrub + post-clone remote set-url).
4. `feat(security): F-47 cap seed Job + Pod deadlines + TTL`.
5. `feat(security)!: F-48 disable default-SA automount + dedicated paddock-workspace-seed SA`.
6. `feat(security)!: F-49 digest-pin seed image + --seed-image flag + Helm value` (includes the new ADR + threat-model + CONTRIBUTING updates).
7. `feat(security): F-51 terminal failure for misconfigured source-Secret keys`.
8. `feat(security): F-52 enable audit on seed proxy + RBAC for paddock-workspace-seed`.
9. `docs(adr): note URL userinfo restriction in ADR-0006` (small, separable).

## Out-of-scope follow-ups

- **First-party `paddock-workspace-seed` image.** Track as its own issue once the third-party-image-policy ADR is in.
- **F-44 run-Pod analogue of F-51.** Theme 6.
- **Seed-image SBOM diff in CI.** Optional follow-up if the third-party-image-policy ADR's audit cadence proves manual.
- **Operator-configurable seed deadline.** Revisit only if a real user hits the 600 s cap.
