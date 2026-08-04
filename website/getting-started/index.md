# Your first workspace

This guide acquires a dependency-ready worktree, runs a command inside it, and
returns it to Ruk's reusable pool.

## 1. Start in the source repository

Run `acquire` from any worktree in the target Git repository:

```sh
ruk acquire agent/auth-flow --owner agent-17 --json
```

Ruk either reuses an available managed workspace or creates a new detached Git
worktree. It switches that worktree to `agent/auth-flow`, prepares dependencies,
and assigns the workspace to `agent-17`.

The JSON response contains the two values you must retain:

```json
{
  "status": "assigned",
  "assignmentId": "46bc4998-95b0-4d16-b017-69b06a13747b",
  "path": "/absolute/path/to/workspace",
  "branch": "agent/auth-flow",
  "expiresAt": "2026-08-04T05:00:00.000Z",
  "reused": false,
  "fingerprint": "sha256 dependency fingerprint",
  "mode": "managed-install"
}
```

## 2. Work at the returned path

```sh
cd /absolute/path/to/workspace
ruk run -- bun test
```

`ruk run` checks dependency readiness before starting the command. Inside an
assigned workspace, it also records the child process so release can clean it
up safely.

Use the repository's normal Git workflow. Commit or otherwise save every file
you intend to keep before release.

## 3. Inspect the workspace

```sh
ruk status --json
```

A ready assigned workspace reports its lifecycle, assignment ID, expiry, and
current dependency fingerprint.

## 4. Release the exact assignment

```sh
ruk release 46bc4998-95b0-4d16-b017-69b06a13747b --json
```

Release stops recorded child processes, cleans the worktree, detaches it, and
returns it to the pool.

::: danger Save work before release
Release removes tracked, untracked, and ignored files before the workspace
becomes available. A dirty workspace blocks normal release; `--force` discards
its changes.
:::

## Longer work

Assignments default to eight hours. Renew before `expiresAt` when work
continues:

```sh
ruk renew 46bc4998-95b0-4d16-b017-69b06a13747b --json
```

Read [Assignments and renewal](/guides/assignments) for lease and recovery
rules.
