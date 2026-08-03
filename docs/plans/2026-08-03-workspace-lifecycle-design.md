# Workspace lifecycle design

## Scope

Ruk reuses prepared worktrees for agents on one host. Each active assignment
has a lease and an immutable assignment ID that fences delayed renew or release
commands. Coordination across hosts, port allocation, and discovery of
unrecorded processes or worktrees are outside this design.

## State machine

```text
absent -> preparing -> assigned -> returning -> available
             |           ^                        |
             v           +------------------------+
           failed
```

- `preparing` owns creation and dependency preparation.
- `assigned` has one assignment ID, owner, host, and expiry.
- `returning` prevents reassignment while release cleans recorded processes.
- `available` is reusable and has no active assignment.
- `failed` records incomplete preparation for safe inspection and collection.

State lives under the repository's Git common directory. Existing dependency
preparation records remain intact. Lifecycle mutations use the same atomic,
host-local state lock as other Ruk state changes.

## Operations

Acquire atomically reserves a deterministic compatible `available` workspace,
or records a new workspace as `preparing`, creates and prepares it, then marks
it `assigned`. A preparation failure moves only that workspace to `failed`.
Every assignment receives a new UUID, including reuse.

Renew accepts only the current assignment ID in `assigned` state and replaces
its renewal and expiry timestamps. Release performs an ID-fenced transition to
`returning`, gracefully stops processes recorded for that assignment, cleans
the worktree, and then makes it `available`. If a process survives or the tree
is dirty, release preserves the assignment for retry. Explicit forced release
may kill surviving tracked process trees and discard tracked and untracked
changes. All ignored files are also removed before pooling. It still enforces
the assignment fence, so a delayed
command cannot act on a reassigned workspace.

`ruk run` registers its child only for an assigned workspace in managed mode
and removes the record when the child exits. Ruk does not infer ownership from
the operating-system process table.

GC selects stale `available` and `failed` records using the explicit age
cutoff. It reports expired `assigned` and `returning` records separately.
Expired assignments are not reclaimed unless the caller applies the plan and
explicitly enables forced expired cleanup.

## Validated safety properties

- A stale assignment ID cannot renew, release, or mutate process records.
- Reservation and assignment changes are serialized, preventing double
  assignment on one host.
- Expiry alone never reclaims active state.
- Dry-run GC performs no deletion.
- Ordinary GC never removes assigned or returning workspaces.
- Release and GC act only on Ruk-managed records; arbitrary orphan cleanup is
  not claimed.
