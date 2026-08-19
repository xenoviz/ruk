# JSON contracts

Use `--json` for automation. Paths are absolute and timestamps are ISO 8601 UTC
strings. Consumers must ignore unknown object fields. The installed runtime is
a native Go binary in both npm and standalone distributions; JSON contracts are
shared across those distributions.

## Acquire

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

`mode` reports the preparation backend. Current values include
`managed-install`, `bun-global-store`, and `pnpm-global-store`.
Named ports are also injected into assigned processes as variables such as
`RUK_PORT_APP`.

## Renew

```json
{
  "status": "renewed",
  "assignmentId": "46bc4998-95b0-4d16-b017-69b06a13747b",
  "path": "/absolute/path/to/workspace",
  "expiresAt": "2026-08-04T09:00:00.000Z"
}
```

## Release

```json
{
  "status": "available",
  "assignmentId": "46bc4998-95b0-4d16-b017-69b06a13747b",
  "path": "/absolute/path/to/workspace",
  "cleanedProcesses": 2
}
```

`cleanedProcesses` counts recorded process identities that Ruk found and asked
the operating system to terminate.

## Garbage collection

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

`status` is `planned` for a dry run and `collected` with `--apply`.

## Status and list

`ruk status --json` and each item from `ruk list --json` include `lifecycle`,
`assignmentId`, `expiresAt`, `lastActivityAt`, `autoRenewing`,
`primaryCheckout`, `managed`, and `activeAssignments`. Assignment timestamps
are `null` when no assignment is active. `autoRenewing` is derived from current
fenced keepers rather than stored as a durable status.

## Worktrees

`ruk worktrees --json` returns the repository root, its Git common directory,
and the tracked Ruk-created worktrees sorted by `path`:

```json
{
  "repository": "/work/app",
  "commonDir": "/work/app/.git",
  "worktrees": [
    {
      "path": "/work/app-ruk-agent-task-1a2b3c4d",
      "branch": "agent/task",
      "source": "acquire",
      "createdAt": "2026-08-19T10:00:00.000Z",
      "updatedAt": "2026-08-19T10:00:00.000Z",
      "exists": true
    }
  ]
}
```

`source` is `acquire`, `warm`, or `create`. `worktrees` is an empty array when
none are tracked.

`ruk worktrees --all --json` aggregates those per-repository objects from the
host index at `~/.config/ruk/host/repositories.json`:

```json
{
  "repositories": [
    {
      "repository": "/work/app",
      "commonDir": "/work/app/.git",
      "worktrees": [
        {
          "path": "/work/app-ruk-agent-task-1a2b3c4d",
          "branch": "agent/task",
          "source": "acquire",
          "createdAt": "2026-08-19T10:00:00.000Z",
          "updatedAt": "2026-08-19T10:00:00.000Z",
          "exists": true
        }
      ]
    }
  ]
}
```

`repositories` is an empty array when none are tracked. The host index is
display-only discovery metadata; per-repo registries stay authoritative.

## Failure behavior

A failed JSON command exits nonzero, emits no success record on stdout, and
writes one error record to stderr:

```json
{
  "status": "error",
  "code": "WORKSPACE_DIRTY",
  "message": "Workspace has uncommitted changes.",
  "retryable": false
}
```

Use `code` for decisions and `message` for operators. Unknown codes must be
treated as `OPERATION_FAILED`.

A denied shared-checkout command reports `RESOURCE_BUSY`, sets `retryable` to
`true`, and also includes `activeAssignments` and `recovery`.

## Update distribution

Package updates report the package-manager method (`npm`, `bun`, `pnpm`, or
`yarn`) and delegate installation to that manager. Standalone updates report
`standalone` and the selected native asset. Stable versions ignore prerelease
releases; a current prerelease version follows newer prereleases automatically.
