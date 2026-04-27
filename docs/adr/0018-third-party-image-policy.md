# ADR-0018: Third-party container image policy

- Status: Accepted
- Date: 2026-04-27
- Deciders: @tjorri
- Tracks: F-49 (Theme 1 — workspace seed surface security).

## Context

Paddock pulls a number of third-party container images at runtime
(seed Job's `alpine/git`) and at build time (Dockerfile base layers).
The threat-model framing in T-5 historically described "Paddock-authored"
images even where the shipped artefact is third-party. F-49 surfaced
the gap and prompts an explicit policy.

## Decision

Every third-party image referenced by Paddock — whether bundled into a
Paddock-built image as a base layer, or pulled directly at runtime —
must satisfy:

1. **Audited.** The image's manifest, labels, and base layers are
   reviewed before adoption. Subsequent version bumps are reviewed in
   the PR that updates the reference. Vendor advisories (e.g. alpine/git's
   GitHub releases) are tracked at the cadence noted below.

2. **Digest-pinned in source.** References live in code or Helm values
   as `image@sha256:<digest>`, not `image:tag`. Captures immutability
   so a force-pushed tag cannot silently substitute a different image.

3. **Operator override available.** Every direct-runtime third-party
   image has a manager flag and Helm value that lets operators point
   at an internal mirror (e.g. air-gapped clusters). The override path
   accepts arbitrary references; tag-only refs force
   `ImagePullPolicy: Always` and emit a startup warning.

4. **CI-scanned where bundled.** First-party Paddock images that
   include a third-party base layer are scanned by Trivy in CI
   (`make trivy-images`). Direct-use third-party images (i.e. images
   pulled at runtime by Paddock-managed Pods, where Paddock is not the
   image author) rely on the vendor's advisory feed plus the audit
   cadence stated below.

5. **Audit cadence.** Each direct-use third-party image has an entry
   in the table at the bottom of this ADR with a "next review" date.
   Reviews update the digest pin against the latest released vendor
   tag and re-audit the manifest.

## Initial sweep (2026-04-27)

Direct-runtime third-party images:

| Image | Use site | Pin shape | Next review |
|-------|----------|-----------|-------------|
| `alpine/git@sha256:d453f54c83320412aa89c391b076930bd8569bc1012285e8c68ce2d4435826a3` | Workspace seed Job (`internal/controller/workspace_seed_helpers.go`) | digest | 2026-07-27 |

Sweep target list at plan-writing time covered `images/*/Dockerfile`
base layers and every image string in `internal/controller/`. Tag-pinned
base layers found during the sweep are recorded as follow-up work
rather than blocking this initial policy adoption — they will be
digest-pinned in subsequent PRs as the affected images are next touched.

## Consequences

- New direct-runtime third-party images require a flag + Helm value +
  ADR row before merge.
- Bumping the alpine/git pin is a deliberate PR; tag drift is impossible.
- Air-gapped operators have a documented override path.
- A first-party `paddock-workspace-seed` image becomes optional rather
  than required — the policy makes third-party use safe.
