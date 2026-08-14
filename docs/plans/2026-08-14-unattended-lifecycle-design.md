# Unattended lifecycle design

## Status

Approved design for Ruk 0.2. This document defines activity-based lease renewal,
shared-checkout protection, and the benchmark used to decide whether a future
runtime rewrite in Go is justified.

## Problem

Ruk assignments expire after eight hours by default. The expiry protects
abandoned workspaces, but an active user or agent must currently remember to run
`ruk renew`. Long commands can cross the expiry even though Ruk still tracks
their processes. A missed renewal does not immediately remove the workspace,
but it makes the assignment eligible for an explicitly forced GC operation.

Agents can also run task commands from the repository's primary checkout. The
assignment ID fences Ruk lifecycle mutations; it does not prevent another local
process from switching branches or editing an ordinary Git worktree. Ruk should
therefore detect this common mistake where it has enough context to do so.

The current TypeScript runtime also remains resident while `ruk run`, `ruk exec`,
or `ruk shell` supervises a child. Before considering a Go rewrite, the project
needs repeatable measurements of Ruk-owned startup time and memory at realistic
parallelism.

## Goals

- Keep an assignment current while Ruk can prove that a managed operation is
  active.
- Remove the need for users or agents to remember routine renewal.
- Preserve the assignment ID as the fence for every lifecycle mutation.
- Refuse task-oriented commands in a shared primary checkout by default.
- Expose activity and automatic-renewal state through stable JSON fields.
- Preserve explicit forced GC as the only way to reclaim expired assignments.
- Measure the current runtimes and a representative Go prototype before choosing
  a production rewrite.

## Non-goals

- Ruk will not watch every file in a workspace.
- Ruk will not infer ownership from arbitrary processes or filesystem writes.
- Ruk will not add a background daemon.
- Ruk will not block direct Git commands or operating-system file access.
- Ruk 0.2 will not ship a Go implementation.
- Coordination remains local to one host.

## Activity model

Ruk records activity when it performs an assignment-fenced operation. The
following commands update activity:

- `ruk run`, `ruk exec`, and `ruk shell` at operation start and while their
  supervised command remains active;
- `ruk sync` at operation start and, when preparation remains active long enough,
  through the same lease keeper;
- `ruk renew` when the caller explicitly changes the lease;
- process registration and removal when they mutate assignment state.

Direct file edits do not count as activity. Builds, package managers, logs, and
abandoned background processes can write indefinitely, while a legitimate agent
may spend a long time reviewing without touching a file. A filesystem watcher
would therefore make expiry less trustworthy and require a resident service.

## State version 4

Each assignment adds these fields:

```json
{
  "leaseDurationMinutes": 480,
  "lastActivityAt": "2026-08-14T09:00:00.000Z",
  "leaseKeepers": [
    {
      "id": "opaque UUID",
      "heartbeatAt": "2026-08-14T09:05:00.000Z",
      "validUntil": "2026-08-14T09:10:00.000Z"
    }
  ]
}
```

`leaseDurationMinutes` records the duration selected by acquire or the most
recent explicit renewal. `lastActivityAt` records the latest successful,
ID-fenced activity transaction. Each keeper has its own opaque ID so concurrent
commands can start and stop without removing one another's renewal authority.

`autoRenewing` is derived rather than persisted. It is true when at least one
keeper has a `validUntil` later than the observation time. A crashed command
therefore becomes inactive without a cleanup daemon. Stale keeper entries may be
removed opportunistically during later assignment mutations.

Version 3 state migrates in memory. Existing assignments derive their lease
duration from `expiresAt - renewedAt`, set `lastActivityAt` to `renewedAt`, and
start with no keepers. Migration preserves assignment IDs, owners, ports,
processes, and lifecycle operations.

## Lease keeper module

One deep module owns automatic renewal behind this interface:

```text
withAssignmentActivity(paths, assignmentId, operation)
```

The module performs five steps:

1. Verify that the exact assignment ID is still assigned.
2. Record activity, extend expiry by the stored lease duration, and register a
   new keeper.
3. Run the supplied operation.
4. Refresh the keeper and expiry every five minutes or one-third of the lease
   duration, whichever is shorter.
5. Remove only its keeper in a `finally` path.

The implementation sets a practical lower interval for short custom leases so
the timer cannot spin. Tests use an injected clock and scheduler; production
code uses the system clock and timers.

`run`, `exec`, `shell`, and long-running `sync` operations share this module.
The CLI does not implement separate heartbeat loops.

## Renewal failures

A heartbeat first verifies the assignment ID. If the assignment no longer
exists or belongs to another caller, Ruk terminates the supervised command using
the existing identity-fenced process cleanup and reports a resource-busy error.
It must not let the command continue in a workspace owned by another assignment.

A transient state-write failure receives a short, bounded retry. If retries
fail, Ruk stops the managed command, preserves the current assignment where
possible, and returns a retryable machine-readable error. Removing a keeper is
best effort after the operation has already failed; its validity window still
prevents a stale `autoRenewing` value from persisting.

