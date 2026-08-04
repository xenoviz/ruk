# JSON contracts

Use `--json` for automation. Paths are absolute and timestamps are ISO 8601 UTC
strings. Consumers must ignore unknown object fields.

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
  "mode": "bun-global-store"
}
```

`mode` reports the preparation backend. Current values include
`managed-install`, `bun-global-store`, and `pnpm-global-store`.

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
`assignmentId`, and `expiresAt`. Assignment fields are `null` when no assignment
is active.

## Failure behavior

A failed command exits nonzero and does not emit a success record. Read stderr
for the diagnostic; do not treat partial output as JSON success.
