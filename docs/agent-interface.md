# Agent interface

Ruk's installed command is a dependency-free native Go binary. Use `--json` for
automation. On success, Ruk writes exactly one JSON value and
a trailing newline to stdout. Human-mode progress and dependency-installer
output use the terminal; JSON mode discards installer streams so verbose tools
cannot grow an unbounded in-memory buffer. A failure exits nonzero, writes one
JSON error to stderr, and does not emit a success record.

Paths are absolute. Timestamps use ISO 8601 UTC strings. Within a major
release, documented fields keep their names, types, and meanings. Ruk may add
object fields; consumers must ignore unknown fields.

## Acquire

```text
ruk acquire <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...] [--json]
```

The TTL defaults to 480 minutes. The owner defaults to `RUK_AGENT_ID`, then to
`<hostname>:<pid>`. Managed Ruk operations renew the assignment automatically
while they remain active; the TTL still controls how long an idle assignment
remains current after the latest observed activity. The initial lease starts
when acquisition preparation finishes and the ready assignment is published,
so dependency installation time does not consume the requested TTL.

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

If a reused acquisition fails after ownership is published, Ruk retains the
workspace and includes `assignmentId`, `path`, `expiresAt`, and a `recovery`
release command in the error. The original machine-readable category is
preserved, so dependency failures remain `DEPENDENCY_PREPARATION_FAILED`, port
failures remain `PORT_UNAVAILABLE`, and an otherwise unclassified retained
failure is retryable `RESOURCE_BUSY`. Automation can therefore respond to the
cause while preserving and later recovering the exact fenced assignment. Ruk
clears the incomplete acquisition marker before returning this error, so the
exact recovery command is accepted when the caller decides cleanup is safe.
The returned expiry is the current retained-state value, including a heartbeat
renewal that completed while dependency synchronization was running.
Assigned synchronization rechecks the exact assignment after acquiring the
dependency lock; release or reassignment while waiting makes the operation fail
without touching dependency projections.

`--fetch` explicitly refreshes the remote selected by `--from`; without
`--from`, Ruk resolves the primary remote's advertised default branch. Fully
qualified remote-tracking refs fail if their named remote is missing. Named
ports are unique among active Ruk assignments and are injected into assigned
commands as normalized variables such as `RUK_PORT_APP`.

The start point is pinned to an immutable commit in the checkout where the
command runs before any state or worktree mutation. `--from` accepts refs;
worktree-relative refs such as `HEAD` are resolved in that checkout, not in a
recycled pool slot.

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
Failed acquisition cleanup restores assigned ownership so the fenced workspace
can be discovered and recovered.
Dependency installers are tracked with the same native process identity rules
as managed commands. If cancellation cannot prove that the installer tree is
gone, Ruk keeps the assigned, failed, or preparing workspace fenced and reports
a retryable cleanup failure. Applied GC can later drain that exact recorded
process tree before collecting an unassigned failed preparation.
Ordinary release is rejected while an acquisition handoff marker is active.
Tracked, untracked, and ignored files are removed before a workspace enters the
pool, except dependency projections recorded and integrity-validated by Ruk.
Preserving an unchanged projection allows reassignment to skip installation;
a modified projection is discarded and rebuilt on the next acquisition.
Agents must still commit or otherwise save every intended artifact first.
It never permits an old ID to release a newer assignment. `cleanedProcesses`
counts recorded process identities found and sent a termination request.

## Garbage collection

```text
ruk gc [--max-age <minutes>] [--apply] [--force-expired] [--json]
```

The age defaults to 1440 minutes. GC is a dry run unless `--apply` is present.
Interrupted preparations, acquisition handoffs, and collections older than the
cutoff are safe candidates; live operations hold the corresponding locks and
cannot be collected concurrently.

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
`--apply` and `--force-expired` are present; successfully forced removals are
omitted from `expired`, and apply output is recomputed from final lifecycle state.
Forced collection rechecks the current expiry in the lifecycle state transaction,
so a concurrent renewal prevents collection. It also skips assignments with a
current fenced lease keeper even when their stored expiry has just elapsed.

## Worktrees

```text
ruk worktrees [--all] [--json]
```

All worktrees created by `ruk acquire`, `ruk warm`, and `ruk create` are tracked
per repository in `<git-common-dir>/ruk/worktrees.json`. `ruk worktrees` lists
that registry for the discovered repository. Entries are removed when Ruk
removes the worktree (`ruk remove`, `ruk gc --apply`, and create-failure
cleanup). Git remains authoritative for which worktrees exist; the registry
records which of them Ruk created.

`ruk worktrees --all` works outside a repository. It reads the host index at
`~/.ruk/repositories.json` and aggregates each per-repo registry.
The index is display-only discovery metadata: it maps repositories to their
registries and contains no worktree records. Per-repo registries stay
authoritative. Deleted repositories are skipped on read and pruned from the
index on the next write.

