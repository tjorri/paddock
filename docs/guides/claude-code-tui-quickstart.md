# Claude Code with `paddock-tui`: end-to-end quickstart

A practical walkthrough that takes a fresh namespace from zero to a working
interactive Claude Code session driven by [`paddock-tui`](../../cmd/paddock-tui).

By the end you'll have:

1. A `Secret` holding a `CLAUDE_CODE_OAUTH_TOKEN` value.
2. A namespaced `HarnessTemplate` that wraps the project's Claude Code image
   and declares the OAuth credential as its requirement.
3. A `BrokerPolicy` granting that credential via `UserSuppliedSecret` /
   `inContainer` delivery, plus the egress destinations Claude Code needs.
4. A reusable interactive session in `paddock-tui` that you can fire prompts
   into without re-typing config.

The whole flow takes about five minutes against a Paddock-equipped cluster.

## Prerequisites

- A Kubernetes cluster running the Paddock control plane (see
  [`getting-started/`](../getting-started/) if you need to install it).
- The `paddock-claude-code` and `paddock-adapter-claude-code` images
  available on the cluster — either pulled from your registry or
  side-loaded into Kind via `make image-claude-code image-adapter-claude-code`
  and `kind load docker-image …`.
- `kubectl` pointed at the cluster, with permission to create `Secret`,
  `HarnessTemplate`, and `BrokerPolicy` objects in your target namespace.
- The `paddock-tui` binary on your `PATH` (`make paddock-tui` produces
  `bin/paddock-tui`; copy it into `~/bin/` or equivalent).
- A Claude Code OAuth token from `claude setup-token` (the Claude Code CLI's
  long-lived OAuth flow). Treat this like any other API credential — it's
  user-scoped and gives the bearer interactive Claude.ai access.

We'll use the namespace `claude-code-demo`. Substitute your team's
namespace where it appears below.

```bash
kubectl create namespace claude-code-demo
```

## One-time `claude /login`

As of Paddock vX.Y, every agent's `$HOME` lives on the workspace PVC.
That means a `claude /login` you run inside an interactive session
persists in `~/.claude/` on the workspace and survives across runs —
no `UserSuppliedSecret` plumbing required for repeat use. Use the
`UserSuppliedSecret` path below only for first-run automation or for
non-interactive Batch chains where you can't drop into a TUI shell.

## Step 1 — Stash the OAuth token in a `Secret`

Place the token in a Kubernetes `Secret`. The key is `token` here; the
`BrokerPolicy` below references it explicitly.

```yaml
# claude-code-oauth-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: claude-code-oauth
  namespace: claude-code-demo
stringData:
  token: "sk-ant-oat01-…"
```

```bash
kubectl apply -f claude-code-oauth-secret.yaml
```

The Secret never leaves your cluster. Paddock reads it at run-creation
time and plumbs the value into the agent container's environment per
the policy below.

## Step 2 — Define a namespaced `HarnessTemplate`

The repo ships a `ClusterHarnessTemplate` named `claude-code` that uses
`ANTHROPIC_API_KEY` and the `AnthropicAPI` vertical provider
(see [`config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code.yaml`](../../config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code.yaml)).
For OAuth-token auth we declare a new namespaced template that requires
`CLAUDE_CODE_OAUTH_TOKEN` instead. Same image, same adapter, same egress —
just a different credential name.

```yaml
# claude-code-oauth-template.yaml
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: claude-code-oauth
  namespace: claude-code-demo
spec:
  harness: claude-code
  image: paddock-claude-code:dev
  command:
    - /usr/local/bin/paddock-claude-code
  eventAdapter:
    image: paddock-adapter-claude-code:dev
  requires:
    credentials:
      - name: CLAUDE_CODE_OAUTH_TOKEN
    egress:
      - host: api.anthropic.com
        ports: [443]
      # The harness installs the Claude Code CLI from Anthropic's downloads
      # CDN at startup; remove this line if you bake the CLI into the image.
      - host: downloads.claude.ai
        ports: [443]
  defaults:
    model: claude-sonnet-4-6
    timeout: 30m
    resources:
      requests:
        cpu: "100m"
        memory: "512Mi"
      limits:
        memory: "2Gi"
  workspace:
    required: true
    mountPath: /workspace
```

```bash
kubectl apply -f claude-code-oauth-template.yaml
```

The Claude Code CLI inside the container reads `CLAUDE_CODE_OAUTH_TOKEN`
out of its environment when the `ANTHROPIC_API_KEY` slot is empty, and
authenticates against Anthropic's OAuth endpoints. No code changes inside
the harness image are required — the credential name on the template
matches the env var the CLI looks up.

## Step 3 — Grant the credential and egress with a `BrokerPolicy`

The template declares **what** it needs; the policy decides **how** the
credential is delivered and **whether** the namespace is allowed to talk
to those hosts. We use `UserSuppliedSecret` with `inContainer` delivery —
the OAuth token has to reach the CLI process verbatim, and the Anthropic
endpoints validate it on the server side, so there's no upstream
substitution the proxy could do for us.

```yaml
# claude-code-oauth-policy.yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: claude-code-oauth
  namespace: claude-code-demo
spec:
  appliesToTemplates: ["claude-code-oauth"]
  grants:
    credentials:
      - name: CLAUDE_CODE_OAUTH_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: claude-code-oauth
            key: token
          deliveryMode:
            inContainer:
              accepted: true
              reason: "Claude Code's CLI authenticates to Anthropic OAuth endpoints with the token directly; the proxy cannot replicate the OAuth handshake on its behalf."
    egress:
      - host: api.anthropic.com
        ports: [443]
      - host: downloads.claude.ai
        ports: [443]
```

