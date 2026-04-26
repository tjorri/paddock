---
title: Workspace
description: A persistent scratch area shared across runs — backed by a PVC, optionally seeded from git.
---

A **Workspace** is a persistent scratch area shared across runs, backed by a
PersistentVolumeClaim. A workspace can be seeded from git or from an archived
snapshot, can be archived to object storage when idle, and outlives the
individual runs that use it.

## What workspaces remember

Workspaces are how the agent "remembers" between invocations:

- Cloned repos stay cloned.
- Intermediate edits persist.
- Harness-native session files (`.claude/`, `.codex/`, etc.) survive across
  invocations.
- Build caches, downloaded dependencies, and other warm state stick around.

## Concurrency

A workspace is serialised to **one active HarnessRun at a time**. If a second
run targets the same workspace while one is still running, the second waits.
This avoids the entire class of bugs that come from two agents writing the
same files simultaneously.

## Multi-repo seeding

A workspace's `spec.gitRepos[]` declares the repos to seed at workspace
creation time. The platform clones each one into a deterministic path and
keeps the clones up to date between runs. The agent sees a multi-repo working
tree without ever having had to know how to clone things itself.