```json
{
  "repository": "/absolute/path/to/repo",
  "commonDir": "/absolute/path/to/repo/.git",
  "worktrees": [
    {
      "path": "/absolute/path/to/workspace",
      "branch": "agent/my-task",
      "source": "acquire",
      "createdAt": "2026-08-19T10:00:00.000Z",
      "updatedAt": "2026-08-19T10:00:00.000Z",
      "exists": true
    }
  ]
}
```

`--all` wraps those per-repository objects:

```json
{
  "repositories": [
    {
      "repository": "/absolute/path/to/repo",
      "commonDir": "/absolute/path/to/repo/.git",
      "worktrees": [
        {
          "path": "/absolute/path/to/workspace",
          "branch": "agent/my-task",
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

`source` is `acquire`, `warm`, or `create`. Records are sorted by `path`.
`worktrees` is an empty array when none are tracked. `exists` is true when the
folder is present on disk. `--all` omits repositories whose registries have
zero records; the host-wide JSON shape is `{"repositories":[]}` when none remain.

## Inspecting and running

`ruk list --json` and `ruk status --json` include `lifecycle`, `assignmentId`,
`expiresAt`, `lastActivityAt`, `autoRenewing`, `primaryCheckout`, `managed`, and
`activeAssignments`. Assignment timestamps are null when no assignment is
active. `autoRenewing` is true only while a current fenced keeper is visible.
Transient heartbeat writes receive bounded retries. A persistent write failure
or lost assignment fence stops the managed command and is reported as retryable
`RESOURCE_BUSY`. A refresh that waited behind a newer explicit renewal cannot
regress its timestamps or shorten its expiry, and initial keeper registration
captures its validity window only after acquiring the state lock. If stopping
the command cannot verify the original detached leader or prove that its process
tree is gone, Ruk preserves that cleanup failure and retains the assignment
instead of returning the workspace to the pool.
Attached POSIX cleanup verifies the leader and every captured descendant's start
identity immediately before signaling it; a reused PID or an unreadable identity
for a process that is still alive retains the assignment. Ruk also keeps the
workspace tree lock until process registration succeeds or failed-registration
cleanup settles, preventing concurrent release during that handoff.
The heartbeat failure is still reported if operation work resolves while its
failure hook is running; concurrent success cannot hide a lost renewal fence.
Nested activity-renewal failures are reported as retryable `RESOURCE_BUSY`, and
keeper completion cannot shorten a newer renewal when the wall clock moves backward.
Status reports `projection-changed` with a `ruk sync` recovery when recorded
projection contents or linked package targets no longer match their fingerprint.
`ruk run -- ...` validates dependency inputs and projection integrity, then
rechecks the assignment fence and records the child process only when invoked
inside an assigned managed workspace. Use the returned acquire path as the
working directory.
Detached managed `run` and `exec` commands forward wrapper `SIGINT` and
`SIGTERM` signals to their recorded POSIX process group and return conventional
130 or 143 exit codes when the child uses the default signal disposition.

The primary checkout is a control location while assignments are active.
`ruk run` and `ruk sync` refuse task work there by default. Repositories can set
`sharedCheckoutPolicy` to `warn` or `allow`; `--allow-shared-checkout` permits
one intentional command. A denied JSON `sync` reports retryable
`RESOURCE_BUSY`, `activeAssignments`, and a `ruk acquire <branch>` recovery.

`ruk exec <branch> -- <command>` composes acquire, run, and normal release. It
retains the assignment when the command leaves a dirty tree, cleanup fails, or
the launched process cannot be identified while descendants remain.
Leaderless detached groups retain the assignment until their known descendants
exit; Ruk does not signal a group whose original leader cannot be verified.
Windows process trees follow the same fail-closed rule when a leader exits or
its PID is reused, including registration failures.
`ruk warm --count <n> --json` counts only integrity-valid projections and
ensures that the pool has the requested number of available prepared workspaces;
acquisition uses the same capacity lock.
Statistics count both assigned and returning workspaces as active assignments.
Available capacity excludes workspaces fenced by an in-progress collection.
Allocated ports are probed through a dual-stack IPv6 wildcard when supported,
with an IPv4 fallback on hosts without IPv6.

`ruk stats --json` returns aggregate acquisition, reuse, preparation, failure,
and timing counters. `--disk` adds an on-demand scan; `estimatedBytesAvoided`
is explicitly approximate, nested linked targets are counted once, and target
traversal is sequential to bound filesystem and memory pressure.

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
Malformed `.rukrc.json` input and TTL values outside the supported date range
are non-retryable `INVALID_ARGUMENT` errors raised before acquisition.

## Update ownership

`ruk update` is explicit and never runs in the background. An npm installation
delegates the exact version to the package manager recorded by its durable
distribution marker; standalone binaries verify the release manifest and
replace their native executable atomically. On Windows, either distribution
reports a scheduled update while a detached handoff waits for the running Ruk
process to exit before replacing locked native files. Stable installations
select stable releases. A current prerelease follows newer prereleases
automatically, which keeps beta installations on the beta channel without a
second flag.
