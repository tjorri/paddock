# ADR-0004: Ephemeral workspaces are real `Workspace` CRs

- Status: Accepted
- Date: 2026-04-22
- Deciders: @tjorri
- Applies to: v0.1+

## Context

A HarnessRun whose template requires a workspace but which was submitted without `spec.workspaceRef` needs *some* workspace to mount. Two paths were considered:

- **Invisible volume**: the HarnessRun controller attaches an `emptyDir` (or anonymous PVC) directly to the Job. No `Workspace` CR exists for the run.
- **Real `Workspace` CR**: the controller creates a `Workspace` named after the run, owner-ref'd to it, marked `spec.ephemeral: true`. The Workspace controller reconciles it the same way it reconciles user-created workspaces.

The first is tempting for simplicity, but it creates two code paths in the run controller ("do we have a workspace?"), bypasses the Workspace controller's state machine entirely, and loses the debuggability of `kubectl get workspaces`.

## Decision

Ephemeral workspaces are real `Workspace` resources. When a HarnessRun is admitted without `spec.workspaceRef` and its template requires a workspace:

- The HarnessRun controller creates a `Workspace` named `<run>-ws`, in the run's namespace.
- `spec.ephemeral: true` is set (informational — the controller doesn't gate behaviour on it).
- `ownerReferences` points to the HarnessRun, with `controller: true` and `blockOwnerDeletion: true`. Deletion of the run cascades; `blockOwnerDeletion` ensures the workspace finalizer runs before the run's own finalizer releases.
- `spec.storage` defaults come from the template's future `workspace.storage` block (v0.2+); in v0.1 the controller uses conservative defaults (1Gi, `ReadWriteOnce`, cluster default StorageClass).
- No `spec.seed` — ephemeral workspaces start empty.

The Workspace controller is oblivious to `spec.ephemeral`: it reconciles the CR identically to a user-created one. The status feeds back into the run controller through the same `activeRunRef` channel.

## Consequences

- `kubectl get workspaces` shows the ephemeral ones alongside user-created ones. Debugging a failed run means looking at one workspace, one Job — not a phantom volume.
- Workspace phase state machine is exercised by every run, which is a much better test bed than synthetic `emptyDir` volumes.
- One extra object per one-shot run. Negligible at our scale; revisit if we ever see >10k concurrent runs and etcd pressure.
- `spec.ephemeral` is an informational flag, not a controller gate. That avoids divergent code paths. Platform teams can still filter on it (`kubectl get workspaces -l …` or by field selector once we add a selector) to distinguish long-lived workspaces from one-shot ones.
- When the HarnessRun is deleted, `blockOwnerDeletion: true` means the Workspace's finalizer (M2) runs first. This matters because the Workspace finalizer refuses deletion while `activeRunRef` is set — the run controller must clear `activeRunRef` during its own finalize before removing its finalizer, or the garbage-collection chain stalls.

## Alternatives considered

- **Anonymous emptyDir in the Pod.** Rejected for the reasons above: two code paths, no CR to observe, the Workspace controller gets bypassed.
- **Anonymous PVC owned directly by the HarnessRun.** Better than emptyDir because it actually persists across retries, but still bypasses the Workspace controller and loses the shared state machine.
- **Mandate `workspaceRef` on every run, reject runs without it.** Forces bridges and users to always provision a Workspace first, which is busywork for one-shot runs. Worse UX for the common "just try this" case.
