---
title: Bridge
description: Translator between an external system and the run platform — webhooks in, CRDs out.
---

A **Bridge** is a translator between an external system and the run platform.
A bridge knows about webhooks, OAuth, and how to post activity back to Linear
or GitHub or Slack. It knows nothing about pods, PVCs, or credentials — it
just creates and watches Custom Resources.

## What a bridge does

The shape of every bridge is roughly the same:

1. Receive an external event (webhook, polled API, etc.).
2. Translate it into a Session CRD (its own type) and one or more HarnessRuns
   (the platform's type), optionally anchored to a Workspace.
3. Watch the resulting status.
4. Mirror progress back outward — comments on the issue, threaded replies on
   the channel, status updates on the PR.

## Why bridges are cheap to add

A bridge is a webhook receiver, a session CRD, and a small controller. Most of
the heavy lifting — scheduling runs, handling failures, capturing events,
managing workspaces — is delegated to the run platform. Adding a bridge for a
new external system is a few hundred lines of code, not a re-implementation
of the platform.

## What ships in the box

The reference bridge for v0.5 is **Linear**, landing in the
`paddock-bridges` repo. Other bridges (GitHub Issues, Slack) come later as
the bridge SDK stabilises.
