# Changelog

All notable changes will be documented here. Releases follow semantic
versioning.

## 0.1.0 - Unreleased

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
- Preserve recorded dependency projections during pool cleanup so unchanged
  reassigned workspaces skip installation while other ignored files are removed.
