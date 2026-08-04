---
layout: home
title: Dependency-aware Git workspaces

hero:
  name: Ruk
  text: Safe workspaces for coding agents
  tagline: Create, prepare, assign, and reuse Git worktrees without sharing writable dependencies between agents.
  actions:
    - theme: brand
      text: Get started
      link: /getting-started/
    - theme: alt
      text: CLI reference
      link: /reference/cli

features:
  - title: Local by design
    details: Ruk coordinates worktrees on one host. It needs no service, account, or long-running daemon.
  - title: Fenced ownership
    details: Every assignment receives an immutable ID, so delayed automation cannot release a workspace after reassignment.
  - title: Dependency-aware
    details: Ruk fingerprints dependency inputs and prepares a local projection only when the workspace needs one.
---

## One safe lifecycle

```sh
ruk acquire agent/auth-flow --owner agent-17 --json
cd <returned-path>
ruk run -- bun test
ruk release <returned-assignmentId> --json
```

Keep both values returned by `acquire`: the absolute `path` tells the agent where
to work, and the `assignmentId` authorizes renewal and release.

::: warning Exact IDs matter
Never infer an assignment ID from a branch or path. A new assignment receives a
new fencing token even when Ruk reuses the same workspace.
:::

## Choose a reading path

- **Operating Ruk yourself?** Start with [your first workspace](/getting-started/).
- **Writing agent automation?** Use the [agent integration guide](/guides/agent-integration).
- **Looking for a flag or JSON field?** Open the [CLI reference](/reference/cli)
  or [JSON contracts](/reference/json).

The name comes from `රුක්`—tree. The command is pronounced “rook.”
