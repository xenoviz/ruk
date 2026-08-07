# Agent interface

Use `--json` for automation. On success, Ruk writes exactly one JSON value and
a trailing newline to stdout. Progress, dependency-installer output, and
diagnostics go to stderr. A failure exits nonzero, writes one JSON error to
stderr, and does not emit a success record.

Paths are absolute. Timestamps use ISO 8601 UTC strings. Within a major
release, documented fields keep their names, types, and meanings. Ruk may add
object fields; consumers must ignore unknown fields.

## Acquire

```text
ruk acquire <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...] [--json]
```

The TTL defaults to 480 minutes. The owner defaults to `RUK_AGENT_ID`, then to
`<hostname>:<pid>`.

```json
{
  "status": "assigned",
  "assignmentId": "46bc4998-95b0-4d16-b017-69b06a13747b",
  "path": "/absolute/path/to/workspace",
  "branch": "agent/my-task",
  "expiresAt": "2026-08-04T05:00:00.000Z",
  "reused": false,
  "fingerprint": "sha256 dependency fingerprint",
  "mode": "bun-global-store",
  "ports": {
    "app": 43127
  }
}
```

`assignmentId` is an immutable fencing token. Store it and use it for renew
and release. `reused` reports whether Ruk assigned an available managed
workspace instead of creating one.

`--fetch` explicitly refreshes the remote selected by `--from`. Named ports are
unique among active Ruk assignments and are injected into assigned commands as
normalized variables such as `RUK_PORT_APP`.

## Renew

```text
ruk renew <assignment-id> [--ttl <minutes>] [--json]
```

The TTL defaults to 480 minutes and is measured from renewal time.

```json
{
  "status": "renewed",
  "assignmentId": "46bc4998-95b0-4d16-b017-69b06a13747b",
  "path": "/absolute/path/to/workspace",
  "expiresAt": "2026-08-04T09:00:00.000Z"
}
```

Renew fails if the assignment is stale or is no longer assigned.

## Release

```text
ruk release <assignment-id> [--force] [--json]
```

```json
{
  "status": "available",
  "assignmentId": "46bc4998-95b0-4d16-b017-69b06a13747b",
  "path": "/absolute/path/to/workspace",
  "cleanedProcesses": 2
}
```

Release is fenced by the assignment ID. Ruk cleans only processes recorded for
that assignment; it does not search for arbitrary processes. Without `--force`,
release fails and preserves the assignment if a tracked process survives
graceful termination or the worktree is dirty. `--force` force-kills surviving
tracked process trees and discards tracked and untracked changes.
Tracked, untracked, and ignored files are removed before a workspace enters the
pool, except dependency projections recorded by Ruk. Preserving those
projections allows an unchanged reassignment to skip installation; agents must
still commit or otherwise save every intended artifact first.
It never permits an old ID to release a newer assignment. `cleanedProcesses`
counts recorded process identities found and sent a termination request.

## Garbage collection

```text
ruk gc [--max-age <minutes>] [--apply] [--force-expired] [--json]
```

The age defaults to 1440 minutes. GC is a dry run unless `--apply` is present.

```json
{
  "status": "planned",
  "removed": [
    {
      "path": "/absolute/path/to/workspace",
      "lifecycle": "available",
      "reason": "older than max age"
    }
  ],
  "expired": [
    {
      "path": "/absolute/path/to/other-workspace",
      "assignmentId": "8fbdb311-fcd0-43fe-b699-c68934b29175",
      "expiresAt": "2026-08-03T10:00:00.000Z"
    }
  ]
}
```

`status` is `planned` for a dry run and `collected` when applying the plan.
`removed` contains candidates in dry-run output and selected removals in apply
output. Expired assigned or returning workspaces are only reported unless both
`--apply` and `--force-expired` are present.

## Inspecting and running

`ruk list --json` and `ruk status --json` include `lifecycle`, `assignmentId`,
and `expiresAt`; assignment fields are null when no assignment is active.
`ruk run -- ...` records the child process only when invoked inside an assigned
managed workspace. Use the returned acquire path as the working directory.

`ruk exec <branch> -- <command>` composes acquire, run, and normal release. It
retains the assignment when the command leaves a dirty tree, cleanup fails, or
the launched process cannot be identified while descendants remain.
`ruk warm --count <n> --json` ensures that the pool has the requested number of
available prepared workspaces.

`ruk stats --json` returns aggregate acquisition, reuse, preparation, failure,
and timing counters. `--disk` adds an on-demand scan; `estimatedBytesAvoided`
is explicitly approximate.

## Failure record

```json
{
  "status": "error",
  "code": "WORKSPACE_DIRTY",
  "message": "Workspace has uncommitted changes.",
  "retryable": false
}
```

Stable categories include `INVALID_ARGUMENT`, `ASSIGNMENT_CONFLICT`,
`WORKSPACE_DIRTY`, `PORT_UNAVAILABLE`, `RESOURCE_BUSY`,
`DEPENDENCY_PREPARATION_FAILED`, `GIT_OPERATION_FAILED`, and
`OPERATION_FAILED`. Consumers must still ignore unknown fields and categories.
