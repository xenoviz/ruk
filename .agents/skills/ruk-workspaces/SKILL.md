---
name: ruk-workspaces
description: Manage Ruk workspaces for coding agents. Use when an agent must acquire, warm, run in, inspect, renew, release, or measure a dependency-ready Git workspace.
---

# Ruk Workspaces

## Workflow

1. Run `ruk acquire <branch> --owner <stable-agent-id> --json` from the source
   repository. Add `--from`, `--fetch`, `--ttl`, or named `--port` requests only
   when needed. Fetch is explicit because it performs network I/O; without
   `--from`, it uses the primary remote's advertised default branch. A fully
   qualified remote-tracking ref fails if its named remote is missing.
2. Parse and retain `path`, `assignmentId`, `expiresAt`, and `ports`. Treat the
   assignment ID as opaque; never derive it from the path.
3. Set the working directory to the returned path. Use `ruk run -- <command>`
   for agent processes and `ruk sync --json` after dependency inputs change.
4. Inspect with `ruk status --json`. Renew before expiry with
   `ruk renew <assignmentId> --json` when work continues.
5. Finish with `ruk release <assignmentId> --json`, even when the workspace was
   reused. Report a release failure instead of substituting another ID.

For a short clean job, use `ruk exec <branch> -- <command>` to acquire, run,
and release automatically. If the command leaves changes or cleanup fails, Ruk
retains the assignment and prints the exact recovery ID. It also retains the
assignment when child identity cannot be established while descendants remain;
leaderless POSIX process groups retain the assignment rather than being signaled;
failed registration also terminates the detached group after a short-lived leader exits.
On Windows it terminates the new tree only with a verified leader identity and
otherwise retains the assignment while descendants remain or the leader PID is reused.
Ruk fences the assignment again immediately before launching a command.
Managed detached `run` and `exec` commands forward `SIGINT` and `SIGTERM` to
their POSIX process group and preserve conventional 130/143 exit codes. Ordinary
release is rejected until acquisition handoff finishes.
Inspect that process tree before forcing release. Use `ruk shell <branch>` for
an interactive, terminal-attached assigned shell; its isolated terminal session
keeps surviving descendants tracked after the shell exits (by session ID on
Linux and a live, identity-fenced controlling-terminal sentinel on macOS).
Leaderless Linux session records fail closed rather than signaling a reused ID.
Non-interactive shell input skips PTY allocation so EOF remains observable and
forwards interrupts to its detached process group. Commit intended work before
exit so normal release can succeed. Release integrity-validates recorded dependency projections:
unchanged projections stay warm, while modified projections are discarded and
rebuilt from the package store before the next assigned command. Warm capacity
counts only projections whose dependency inputs and integrity fingerprint still
validate, including linked package targets. `ruk status --json` reports
`projection-changed` and recommends `ruk sync` when integrity validation fails.

Use `ruk warm --count <n> --json` before a known burst of agents. The count is
the desired number of available prepared workspaces, not the number to add;
acquisition and warm-capacity reservations are serialized.
Use `ruk stats --json` for recorded reuse and preparation metrics; add `--disk`
only when an on-demand filesystem scan is acceptable. Available capacity omits
workspaces fenced by collection, both in statistics and warm-pool counts.

Named ports are cooperative host-local reservations. `--port app` returns an
`app` field and makes `RUK_PORT_APP` available to `ruk run`, `exec`, and
`shell`. The stable per-user registry is owner-only and fails closed on unsafe
or corrupt state, regardless of per-process temporary-directory settings.
Ruk does not hold the socket against unrelated processes.
Allocation probes dual-stack IPv6 when the host supports it and falls back to IPv4.

Use `ruk gc --json` to preview collection. Apply only when requested with
`ruk gc --apply --json`. Add `--force-expired` only with explicit authority to
reclaim expired assignments; expiry alone does not make them safe to remove.
Forced collection revalidates expiry atomically before changing lifecycle state.
GC recovers interrupted preparations, pre-handoff acquisitions, and collections
after the age cutoff; it revalidates handoff state under the acquisition lock
and preserves recovery markers after failed cleanup. Workspace and warm locks
prevent recovery from racing live operations, including forced expiry cleanup.
Warm and GC also share a pool-maintenance lock, so reported capacity cannot be
removed by an already-running collection.
GC revalidates each candidate under its acquisition lock and carries that fence
through the lifecycle transition. A renewal made during acquisition handoff is preserved.
An unreadable identity for a live lock owner is treated as busy, never stale.

Ruk coordinates one host and cleans only processes and workspaces it recorded.
Do not claim multi-host locking or arbitrary orphan discovery. Read
`docs/agent-interface.md` for exact JSON fields and stdout/stderr rules.
When a JSON command fails, parse the JSON error from stderr and use its stable
`code` and `retryable` fields; process-enumeration failures are retryable
`RESOURCE_BUSY` errors, and stdout contains no success record. JSON-mode
dependency installers have their output discarded to keep memory bounded.
Shared-backend version failures are retryable dependency-preparation errors.
Active acquisition handoffs are retryable `RESOURCE_BUSY` errors; unknown
configuration keys, malformed `.rukrc.json`, and invalid TTL ranges are
non-retryable `INVALID_ARGUMENT` errors. Interactive Linux shells require the
util-linux `script` command, which Ruk checks before acquiring a workspace.
Forced GC reports only expired assignments that remain active after collection.
Explicit shorthand `remote/branch` fetches reject missing remotes unless the
start point is an existing local branch.
