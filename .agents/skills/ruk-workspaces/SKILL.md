---
name: ruk-workspaces
description: Manage Ruk workspaces for coding agents. Use when an agent must acquire, warm, run in, inspect, renew, release, or measure a dependency-ready Git workspace.
---

# Ruk Workspaces

## Workflow

1. Run `ruk acquire <branch> --owner <stable-agent-id> --json` from the source
   repository. Add `--from`, `--fetch`, `--ttl`, or named `--port` requests only
   when needed. Fetch is explicit because it performs network I/O; without
   `--from`, it uses the primary remote's advertised default branch. A fully
   qualified remote-tracking ref fails if its named remote is missing.
2. Parse and retain `path`, `assignmentId`, `expiresAt`, and `ports`. Treat the
   assignment ID as opaque; never derive it from the path.
3. Set the working directory to the returned path. Use `ruk run -- <command>`
   for agent processes and `ruk sync --json` after dependency inputs change.
4. Inspect with `ruk status --json`. Managed `run`, `exec`, `shell`, and
   assigned `sync` operations renew automatically while active. Use
   `ruk renew <assignmentId> --json` for long idle work outside those commands.
   Use `ruk worktrees --json` to list worktrees Ruk created for this repository,
   or `ruk worktrees --all --json` for every Ruk-created worktree on this host
   (`--all` works outside a repository). Tracking lives per repository in
   `<git-common-dir>/ruk/worktrees.json`; the host-wide index at
   `~/.ruk/repositories.json` only maps repositories to those registries.
5. Finish with `ruk release <assignmentId> --json`, even when the workspace was
   reused. Report a release failure instead of substituting another ID.

## Safety and on-demand details

- Edits outside managed operations do not renew leases. Expiry never transfers ownership.
- Keep the primary checkout as a control location when assignments exist; acquire a dedicated workspace.
- On failure, parse stderr JSON `code`, `retryable`, and any exact recovery ID. Never invent an ID.
- Cleanup refusal, uncertain process identity, or surviving descendants must remain fenced; report the failure.
- Preview GC with `ruk gc --json`. Apply GC only when requested; force release or expired collection requires explicit authorization.
- Ruk coordinates one host and only recorded workspaces/processes. Named ports are cooperative reservations, not held sockets.

Read [lifecycle details](references/lifecycle-details.md) before recovery, cleanup,
`exec`/`shell`, warm-pool sizing, named-port allocation, GC, updates, or work that
depends on process fencing, dependency integrity, shared-checkout guards, or exact
JSON error semantics. Search that file for the relevant operation and read its
complete paragraphs, including failure conditions; expand to adjacent paragraphs
when needed. For exact fields, read the repository's `docs/agent-interface.md`.
