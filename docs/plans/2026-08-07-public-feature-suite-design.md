# Public feature suite design

## Goal

Complete Ruk's first public automation surface without adding a daemon. The
suite makes failures machine-readable, explains dependency readiness, supports
explicit remote refresh, offers short-lived and interactive workflows, warms
the workspace pool, allocates cooperative host-local ports, and reports
recorded usage statistics.

## CLI

```text
ruk status [--explain] [--json]
ruk acquire <branch> [--fetch] [--port <name>...] ...
ruk exec <branch> [acquire options] -- <command>
ruk warm --count <n> [--from <ref>] [--fetch] [--json]
ruk shell <branch> [acquire options]
ruk stats [--disk] [--json]
```

`--fetch` performs an explicit network operation before resolving `--from`.
Ordinary commands remain offline. `exec` and `shell` compose the normal
acquire, run, and release operations. They never force-release automatically;
if cleanup is unsafe, they retain the assignment and print its recovery data.

`warm` creates detached, prepared worktrees in the `available` state without
creating temporary branches. Acquisition reassigns those worktrees through the
existing fenced lifecycle.

## Errors and readiness

When `--json` is present, a failure writes one JSON object to stderr and exits
nonzero. The object contains `status: "error"`, a stable `code`, a human
message, `retryable`, and optional recovery details. Successful stdout remains
one JSON value.

Status reports explicit readiness reasons: `not-prepared`,
`dependencies-missing`, `fingerprint-changed`, and `projection-changed`.
`--explain` adds the
recommended recovery action in human output; JSON always includes the reason.

## Ports

Repeated `--port <name>` options request named ports. Names are normalized for
environment variables such as `RUK_PORT_APP`. Allocation checks the operating
system and excludes ports held by active Ruk assignments before recording the
selection under the assignment fencing token. Ruk does not hold sockets after
allocation, so reservations coordinate Ruk assignments but cannot exclude
unrelated processes. Renewal preserves reservations; release removes them.

## Statistics

State stores bounded aggregates rather than an event log: acquisitions,
workspace reuse, preparation runs, skipped preparation, failures, and elapsed
preparation time. Schema loading supplies safe defaults for old state while
preserving assignments.

`ruk stats` derives averages and rates from those counters. `--disk` scans
managed workspaces on demand and reports measured projection sizes, deduplicated
linked targets, and a clearly labelled `estimatedBytesAvoided` value. Ruk never
performs the filesystem scan during ordinary commands.

## Safety and recovery

All state changes use the existing common-directory lock and atomic state
replacement. Assignment IDs continue to fence renew, release, process records,
ports, and recovery. Garbage collection acquires the warm lock before treating
an interrupted warm preparation as abandoned, then fences removal with its
operation ID and update timestamp.

The implementation reuses the existing lifecycle, Git, dependency, process,
and state modules. It introduces no service, background process, arbitrary
process discovery, or cross-host claim.

## Validation

Tests cover state migration, structured failures, readiness reasons, fetch
behavior, concurrent port allocation, warm-pool creation, dirty `exec` and
`shell` recovery, statistics, and machine-readable output. Documentation, CLI
reference, configuration, and both maintained workspace skills change with the
public behavior.

The final check runs the repository's full validation suite and a dogfood flow:
warm, acquire with ports, run, inspect statistics, and release.
