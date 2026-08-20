# Changelog

All notable changes will be documented here. Releases follow semantic
versioning.

## 0.3.0 - 2026-08-20

- Promote the Go-native 0.3 line to stable after the beta packaging, update, and
  install-script fixes.
- Authenticate GitHub release discovery with `GH_TOKEN` or `GITHUB_TOKEN` when
  present so shared CI runners are not blocked by unauthenticated API rate
  limits.
- Skip the Windows previous→current update smoke for the first Go-native stable
  instead of selecting TypeScript-era `0.1.x` executables under those rate
  limits.

## 0.3.0-beta.4 - 2026-08-20

- Make the npm `bin/ruk` entry a thin launcher that places the verified native
  binary on first use when lifecycle scripts are skipped, while keeping
  postinstall as an eager optimization when scripts are allowed.

## 0.3.0-beta.3 - 2026-08-19

- Keep standalone self-update downloads readable after the HTTP response
  returns, so a mid-stream `context canceled` can no longer abort the binary
  asset. Updates from 0.3.0-beta.1 and 0.3.0-beta.2 standalone executables
  required a manual reinstall because of this.

## 0.3.0-beta.2 - 2026-08-19

- Track every worktree created by `acquire`, `warm`, and `create` in a
  per-repository registry at `<git-common-dir>/ruk/worktrees.json`, removed
  again by `remove`, applied `gc`, and create-failure cleanup.
- Add `ruk worktrees [--all] [--json]` to list tracked worktrees for the
  current repository or, with `--all`, across every repository named by the
  new per-user discovery index at `~/.ruk/repositories.json`.
- Pin acquire and warm start points to an immutable commit in the invoking
  checkout before assignment, so a reused pool slot can no longer adopt its
  stale detached `HEAD`.
- Skip the Windows release-to-release update smoke when the current tag is a
  prerelease and no prior ready Windows executable exists on that channel,
  instead of expecting a stable install to jump onto beta.

## 0.3.0-beta.1 - 2026-08-16

- Replace the shipped TypeScript/Node command with one dependency-free native
  Go runtime while preserving the frozen CLI, JSON, state, and lifecycle
  contract.
- Publish native npm platform packages for Linux, macOS, and Windows on x64 and
  ARM64, with glibc and musl Linux x64 variants.
- Replace state atomically on Windows with bounded retries for transient file
  sharing, access, and lock violations.
- Drain native Windows Job Object descendants by querying their active-process
  count, avoiding wrappers that wait forever after a managed command exits.
- Automatically renew assignments during managed commands and dependency sync,
  using concurrent fenced keepers and observable activity timestamps.
- Guard task commands in a shared primary checkout by default, with repository
  `deny`, `warn`, and `allow` policies plus a one-command override.
- Expose activity, automatic-renewal, checkout, management, and active-assignment
  fields through status and list JSON.
- Add a repeatable Node/Go runtime benchmark with Linux and Windows raw evidence,
  RAM-reduction gating, child-process counts, and zero-PowerShell verification.
- Allow repository administrators to bypass branch rules only through pull
  requests, preventing sole-maintainer deadlock without allowing direct pushes;
  keep required CI in a separate non-bypassable ruleset.
- Default custom install commands to managed dependency mode unless shared mode
  is explicitly selected.
- Type-check documentation theme code, declare Vue directly, and include the
  documentation workflow in immutable-action validation.
- Pin release builds to the triggering commit, require it to descend from
  protected `main`, and protect version tags from updates and deletion.
- Restore a prominent link to private vulnerability-reporting instructions.
- Fence primary-checkout work against concurrent assignment publication, keep
  foreground shell descendants durably tracked, and fail closed when native
  process identity or cleanup cannot be proven.
- Include package-manager runtime and native ABI details in dependency
  fingerprints, stream human installer output with bounded diagnostics, and
  keep JSON-mode installers silent and memory-bounded.
- Harden garbage collection against current-workspace subdirectories and
  interrupted state removal, and keep prerelease updates on their current
  channel while discovering every paginated GitHub release.
- Preserve current-workspace `exec` routing, fence explicit workspace creation,
  parse newline-containing worktree paths safely, stream human package updates,
  preserve POSIX rollback modes, and defer locked Windows package replacement.
- Route Windows package-manager shims through `COMSPEC` without PowerShell and
  import active Ruk 0.2 host-port reservations before allocating new ports.
- Rescan dependency inputs after installation, reserve dual-stack ports across
  both address families, recycle shell acquisitions that fail before spawning,
  and return exact retained-acquisition recovery metadata without changing the
  underlying machine-readable failure category.
- Resolve package executable symlinks before update ownership detection,
  validate release projections inside the workspace fence, recover missing GC
  leaves without trusting dangling symlinks, preserve Windows rollback backups,
  and restore shared-backend dependency error classification.
- Fence assigned synchronization to its original lease, keep shells renewed,
  start requested leases after preparation, serialize reusable capacity with
  warm and GC, preserve fail-closed Linux identities, and bound Windows update
  replacement waits without discarding recovery artifacts.
- Supervise dependency installers with native process groups and Windows jobs,
  retain exact process records across canceled preparation, let fenced GC drain
  only identity-matching installer trees, and use microsecond kernel process
  identities on macOS. Windows cleanup performs a final bounded leaderless-tree
  drain without signaling newly discovered reusable PIDs.

## 0.1.2 - 2026-08-08

- Give the draft-release finalizer explicit repository context before checkout
  so tag-triggered releases can create their mutable draft reliably.

## 0.1.1 - 2026-08-08

- Publish immutable GitHub releases only after binaries, checksums, provenance,
  and the update-readiness manifest have been attached to a mutable draft.
- Correct the public installation guide now that the npm package is available.

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
