# Troubleshooting

## `ruk` is not found

Confirm the global package-manager binary directory or standalone executable is
on `PATH`, then run:

```sh
ruk --version
```

The npm package installs a native platform binary. Node.js or Bun may be needed
by the package manager during installation, and the published command entry may
briefly run Node to finish native placement when install scripts were skipped,
but neither remains the command supervisor afterward.

## The declared package manager is missing

Ruk uses the repository's `packageManager` field before lockfile detection.
Install that manager at the declared version or configure a deterministic
`installCommand`.

## Status says `sync-required`

The dependency fingerprint changed, the recorded projection is missing or was
modified (including a linked package target), or the workspace has not been
prepared.

```sh
ruk sync --json
```

## Acquisition fails on a branch

Git rejects a local branch that is already checked out in another worktree.
Inspect active worktrees:

```sh
git worktree list
ruk list --json
```

Choose another agent branch or release the worktree that owns it.

## Release reports a dirty workspace

Normal release protects uncommitted work. Commit, export, or discard the files,
then retry with the same assignment ID.

::: danger Forced release discards work
`ruk release <assignment-id> --force` resets and cleans the worktree, including
ignored files. Use it only when losing those files is acceptable.
:::

## An assignment expired

Expiry does not remove ownership. If the agent is still active, renew the exact
assignment ID. If it has stopped, preview garbage collection and confirm the
recovery target before using `--force-expired`.

## A tracked process survives release

Without `--force`, Ruk preserves the assignment when a recorded process does
not stop gracefully. Inspect the process and retry. Forced release escalates to
forceful process-tree termination.

On Windows, routine Ruk liveness checks use native process APIs and do not
launch PowerShell. If a process remains, preserve the assignment and inspect
the recorded process before considering forced recovery.

## Shared mode fails checks

Return to managed mode. A successful dependency install does not prove that an
isolated layout works with the repository's compiler, build tools, tests, and
lifecycle scripts.

## State is invalid

Ruk stores state under the Git common directory and fails visibly on corrupted
or unsupported data. Do not silently replace state while assignments may be
active. Preserve the diagnostic and inspect the repository before recovery.
