---
name: ruk-workspaces
description: Manage Ruk workspaces for coding agents. Use when an agent must acquire or reuse a dependency-ready Git workspace, run or sync work inside it, inspect or renew its assignment, release it safely, or plan and apply Ruk garbage collection.
---

# Ruk Workspaces

## Workflow

1. Run `ruk acquire <branch> --owner <stable-agent-id> --json` from the source
   repository. Add `--from`, `--ttl`, or both only when needed.
2. Parse and retain `path`, `assignmentId`, and `expiresAt`. Treat the
   assignment ID as opaque; never derive it from the path.
3. Set the working directory to the returned path. Use `ruk run -- <command>`
   for agent processes and `ruk sync --json` after dependency inputs change.
4. Inspect with `ruk status --json`. Renew before expiry with
   `ruk renew <assignmentId> --json` when work continues.
5. Finish with `ruk release <assignmentId> --json`, even when the workspace was
   reused. Report a release failure instead of substituting another ID.

Use `ruk gc --json` to preview collection. Apply only when requested with
`ruk gc --apply --json`. Add `--force-expired` only with explicit authority to
reclaim expired assignments; expiry alone does not make them safe to remove.

Ruk coordinates one host and cleans only processes and workspaces it recorded.
Do not claim multi-host locking or arbitrary orphan discovery. Read
`docs/agent-interface.md` for exact JSON fields and stdout/stderr rules.
