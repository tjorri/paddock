---
title: Proxy
description: The per-run egress chokepoint — MITMs TLS so the agent only ever holds Paddock-issued bearers.
---

The **proxy** is a per-run sidecar that intercepts every outbound HTTPS
connection from the agent container. It MITMs the TLS handshake using a CA
the broker mints just for that run, validates the destination against the
egress allowlist, and substitutes the agent's Paddock-issued bearer for the
real upstream credential at request time.

## Why MITM

Paddock's design centre is "the agent never sees a credential it could
exfiltrate". To deliver that, the platform issues the agent a *Paddock
bearer* — a short-lived token that means nothing outside the proxy. The
proxy holds the real upstream secret (an Anthropic API key, a GitHub App
token) and substitutes it just-in-time, after validating the request.

The agent can dump its environment, log every header, and copy every file
in `/tmp` — and the worst it leaks is a bearer that's already worthless.

## Two interception modes

- **Transparent mode** — `iptables-init` REDIRECT rules force every outbound
  TCP/443 packet to the proxy. The agent has no opt-out. Default.
- **Broker mode** — the agent must opt into the proxy by configuring its
  HTTPS_PROXY environment. Reserved for harnesses that genuinely cannot
  function under transparent interception (e.g., libraries that pin the
  system trust store).

The choice is per-template and is part of the operator's threat model. See
[the interception-mode cookbook](https://github.com/tjorri/paddock/blob/main/docs/cookbooks/interception-mode.md)
for the practical decision tree.

## Per-request enforcement

For every request the proxy:

1. Calls `ValidateEgress` on the broker — is this destination allowed?
2. Calls `SubstituteAuth` on the broker — what credential should I swap in?
3. Logs an `AuditEvent` — destination, outcome, redacted envelope.

If either broker call fails, the request fails closed. The agent gets a 502
from the proxy, not a quiet bypass.
