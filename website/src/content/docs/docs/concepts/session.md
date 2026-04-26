---
title: Session
description: A bridge-owned anchor for an external conversation — Linear ticket, GitHub issue, Slack thread.
---

When an external system delegates work to Paddock — Linear, GitHub Issues,
Slack — it creates a **Session**: an adapter-specific Custom Resource owned
by the corresponding bridge.

## What a session represents

A session represents the external conversation or ticket. It tracks:

- Which workspace belongs to it.
- The sequence of HarnessRuns spawned from it.
- The mapping back to the external system's identity and activity model
  (Linear issue ID, GitHub PR number, Slack thread timestamp).

The session is the anchor point for the human's view of "what is the agent
doing about my ticket".

## Why sessions are bridge-owned

Sessions are not a core platform concern. They are a **bridge concern** —
each bridge defines its own Session CRD with the fields its external system
needs. The Linear bridge's session looks different from the GitHub bridge's
session, and that's correct: they map to different external models.

The run platform doesn't see sessions. It sees HarnessRuns. Bridges create
both, watch their status, and mirror progress back to the external system.

## When there is no session

When the entry point is a CLI invocation or a cron job, there is no session.
The user just submits a HarnessRun directly, optionally attached to a
reusable Workspace. Sessions exist when an external conversation does; not
when there isn't one.
