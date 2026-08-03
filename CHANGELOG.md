# Changelog

All notable changes will be documented here. Releases follow semantic
versioning.

## 0.1.0 - Unreleased

- Establish Ruk as an independent dependency-aware Git workspace manager.
- Add managed-by-default Bun, pnpm, npm, Yarn, and custom install handling.
- Add opt-in Bun and pnpm shared-store backends.
- Add dependency fingerprinting, atomic state, and per-workspace locking.
- Add cross-platform CI, coverage and package smoke gates, protected-main
  policy, and npm trusted-publishing workflow.
- Author the project in strict TypeScript with Bun as the pinned repository
  toolchain while retaining a compiled, dependency-free Node.js package.
- Publish standalone Linux, macOS, and Windows executables for x64 and ARM64
  with SHA-256 checksums.
- Add explicit self-update checks, package-manager delegation, verified atomic
  executable replacement, rollback protection, and signed build provenance.
