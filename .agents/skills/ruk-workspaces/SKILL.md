---
name: ruk-workspaces
description: Manage Ruk workspaces for coding agents. Use when an agent must acquire, warm, run in, inspect, renew, release, or measure a dependency-ready Git workspace.
---

# Ruk Workspaces

## Workflow

1. Run `ruk acquire <branch> --owner <stable-agent-id> --json` from the source
   repository. Add `--from`, `--fetch`, `--ttl`, or named `--port` requests only
   when needed. Fetch is explicit because it performs network I/O.
2. Parse and retain `path`, `assignmentId`, `expiresAt`, and `ports`. Treat the
   assignment ID as opaque; never derive it from the path.
3. Set the working directory to the returned path. Use `ruk run -- <command>`
   for agent processes and `ruk sync --json` after dependency inputs change.
4. Inspect with `ruk status --json`. Renew before expiry with
   `ruk renew <assignmentId> --json` when work continues.
5. Finish with `ruk release <assignmentId> --json`, even when the workspace was
   reused. Report a release failure instead of substituting another ID.

For a short clean job, use `ruk exec <branch> -- <command>` to acquire, run,
and release automatically. If the command leaves changes or cleanup fails, Ruk
retains the assignment and prints the exact recovery ID. It also retains the
assignment when child identity cannot be established while descendants remain;
surviving POSIX process groups remain tracked even after their leader exits.
Inspect that process tree before forcing release. Use `ruk shell <branch>` for
an interactive assigned shell; commit intended work before exit so normal
release can succeed. Release integrity-validates recorded dependency projections:
unchanged projections stay warm, while modified projections are discarded and
rebuilt from the package store on the next acquisition.

Use `ruk warm --count <n> --json` before a known burst of agents. The count is
the desired number of available prepared workspaces, not the number to add.
Use `ruk stats --json` for recorded reuse and preparation metrics; add `--disk`
only when an on-demand filesystem scan is acceptable.

Named ports are cooperative host-local reservations. `--port app` returns an
`app` field and makes `RUK_PORT_APP` available to `ruk run`, `exec`, and
`shell`. Ruk does not hold the socket against unrelated processes.

Use `ruk gc --json` to preview collection. Apply only when requested with
`ruk gc --apply --json`. Add `--force-expired` only with explicit authority to
reclaim expired assignments; expiry alone does not make them safe to remove.
GC fences interrupted warm preparations with the warm lock before collecting
them.

Ruk coordinates one host and cleans only processes and workspaces it recorded.
Do not claim multi-host locking or arbitrary orphan discovery. Read
`docs/agent-interface.md` for exact JSON fields and stdout/stderr rules.
When a JSON command fails, parse the JSON error from stderr and use its stable
`code` and `retryable` fields; stdout contains no success record.
