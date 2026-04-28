# ADR-0006: Git clone credentials for the Workspace seed Job

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.1+

## Context

`Workspace.spec.seed.repos[].credentialsSecretRef` lets the Workspace seed Job clone private repositories. The seed Job is the *only* process that needs these credentials — harness pods never see them — and until this ADR the field was declared on the CRD but ignored by the controller.

Two concerns shape the design:

- **Secrets must not land on the PVC.** The workspace PVC persists across runs; anything written there is visible to every subsequent harness pod. Credentials need to live on an ephemeral volume scoped to the seed pod.
- **git has two very different credential shapes.** For `https://` URLs it wants username + password (or a PAT in place of a password). For `ssh://` / scp-style `git@host:path` it wants a private key. A single Secret schema has to cover both.

## Decision

Each `WorkspaceGitSource` declares an optional `credentialsSecretRef` that names a Kubernetes Secret in the workspace's namespace. The Secret's key set signals the auth mode:

| URL scheme | Required Secret keys | How it's passed to git |
| --- | --- | --- |
| `https://` | `username`, `password` (PATs go in `password`) | `GIT_ASKPASS` points to a tiny helper script that echoes the matching key based on the prompt. |
| `ssh://`, `git@host:path` | `ssh-privatekey` | `GIT_SSH_COMMAND` passes `-i <key-path>` plus `StrictHostKeyChecking=accept-new` and a writable `UserKnownHostsFile`. |

Other properties:

- Secrets are mounted **read-only** into the seed init container at `/paddock/creds/<index>/`, with `defaultMode: 0o400`. Each repo's credentials live in their own directory; repos without a credentials ref get no mount at all.
- A pod-level emptyDir (tmpfs, `medium: Memory`) at `/paddock/scratch` holds the askpass helper, `$HOME`, and the ssh `known_hosts` file. It is not persisted and is scoped to the seed pod only.
- The seed Job stays non-root (`uid 65532`) and keeps its restricted-PSS container SecurityContext. The credential volume permissions work because `0o400` is readable by the process that mounted it.
- The PVC is mounted only at `/workspace`. At no point does the seed Job copy, log, or otherwise write credential material to the workspace.

Failure is loud: `backoffLimit: 0`, so a missing-key or bad-credentials failure surfaces on the first attempt with the init container's logs intact.

## Consequences

- Private-repo seeding works end-to-end without the broker (which is v0.4+). When the broker arrives, it synthesises short-lived Secrets matching the same schema; the Workspace controller doesn't change.
- Users have to know that `password` is the key for a PAT. Documented in the Go doc comment on `CredentialsSecretRef` and in `docs/internal/specs/0001-core-v0.1.md` §2.3.
- `StrictHostKeyChecking=accept-new` trusts the remote on first connection. For the tightly scoped seed pod this is acceptable; locked-down environments can pin a `known_hosts` via a future `knownHostsSecretRef` without changing today's contract.
- The tmpfs scratch mount means credential artefacts live only in the pod's memory; a `kubectl cp` from the PVC cannot exfiltrate them.

## Phase 2h update (2026-04-27)

`WorkspaceGitSource.URL` rejects userinfo at admission. Credentials must flow through `credentialsSecretRef` (static) or `brokerCredentialRef` (broker-leased). A URL of the form `https://user:token@host/repo` is no longer admitted; the controller also scrubs userinfo from the on-PVC `repos.json` and runs `git remote set-url origin <scrubbed>` post-clone for broker-backed repos as defence-in-depth (F-50, Theme 1).
