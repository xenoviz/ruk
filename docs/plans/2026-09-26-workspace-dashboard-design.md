# Workspace dashboard design

## Status

Approved design for Ruk 0.5. This document defines `ruk ui`, a local web
dashboard for viewing and managing Ruk workspaces across repositories.

## Problem

Ruk exists so many agents can work in parallel, but its state is only visible
one repository and one command at a time. Answering "which agents hold
workspaces, which leases are about to expire, what failed, and what would GC
remove" means running `list`, `status`, and `gc` in every repository and
reading JSON. A person supervising several agents needs one view of the whole
machine and a safe way to act on what it shows.

## Goals

- Show every Ruk workspace on the host, grouped by repository, with lifecycle,
  owner, lease countdown, automatic renewal, ports, tracked processes, and the
  recorded failure.
- Offer the common interventions: renew, release, force-release an expired
  lease, remove an unmanaged worktree, preview and apply GC, and measure disk.
- Ship inside the existing binary with no runtime dependencies, on every
  supported platform and distribution.
- Change no lifecycle rule. Every action must behave exactly as the matching
  command does.

## Non-goals

- A daemon, background renewal, or any activity inferred from the dashboard
  being open. Viewing never renews a lease.
- Remote or multi-user access. The dashboard serves one person on one machine.
- An event history. Ruk keeps bounded counters, not an event log, and the
  dashboard does not add one.
- Running commands, acquiring workspaces, or opening shells from the browser.
- Per-workspace disk usage. `stats --disk` measures a repository on request,
  and the dashboard does the same.

## Command

```text
ruk ui [--port <number>] [--open] [--json]
```

`ruk ui` binds `127.0.0.1` on a free port (or `--port`), prints a one-time
address, and serves until interrupted. `--open` asks the operating system to
open the address; failure to open a browser is a warning, not an error.
`--json` prints one value, `{"status":"listening","address":…,"url":…}`, so
automation can find the address.

## Architecture

`internal/dashboard` owns the HTTP boundary: session checks, the embedded page,
and a two-endpoint JSON API. It depends on a `Source` interface and knows
nothing about Git or state.

`internal/cli` implements the source:

- **Snapshot.** Repositories come from the host index plus the checkout the
  command started in, deduplicated by common Git directory. For each one the
  source reads state and `git worktree list` once and builds records with the
  same `BuildListResponse` that `ruk list` uses, then adds owner, lease length,
  ports, tracked processes, and failure from the same state snapshot. The
  primary checkout and worktrees Ruk neither manages nor created are omitted.
  A repository that cannot be read reports its error without hiding the rest.
  The page polls every three seconds.
- **Actions.** Each action maps to one CLI invocation that runs in-process on a
  copy of the application with private output buffers:

  | Action       | Command                                   |
  | ------------ | ----------------------------------------- |
  | `renew`      | `ruk renew <id> [--ttl <minutes>] --json` |
  | `release`    | `ruk release <id> [--force] --json`       |
  | `remove`     | `ruk remove <path>`                       |
  | `gc-preview` | `ruk gc --json`                           |
  | `gc-apply`   | `ruk gc --apply [--force-expired] --json` |
  | `disk`       | `ruk stats --disk --json`                 |

  The requested repository must be one the snapshot reports. The host index
  never authorizes a mutation: the source re-discovers the repository through
  Git, and the command then applies its own discovery, locks, fences, and
  validation. Failures return the CLI's JSON error record unchanged.

Commands run from the directory `ruk ui` started in when that directory belongs
to the target repository, otherwise from the primary checkout. This keeps the
rule that the current workspace cannot remove itself.

Renew sends the assignment's stored lease length so a short lease is not
silently extended to the 480-minute CLI default. Pool workspaces have no remove
action; they leave through GC, as they do from the command line. GC applies its
whole plan per repository because `ruk gc` has no per-item selection.

## Security

The dashboard adds a network listener to a tool that manages processes and
deletes directories, so the boundary is strict:

- The listener binds loopback only, and the handler rejects any address that
  is not loopback.
- Every request must name the exact host and port the server listens on
  (`127.0.0.1:<port>` or `localhost:<port>`), which defeats DNS rebinding.
- The printed URL carries a 256-bit random token. The first request exchanges
  it for an `HttpOnly`, `SameSite=Strict` cookie and redirects so the token
  leaves the address bar. Every other request requires the cookie, compared in
  constant time.
- Actions additionally require a matching `Origin` header and a JSON body, so
  a page on another site cannot submit them.
- Action requests are limited to 64 KiB, reject unknown fields and trailing
  values, and are validated against their kind before any command runs.
- Actions run one at a time. Snapshots stay available while an action runs.
- Responses carry a content security policy that allows only the dashboard's
  own files, plus `nosniff`, `DENY` framing, no referrer, and `no-store`.
- Destructive actions ask for confirmation in the page. Force release and
  forced GC are separate, explicitly labeled choices.

On shutdown the server stops accepting requests and gives a running action the
grace period to finish rather than cancelling it mid-transition.

## Page

The page is plain HTML, CSS, and JavaScript embedded with `go:embed`; there is
no build step and nothing loads from the network. It uses two embedded
typefaces under the SIL Open Font License, DM Sans for text and Space Grotesk
for headings, with their licenses beside them. Colour encodes state only. The
workspace table sorts expired and failed workspaces first, and a docked panel
shows details, actions, and the equivalent CLI command.

## Testing

- `internal/dashboard`: host, token, cookie, origin, and content-type checks;
  security headers on every response, including rejections; request
  validation; error mapping; one-at-a-time actions under concurrent load with
  snapshots still served; graceful shutdown; browser opener arguments.
- `internal/cli`: grammar and port parsing; snapshot filtering and enrichment;
  per-repository errors; argument mapping, with each mapped invocation checked
  against the CLI parser; unknown repositories; command failures as error
  records; a real `renew` running through `Application.Run`; and `ruk ui
  --json` serving until cancelled.
- The conformance golden's help text includes the new command.