```bash
kubectl apply -f claude-code-oauth-policy.yaml
```

A few notes on the shape:

- `appliesToTemplates: ["claude-code-oauth"]` matches the `HarnessTemplate`
  by name. Glob patterns work too — `["claude-code-*"]` would cover both
  this template and the cluster `claude-code` template.
- `inContainer.reason` must be at least 20 characters and is captured in
  audit logs alongside every issued lease. Make it specific — "OAuth
  handshake the proxy cannot replicate" is the kind of explanation a
  future security reviewer wants to see in `git blame`.
- Egress is granted at the policy level, not the template level. The
  template's `requires.egress` is the *minimum* for admission; the policy
  must explicitly grant each host or the run is rejected at admission time.

## Step 4 — Verify the policy resolves cleanly

Before attaching the TUI, sanity-check that admission accepts the
combination by submitting a tiny `HarnessRun` directly. This catches
typos in the template ref, missing egress grants, or a misnamed Secret
key — all classes of error that would otherwise greet you halfway through
your first interactive prompt.

```bash
kubectl paddock submit \
  --template claude-code-oauth \
  --namespace claude-code-demo \
  --prompt "Reply with the single word OK and exit. Do not call any tools."
```

Watch the run reach `Succeeded`:

```bash
kubectl paddock events --namespace claude-code-demo --follow <run-name>
```

If the run reaches `Failed` with a `BrokerCredentialsReady=False` condition,
re-check that the Secret name and key match the policy's `secretRef`. If
admission rejects the run with a missing-grant message, the template's
`requires.egress` declared a host that the policy doesn't grant — add it
to the policy.

Once a one-shot run works, you're ready to drop into the interactive
flow.

## Step 5 — Use `paddock-tui`

Launch the TUI from the same kubeconfig context, scoped to the demo
namespace:

```bash
kubectl config set-context --current --namespace=claude-code-demo
paddock-tui
```

You'll see a sidebar on the left (initially empty, with a sticky
`[+ new session]` row) and the main pane on the right inviting you to
pick or create a session.

### Create a session

Press `n`. The new-session modal opens with four fields:

1. **Name** — type a memorable handle, e.g. `oauth-demo`. Tab to advance.
2. **Template** — Up/Down to cycle through the templates available in
   the namespace. Pick `claude-code-oauth`.
3. **Storage** — defaults to `10Gi`. Adjust if you expect the workspace
   to fill (Claude Code can produce hundreds of MB of generated artifacts
   on real repos).
4. **Seed repo** — optional. Leave empty for a blank workspace, or paste
   an `https://github.com/…` URL to clone it as the workspace's initial
   contents.

Press `Enter` on the seed-repo field to submit. The TUI watches the
`Workspace` reconcile and focuses the new session as soon as it appears
in the sidebar.

### Send your first prompt

Press `Tab` to move focus to the prompt input at the bottom. Type:

```
Read the workspace, list the files you see, and propose three small
improvements to the structure. Don't edit anything yet.
```

Press `Enter`. The TUI submits a `HarnessRun` against your session's
`Workspace`, opens an event tail, and starts streaming `PaddockEvents`
into the main pane. Each run is bracketed:

```
╭─ <run-name> · 14:22:11 ────────────────╮
│ > Read the workspace, list the files…  │
│ • read /workspace                       │
│ • read /workspace/README.md             │
│   I see three top-level files…          │
╰─ Succeeded · 47s ───────────────────────╯
```

While the run is in flight, you can keep typing prompts at the bottom —
they queue locally and submit one-by-one as the active run completes.

### Slash commands

The prompt input recognises a few `:`-prefixed commands. The most useful
day-to-day:

- `:cancel` — cancel the in-flight run (also bound to `Ctrl-X`).
- `:queue` — show queued prompts; cancel individual entries with `q`.
- `:edit` — open `$EDITOR` for a longer multi-line prompt.
- `:template <name>` — switch the session's default template for future
  prompts; persisted as an annotation so reattach restores the override.
- `:help` — full keybindings (`?` works too).

### Detach and reattach

Press `q` (outside the prompt) to quit. Your session, the Workspace, and
any in-flight `HarnessRun` keep running on the cluster — only the local
prompt queue is lost. Re-launch `paddock-tui` from any terminal and pick
the same session from the sidebar; the TUI loads the existing run history
and tails the latest in-flight run from where you left off.

### Multi-session

Press `n` again to create a second session against the same template.
The sidebar lists both; arrows or `j`/`k` navigate, `Enter` switches focus.
Each session has its own queue, its own active run, and its own scroll
state in the main pane.

### End the session

When you're done, focus the session in the sidebar and press `e`.
Confirm with `Enter`. The TUI deletes the labeled `Workspace`; the
controller drains any active run and reclaims the PVC. The OAuth Secret,
template, and policy stay in place for the next session.

You can also tear down via the non-TUI subcommand:

```bash
paddock-tui session end oauth-demo --yes
```

## See also

- [`usersuppliedsecret.md`](./usersuppliedsecret.md) — full reference for
  the `UserSuppliedSecret` provider and its four delivery patterns
  (header, queryParam, basicAuth, inContainer).
- [`anthropic-api.md`](./anthropic-api.md) — vertical-provider equivalent
  if you'd rather use an `ANTHROPIC_API_KEY` and have the proxy substitute
  the `x-api-key` header.
- [`picking-a-delivery-mode.md`](./picking-a-delivery-mode.md) — when to
  pick `inContainer` vs `proxyInjected` for a credential.
- [`bootstrapping-an-allowlist.md`](./bootstrapping-an-allowlist.md) — the
  iterate-and-deny loop for figuring out which egress hosts a new harness
  needs.
