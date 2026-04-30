# Workspaces and HOME

By default, Paddock points every harness agent's `HOME` environment
variable at a subdirectory inside the workspace PVC rather than at a
path baked into the container image. This means tool caches, shell
history, editor configs, and agent-level credentials written to `~`
survive across runs as long as the workspace exists — no per-run
reinstall required.

## What HOME maps to

The agent container receives:

```
HOME=<workspace.spec.workspace.mountPath>/.home/
```

When `workspace.spec.workspace.mountPath` is not set, the controller
uses the default mount of `/workspace`, so the effective HOME is:

```
/workspace/.home/
```

An init container (`paddock-home-init`) runs before the agent starts
and creates the directory if it does not yet exist:

```sh
mkdir -p /workspace/.home/ && chmod 0775 /workspace/.home/
```

This happens on every run — `mkdir -p` is idempotent, so the second
and subsequent runs skip the creation and pay only a small filesystem
stat cost.

The `PADDOCK_WORKSPACE` environment variable continues to point at the
mount root (`/workspace`). Do not confuse the two:

| Variable | Value | Contents |
|---|---|---|
| `PADDOCK_WORKSPACE` | `/workspace` | Checked-out repo, seed data, run outputs |
| `HOME` | `/workspace/.home/` | Agent's personal config and caches |

## Why this is the default

Each workspace PVC is long-lived — it outlives individual runs and
is shared by all runs that reference the same `Workspace` CR. Putting
`HOME` on the PVC gives three concrete payoffs:

**Tool installs persist.** Languages like Node.js, Python, and Rust
write user-level binaries and packages into `~/.local/bin`,
`~/.npm`, `~/.cargo`, etc. When the agent reinstalls tools on every
run, cold-start time grows in proportion to the tool surface. With
`HOME` on the PVC, the first run pays the install cost; every
subsequent run reuses the cached binaries directly.

**Agent caches persist.** Editors, language-server indexes, and
model-inference SDKs all maintain per-user caches under `~/.cache`.
These can amount to hundreds of megabytes after a warm run. Keeping
them on the PVC avoids redundant network fetches and re-index passes.

**Interactive setup is one-time.** Tools that require an interactive
login step — OAuth flows, API-key prompts, SSH host-key acceptance —
write their result to `~/.config` or a dotfile. With HOME on the PVC,
an operator or an initial seeded run can complete the one-time
setup, and all subsequent automated runs inherit that state without
repeating the authentication ceremony.

## What this changes for harness images

Images that populate `HOME`-relative paths at image-build time (e.g.,
`COPY .npmrc /root/.npmrc`) need a small adjustment. The agent's
actual `HOME` at runtime is `/workspace/.home/`, not `/root/` or
whatever the image's `ENV HOME` declares. Files baked into `/root/`
are still present, but the agent process reads `~/.npmrc` as
`/workspace/.home/.npmrc`.

The recommended pattern is to seed these files at container startup
rather than at image-build time. An init script can copy from a
well-known image path into `$HOME` on first run — or the workspace
seeding mechanism (`spec.workspace.seedFrom`) can deliver a pre-baked
`.home/` subdirectory as part of the initial workspace content.

Harness images that do not write anything to `HOME` are unaffected.

## Known limitation: cross-UID HOME stomping

All runs that share a workspace write into the same
`/workspace/.home/` directory tree. This is safe as long as all
agents run as the same UID. If two `HarnessTemplate` definitions
reference the same `Workspace` but pin different runtime UIDs (via
`spec.podTemplate.securityContext.runAsUser`), the second run may
be unable to read or write files laid down by the first — or,
worse, may silently shadow them with files owned by a different UID.

**How to recognize the failure.** The agent process reports
`Permission denied` when trying to open `~/.config/…` or
`~/.cache/…`, or tool installs appear to succeed but are absent on
the next run because the cache entry is owned by the other UID.

**When this is a problem.** The scenario arises most often when:

- A namespace has one workspace shared across multiple harness types
  whose images were built from different base images with different
  default users (e.g., `node` image uses UID 1000; a custom toolchain
  image uses UID 501).
- An operator pins an explicit `runAsUser` on one template but not
  the other, leaving them with different effective UIDs.

**It does not apply to** workspaces that serve only a single harness
type, or to multiple templates that all resolve to the same UID.

## Workarounds

Two approaches eliminate the cross-UID risk:

**Dedicate a workspace per harness type.** Create a separate
`Workspace` CR for each `HarnessTemplate`. The PVCs are independent,
so each harness type gets its own `/workspace/.home/` tree with no
ownership collisions. This is the simplest fix and works without any
image changes.

```yaml
# workspace per harness type
---
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: my-workspace-nodejs
  namespace: my-namespace
spec:
  storage:
    size: 10Gi
---
apiVersion: paddock.dev/v1alpha1
kind: Workspace
metadata:
  name: my-workspace-toolchain
  namespace: my-namespace
spec:
  storage:
    size: 10Gi
```

Each `HarnessRun` references the workspace appropriate for its
template via `spec.workspaceRef.name`.

**Pin all harness images to the same runtime UID.** If sharing a
workspace across harness types is intentional, ensure every image
runs as the same UID. The Dockerfile convention is:

```dockerfile
# Match whichever UID the other harness images use — pick one and
# apply it consistently across all images in the namespace.
RUN useradd -u 1000 -m agent
USER 1000
```

Combined with a matching `spec.podTemplate.securityContext.runAsUser:
1000` on each template (or relying on the image's declared user if
the orchestrator respects it), all files written to
`/workspace/.home/` will be owned by the same UID regardless of
which harness type created them.

## Related reading

- [`interactive-harnessruns.md`](./interactive-harnessruns.md) —
  long-lived multi-prompt runs that benefit most from persistent HOME
  state (tool reinstalls compound across many turns).
- [`claude-code-tui-quickstart.md`](./claude-code-tui-quickstart.md)
  — end-to-end walkthrough of a Claude Code session where persistent
  `HOME` means `claude /login` is done once per workspace.
- [`../superpowers/specs/2026-04-30-paddock-tui-interactive-design.md`](../superpowers/specs/2026-04-30-paddock-tui-interactive-design.md)
  — design spec (§3 / Resolved Q3) that established the HOME-from-PVC
  default and the single shared `.home/` path.