## Expiry and garbage collection

Activity renewal changes `expiresAt` through the same atomic state transaction
that verifies the assignment ID. Forced GC keeps its existing authority and
lock order. Before release it rechecks:

- the assignment ID;
- the current expiry;
- the activity update fence;
- the acquisition operation marker;
- recorded process safety.

If a heartbeat or command activity wins the race, GC skips that candidate and
continues processing the rest of its plan. Activity alone never deletes,
releases, or reassigns a workspace.

## Shared primary checkout

Ruk identifies the primary checkout through Git's common directory and worktree
metadata. The guard applies when the current path is the primary checkout and
the repository has one or more active Ruk assignments.

By default, `ruk run` and `ruk sync` refuse to continue from that checkout. Pool
and inspection commands such as `acquire`, `warm`, `list`, `gc`, and `status`
remain available. `init` also remains available because it intentionally
prepares the current checkout.

The repository configuration supports:

```json
{
  "sharedCheckoutPolicy": "deny"
}
```

Valid policies are:

- `deny`: refuse the task command and explain how to acquire a workspace;
- `warn`: emit a diagnostic and continue;
- `allow`: continue without a diagnostic.

`deny` is the default. `--allow-shared-checkout` overrides the configured policy
for one command. The CLI flag is intentionally narrow and does not mutate
configuration.

The guard cannot block direct `git switch` or filesystem writes. Documentation
must describe the primary checkout as a control location rather than claim
operating-system isolation.

## Machine-readable interface

`ruk status --json` and managed entries in `ruk list --json` add:

```json
{
  "lastActivityAt": "2026-08-14T09:05:00.000Z",
  "autoRenewing": true,
  "primaryCheckout": false,
  "managed": true,
  "activeAssignments": 1
}
```

Assignment fields remain null for unmanaged workspaces. A denied shared-checkout
command uses a stable error category, is retryable, and reports the active
assignment count and recovery instruction. Existing fields retain their names
and meanings.

## Runtime benchmark and Go experiment

Ruk 0.2 keeps the production TypeScript implementation. A benchmark harness
compares:

- the Node.js npm distribution;
- the standalone Bun executable;
- a non-shipping Go supervisor built with the standard library.

The Go prototype performs representative work rather than printing a version:
it reads a fixture state file, launches a child, writes periodic atomic
heartbeats, forwards termination, and cleans up the child.

The harness measures cold-start latency, idle resident memory, peak resident
memory, binary size, and one, ten, and twenty concurrent long-running wrappers.
Results must identify the operating system, architecture, runtime version, and
sample count. Platform-specific measurement adapters are allowed inside the
benchmark harness; they are not part of the published runtime.

A future Go migration requires a large, repeatable reduction in Ruk-owned memory
and must preserve the CLI, JSON contract, configuration, state migration,
process cleanup, updater trust, and cross-platform behavior. The experiment does
not create a permanent second production implementation.

## Verification

Tests cover:

- version 3 to version 4 migration;
- explicit renewal changing the stored lease duration;
- one keeper extending and then leaving an assignment;
- multiple keepers operating concurrently;
- a crashed or stale keeper becoming inactive;
- assignment replacement during a heartbeat;
- bounded retries and machine-readable heartbeat failures;
- forced GC racing activity before and after candidate selection;
- `deny`, `warn`, and `allow` shared-checkout policies;
- the command-line override taking precedence over configuration;
- primary checkout detection across linked worktrees;
- JSON fields and backward compatibility;
- deterministic benchmark result schemas.

The existing repository checks remain required. CI exercises Node.js 22 and 24
on Linux, macOS, and Windows, native binaries on all three systems, package
smoke tests, cross-compilation, documentation, and repository policy.

## Implementation plan

1. Add version 4 types, validation, migration, and focused state tests.
2. Add ID-fenced activity and keeper lifecycle operations to the lifecycle
   module with injected time in tests.
3. Add the lease keeper module and concurrency tests.
4. Route `run`, `exec`, `shell`, and long-running `sync` through the keeper.
5. Extend status, list, structured errors, and JSON contract tests.
6. Add primary-checkout detection and the three configuration policies.
7. Add CLI override, recovery diagnostics, and policy tests.
8. Update architecture, lifecycle documentation, command reference, examples,
   changelog, and the bundled `ruk-workspaces` skill.
9. Add the runtime benchmark harness and non-shipping Go supervisor.
10. Record benchmark results separately so generated measurements do not become
    release requirements or runtime dependencies.

Keep these changes in reviewable commits. The state migration and lifecycle
operations land before CLI integration; documentation and the skill update land
with the public behavior they describe.

## Release criteria

Ruk 0.2 is ready when active managed commands renew without user intervention,
stale keepers expire without a daemon, forced GC cannot cross a concurrent
activity fence, shared-checkout policies behave consistently in text and JSON
mode, version 3 state migrates without losing ownership, and the complete
cross-platform CI suite passes. The Go benchmark informs a later decision and
does not block the release unless its harness compromises repository checks.
