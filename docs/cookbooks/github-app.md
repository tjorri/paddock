# GitHubApp

Vertical provider that mints short-lived installation tokens from a GitHub
App's private key. Use this for git authentication (cloning, pushing) and
GitHub REST/GraphQL API access. Compared to a long-lived PAT, App tokens
are scoped per installation, expire in ~1h, and can be revoked from the
GitHub App settings.

## When to use this

- You have a GitHub App installed on the repos your harness will access.
- You want short-lived, narrowly-scoped tokens minted on demand rather
  than a static PAT.
- You're authenticating to GitHub.com or GitHub Enterprise.

## When NOT to use this

- For other gitforges (GitLab, Bitbucket, Gitea) — use
  [pat-pool.md](./pat-pool.md) with the right host.
- If you don't have a GitHub App and don't want to set one up — use
  [pat-pool.md](./pat-pool.md) with a personal access token.

## Setup

The `GitHubApp` provider needs three things:

1. The App's numeric ID (visible in the App settings page).
2. The Installation ID for the org/repo set you're targeting (visible
   in the installation URL: `https://github.com/settings/installations/<id>`
   for personal accounts, or under the org's "Configure" page for org
   installs).
3. The App's RSA private key, stored in a Kubernetes Secret.

Generate the App key from the GitHub App settings page; download the
`.pem` file and create a Secret from it:

```bash
kubectl create secret generic github-app-key \
  --from-file=private-key=/path/to/github-app.private-key.pem \
  -n my-team
```

Then declare the BrokerPolicy:

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: github-policy
  namespace: my-team
spec:
  appliesToTemplates: ["repo-bot-*"]
  grants:
    credentials:
      - name: GITHUB_TOKEN
        provider:
          kind: GitHubApp
          appId: "123456"
          installationId: "987654"
          secretRef:
            name: github-app-key
            key: private-key
    egress:
      - host: "github.com"
        ports: [443]
      - host: "api.github.com"
        ports: [443]
    gitRepos:
      - owner: my-org
        repo: my-repo
        access: write
```

The agent container sees `GITHUB_TOKEN=ghs_…<short-lived-token>`. The
broker mints a fresh installation token on each issue request and rotates
before expiry.

For GitHub Enterprise, override the egress hosts to your Enterprise URL
(e.g. `github.example.com`, `api.github.example.com`).

## Complete worked example

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: github-app-key
  namespace: my-team
type: Opaque
data:
  # base64-encoded PEM private key from the GitHub App settings page
  private-key: |
    LS0tLS1CRUdJTi…
---
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: repo-bot-policy
  namespace: my-team
spec:
  appliesToTemplates: ["repo-bot-*"]
  grants:
    credentials:
      - name: GITHUB_TOKEN
        provider:
          kind: GitHubApp
          appId: "123456"
          installationId: "987654"
          secretRef:
            name: github-app-key
            key: private-key
    egress:
      - host: "github.com"
        ports: [443]
      - host: "api.github.com"
        ports: [443]
    gitRepos:
      - owner: my-org
        repo: my-repo
        access: write
```

## See also

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — decision tree.
- [pat-pool.md](./pat-pool.md) — for other gitforges or for environments
  without a GitHub App.
- [Spec 0003](../specs/0003-broker-secret-injection-v0.4.md) — design intent.
