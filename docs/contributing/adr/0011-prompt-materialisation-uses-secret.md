# ADR-0011: Prompt materialisation uses a Secret regardless of source

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.1+

## Context

A HarnessRun's prompt can arrive three ways:
- `spec.prompt` — inline.
- `spec.promptFrom.configMapKeyRef` — copied out of a user-managed ConfigMap.
- `spec.promptFrom.secretKeyRef` — copied out of a user-managed Secret.

In all three cases the run controller needs to materialise the bytes into an owned Kubernetes object so the agent Pod can mount them as a file at `/paddock/prompt/prompt.txt`. The original v0.1 implementation wrote them into an owned ConfigMap (`<run>-prompt`) regardless of the source. That shipped.

The problem: when the user sourced the prompt from a Secret, the controller silently copied the bytes into a ConfigMap. Anyone with `configmaps get` in the run's namespace — a far broader audience than `secrets get` — could read the prompt. This contradicts the user's evident intent when they chose a Secret in the first place, and it contradicts Paddock's "secure by default" principle (VISION.md).

Three fix options were considered:

- **Keep the status quo, document loudly.** Put a warning in the `spec.promptFrom.secretKeyRef` godoc. Rejected — the field still works, but the behaviour is surprising; documentation doesn't fix the blast-radius mismatch for users who don't read godoc.
- **Mount the user's source Secret directly as a volume.** Skip materialisation for Secret-sourced prompts. Rejected — requires branching volume generation by source (Secret vs. ConfigMap vs. inline), doubles the pod-spec test matrix, and breaks the clean "prompt is always at `$PADDOCK_PROMPT_PATH`" contract the agent relies on because the mounted file name is tied to the source object's key, not ours.
- **Materialise into an owned Secret in every case.** Single code path; uniform security posture; identical volume-mount semantics to a ConfigMap; the agent contract (`$PADDOCK_PROMPT_PATH` points at a file in a read-only volume) is unchanged.

## Decision

The controller materialises every prompt into an owned `corev1.Secret` of type `Opaque`, named `<run>-prompt`, with key `prompt.txt`. The agent Pod mounts it at `/paddock/prompt/prompt.txt`, unchanged.

- **No branching**: inline prompts, ConfigMap-sourced prompts, and Secret-sourced prompts all go through the same `ensurePromptSecret` code path.
- **Owner reference**: the Secret is owner-ref'd to the HarnessRun with `controller: true`, so cascade delete GCs it with the run.
- **RBAC**: the controller-manager's ClusterRole gains `secrets: create;update;patch;delete` (previously `get;list;watch` for reading credential Secrets). The broadened scope is documented in the RBAC block.
- **Volume**: the prompt volume uses `Secret:` instead of `ConfigMap:`. Items still select `prompt.txt`.
- **Env contract**: unchanged. Agents, adapters, and collectors still see `$PADDOCK_PROMPT_PATH=/paddock/prompt/prompt.txt`.

## Consequences

- Prompts sourced from Secrets stay in Secrets. `kubectl get configmaps -l paddock.dev/run=<run>` no longer lists a prompt CM; `kubectl get secrets -l paddock.dev/run=<run>` does.
- Debugging is slightly worse for non-sensitive prompts — `kubectl describe secret` redacts values, so inspecting the prompt requires `-o jsonpath='{.data.prompt\.txt}' | base64 -d`. Acceptable trade; `kubectl paddock` could grow a convenience `prompt` subcommand in future if this becomes annoying.
- The controller's RBAC is broader: it can now create/update/delete Secrets cluster-wide. This is consistent with how it already creates/updates ConfigMaps, Jobs, PVCs, ServiceAccounts, Roles, and RoleBindings — the controller is the trust boundary. Per-run `resourceNames` scoping isn't feasible because the prompt Secret name is derived per run and chart-install-time RBAC can't know them.
- When the broker lands (v0.4) and starts minting short-lived credentials, the same `secrets create` verb supports that use case without further RBAC changes.
- Size limit unchanged: Secrets and ConfigMaps both cap at 1 MiB, and the inline-prompt cap (256 KiB, ADR-N/A — see webhook validator) holds well below either ceiling.

## Alternatives considered

- **Per-run Role with `resourceNames` instead of cluster-wide write.** Attractive from a least-privilege angle, but chart-install time can't know run names. The controller would need to create the Role + RoleBinding per run — which it already does for the collector's output ConfigMap. Feasible, but the controller already has broad privileges inside the run's namespace (creates Jobs, PVCs, ConfigMaps, SAs); adding one more object type doesn't change the threat model meaningfully. Revisit if we ever move Paddock's own trust boundary.
- **Encrypt-at-rest requirement on the cluster.** Out of scope. If the cluster doesn't encrypt Secrets at rest, prompt leakage via etcd dump is a concern — but so is every other Secret in the cluster. Users who need etcd encryption should enable it in kube-apiserver; Paddock doesn't gate on it.
- **Prompt-in-Job-annotation.** Considered briefly; rejected. Annotations are subject-to-list on pods just like ConfigMap data, plus there's a 256 KiB etcd object-size soft-ceiling that makes long prompts fragile.
