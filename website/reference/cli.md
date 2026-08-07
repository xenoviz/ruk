# CLI reference

## Prepare workspaces

### `ruk init [--json]`

Prepare dependencies in the current worktree. `init` and `sync` have the same
behavior.

### `ruk sync [--json]`

Recompute the dependency fingerprint and prepare the current worktree when its
record or local dependency projection is stale.

### `ruk create <branch>`

```text
ruk create <branch> [--path <directory>] [--from <ref>] [--fetch] [--detach] [--json]
```

Create and prepare an ordinary Git worktree. This command does not create an
assignment or add the worktree to Ruk's reusable pool.

## Manage assignments

### `ruk acquire <branch>`

```text
ruk acquire <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...] [--json]
```

Assign an available managed workspace or create one. The default TTL is 480
minutes. `--fetch` refreshes the remote used by `--from` before assignment.
Without `--from`, it resolves the primary remote's advertised default branch.
Repeated `--port` options reserve named cooperative host-local ports.

### `ruk renew <assignment-id>`

```text
ruk renew <assignment-id> [--ttl <minutes>] [--json]
```

Extend the exact active assignment. The default renewal TTL is 480 minutes.

### `ruk release <assignment-id>`

```text
ruk release <assignment-id> [--force] [--json]
```

Return the assigned workspace to the pool. `--force` can kill surviving
recorded processes and discard worktree changes.

## Run and inspect

### `ruk run -- <command>`

Ensure dependencies are ready, launch the command, and return its exit code.
Inside an assigned workspace, Ruk records the child process for release.
The separator remains recommended. Ruk also accepts the command without it for
PowerShell npm shims that consume a standalone `--`.

### `ruk exec <branch> -- <command>`

Acquire a workspace, run one command, and release it when cleanup is safe.
Acquire options include `--from`, `--fetch`, `--ttl`, `--owner`, and repeated
`--port`. A dirty tree or cleanup failure retains the assignment and prints its
recovery ID.

### `ruk shell <branch>`

Open a terminal-attached interactive assigned shell. Set `RUK_SHELL` to override the platform
default. Ruk releases a clean workspace on shell exit and retains a dirty one.

### `ruk warm --count <n>`

```text
ruk warm --count <n> [--from <ref>] [--fetch] [--json]
```

Ensure that the pool contains the requested number of available prepared
workspaces. The count is a target, so an already-warm pool creates nothing.

### `ruk status [--explain] [--json]`

Report dependency readiness and lifecycle state for the current worktree.
Readiness reasons distinguish an unprepared workspace, missing dependency
projection, and changed fingerprint.

### `ruk list [--json]`

List Git worktrees with Ruk preparation and assignment information.

### `ruk stats [--disk] [--json]`

Report recorded acquisitions, reuse, preparation hits, failures, and timings.
`--disk` performs an on-demand scan and labels deduplication savings as an
estimate.

## Remove and collect

### `ruk remove <path> [--force]`

Remove an ordinary worktree. Ruk refuses to remove the current workspace or a
workspace owned by the managed pool.

### `ruk gc`

```text
ruk gc [--max-age <minutes>] [--apply] [--force-expired] [--json]
```

Preview or apply collection of old managed workspaces. The default maximum age
is 1,440 minutes. `--force-expired` requires `--apply`.

## Update

### `ruk update [--check] [--json]`

Check for or install a completed stable release. Ordinary commands never
contact GitHub for updates.

## Global output behavior

`--json` applies only where shown. Successful JSON commands write one value to
stdout and diagnostics to stderr. Failures exit nonzero and write one JSON
error with a stable `code` and `retryable` field to stderr.
