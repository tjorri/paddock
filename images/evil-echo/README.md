# paddock-evil-echo — hostile test harness

> ⚠️ **Do NOT use as a real harness.** This image deliberately attempts
> security-relevant operations to validate Paddock's defences. It exists
> only for adversarial E2E coverage.

## Purpose

`paddock-evil-echo` is the test-side counterpart to `paddock-echo`. It
runs as a HarnessRun's main container (or as a Workspace seed image) and
attempts a sequence of hostile actions specified by CLI flags. Each
action emits one line of structured JSON describing the attempt's
outcome. E2E tests parse the JSON and assert that Paddock's defences
denied the action.

## Flags

See `docs/security/2026-04-25-v0.4-test-gaps.md` §4 for the full
catalogue. As of v0.5.0, 15 flags are supported:

- `--bypass-proxy-env` — unset HTTPS_PROXY before subsequent flags.
- `--connect-raw-tcp <host:port>` — raw TCP dial.
- `--connect-ip-literal <ip:port>` — same, but flag name signals IP-literal test.
- `--connect-loopback <port>` — TCP dial to 127.0.0.1.
- `--read-secret-files <glob>` — list/stat files matching glob.
- `--read-pvc-git-config` — read `/workspace/.git/config` and `/workspace/.paddock/repos.json`.
- `--probe-broker <url>` — POST a synthetic bearer to the broker.
- `--probe-imds` — connect to `169.254.169.254:80` (cloud metadata service).
- `--probe-env-override` — report the values of HTTPS_PROXY etc.
- `--probe-provider-substitution-host <url>` — request to non-allowlisted host with a synthetic broker bearer.
- `--exfil-via-dns <host>` — DNS lookup to host.
- `--read-other-tenant-pvc <namespace>` — attempt cross-namespace PVC read.
- `--forge-ca-cert <fqdn>` — read per-run CA private key.
- `--flood-connect-raw-tcp <host:port>` — 50 sequential TCP dials (DoS probe).
- `--smuggle-headers <name=value>` — request with extra header through proxy.

## Build

```sh
make image-evil-echo
```

Image is tagged `paddock-evil-echo:dev`. Not pushed to public
registries by default.

## Output format

One JSON object per flag, one line per object:

```json
{"flag": "--connect-raw-tcp", "target": "10.0.0.1:443", "result": "denied", "error": "i/o timeout"}
```

`result` is one of `success`, `denied`, `error`, `skipped`. `success`
means the attack succeeded (Paddock's defence failed); E2E tests
typically assert `denied` or `error`.

## Exit code

Always 0 when at least one flag ran (regardless of attack success).
Success/failure is in the JSON, not the exit code, so a hostile run
that "successfully" fails is still detectable by the test harness.
