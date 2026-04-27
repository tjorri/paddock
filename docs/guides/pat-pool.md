# PATPool

Vertical provider for Personal Access Token pools — a static set of PATs
the broker rotates between. Use this for any gitforge (GitHub, GitLab,
Bitbucket, Gitea, etc.) where short-lived App tokens aren't an option.

## When to use this

- You're authenticating to a gitforge other than GitHub.com (e.g. GitLab,
  Bitbucket, Gitea, self-hosted Gitea).
- You're authenticating to GitHub.com without a GitHub App.
- You have multiple PATs to spread load across (e.g. avoid rate limits).

## When NOT to use this

- You have a GitHub App available — use [github-app.md](./github-app.md)
  instead. App installation tokens are short-lived, narrowly scoped, and
  can be revoked from the App settings without rotating individual PATs.
- For non-gitforge HTTP authentication, use
  [usersuppliedsecret.md](./usersuppliedsecret.md) with `basicAuth` or
  `header`.

## Setup

The `PATPool` provider takes a Secret containing one or more PATs (newline-
separated under the configured key). The broker hashes them, picks one
deterministically per (run, host) pair, and rotates if a request fails
auth.

```bash
# Multiple tokens, one per line
cat <<EOF > /tmp/gitlab-pats.txt
glpat-token-one
glpat-token-two
glpat-token-three
EOF

kubectl create secret generic gitlab-pats \
  --from-file=pool=/tmp/gitlab-pats.txt \
  -n my-team
```

Then declare the BrokerPolicy:

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: gitlab-policy
  namespace: my-team
spec:
  appliesToTemplates: ["gitlab-bot-*"]
  grants:
    credentials:
      - name: GITLAB_TOKEN
        provider:
          kind: PATPool
          secretRef:
            name: gitlab-pats
            key: pool
    egress:
      - host: "gitlab.example.com"
        ports: [443]
    gitRepos:
      - owner: my-group
        repo: my-project
        access: write
```

The agent container sees `GITLAB_TOKEN=pdk-…<random>`. The proxy
intercepts gitforge traffic and substitutes one of the pool's PATs into
the `Authorization` header (Basic auth with the token as the password,
matching the standard gitforge pattern).

For GitHub.com PAT use, the egress block lists `github.com` /
`api.github.com` instead. The provider doesn't hardcode hosts; you
declare them as for any cookbook.

## Complete worked example

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: gitlab-pats
  namespace: my-team
stringData:
  pool: |
    glpat-token-one
    glpat-token-two
    glpat-token-three
---
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: gitlab-bot-policy
  namespace: my-team
spec:
  appliesToTemplates: ["gitlab-bot-*"]
  grants:
    credentials:
      - name: GITLAB_TOKEN
        provider:
          kind: PATPool
          secretRef:
            name: gitlab-pats
            key: pool
    egress:
      - host: "gitlab.example.com"
        ports: [443]
    gitRepos:
      - owner: my-group
        repo: my-project
        access: write
```

## See also

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — decision tree.
- [github-app.md](./github-app.md) — for GitHub specifically when an App
  is available.
- [Spec 0003](../internal/specs/0003-broker-secret-injection-v0.4.md) — design intent.
