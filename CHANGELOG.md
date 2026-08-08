# Changelog

All notable changes will be documented here. Releases follow semantic
versioning.

## 0.1.0 - 2026-08-08

- Establish Ruk as an independent dependency-aware Git workspace manager.
- Add automatic Bun, pnpm, npm, Yarn, and custom install handling.
- Share immutable package content by default for supported Bun and pnpm
  versions, with managed mode available as an opt-out.
- Add dependency fingerprinting, atomic state, and per-workspace locking.
- Add cross-platform CI, coverage and package smoke gates, protected-main
  policy, and npm trusted-publishing workflow.
- Author the project in strict TypeScript with Bun as the pinned repository
  toolchain while retaining a compiled, dependency-free Node.js package.
- Publish standalone Linux, macOS, and Windows executables for x64 and ARM64
  with SHA-256 checksums.
- Add explicit self-update checks, package-manager delegation, verified atomic
  executable replacement, rollback protection, and signed build provenance.
- Add an advanced update-installer environment override, manifest-gated release
  readiness, one coordinated release workflow, and real Windows
  release-upgrade smoke validation.
- Add warm pools, assigned one-command execution, interactive shells, explicit
  remote refresh, and cooperative named port reservations.
- Add structured JSON failures, readiness reasons, bounded reuse and
  preparation statistics, and optional disk estimates.
- Accept separator-free `ruk run` commands for PowerShell npm shims and tolerate
  short-lived Windows children that exit before process identity inspection.
- Integrity-validate recorded dependency projections during pool cleanup so
  unchanged workspaces skip installation while modified projections are
  discarded before reassignment, including mutations behind package links.
- Keep background POSIX process groups tracked after their leader exits, make
  port-registry cleanup recoverable, and tolerate concurrent disk-stat races.
- Resolve fetched remote defaults, fence commands at launch, keep shells
  terminal-attached, and fail closed on corrupt active port reservations.
- Revalidate forced-GC expiry, restore ownership after failed acquisition
  cleanup, serialize warming with acquisition, and secure the host registry.
- Track interactive shell descendants by POSIX session on Linux and by their
  isolated controlling terminal on macOS.
- Recover interrupted acquire preparations, keep failed-reuse projection
  metadata, retain surviving Windows process records, stabilize host port
  reservations, and deduplicate nested linked targets in disk statistics.
- Fence acquisition handoff and collection recovery, re-lock failed removals,
  fail closed on process-enumeration errors, and preserve EOF for
  non-interactive POSIX shells.
- Serialize abandoned-acquisition recovery with live handoff, preserve its
  retry marker after failed cleanup, and forward non-interactive shell signals.
- Publish pool reservations only under their handoff lock, preserve assignment
  identity across repair, harden process inspection and registration cleanup,
  terminate detached descendants after failed registration, classify process
  enumeration failures as retryable resource contention,
  bound JSON installer output, validate explicit remotes, and report only
  reservable warm capacity.
- Reject commands from unassigned pool slots, exclude collection-fenced slots
  from warm capacity, and serialize forced-expiry cleanup with live handoff.
- Block release during acquisition handoff, forward detached command interrupts,
  and fail closed on leaderless sessions or unreadable live lock identities.
- Reject symlinked projection ancestors, leaderless reused groups, and ambiguous
  default remotes; probe dual-stack ports and classify backend version failures.
- Forward managed `SIGTERM` signals and classify handoff/configuration failures
  for reliable JSON automation.
- Serialize warm capacity with GC, bound linked-target scans, and omit collected
  assignments from expired recovery output.
- Validate shorthand remotes, recompute expired output after GC, and preserve
  conventional signal exit codes.
- Revalidate GC candidates, preserve handoff renewals, and fail closed for
  leaderless or reused Windows process trees.
- Fence Windows registration cleanup by process identity and skip forced-GC
  candidates whose leases change after revalidation.
- Carry abandoned-acquisition update fences into GC lifecycle transitions.
- Validate Linux interactive-shell prerequisites and oversized TTLs before
  acquisition, and classify malformed configuration as invalid input.
