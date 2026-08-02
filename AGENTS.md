# Agent instructions

- Work on a feature branch; never push directly to `main`.
- Read `docs/architecture.md` before modifying lifecycle, state, locking, Git,
  dependency preparation, or release behavior.
- Preserve the safety invariants in that document.
- Keep runtime code dependency-free unless the user approves a justified
  architecture change.
- Add tests for success, failure, concurrency, and machine-readable output when
  changing public behavior.
- Use Bun 1.3.14 and the committed `bun.lock`; do not add another package
  manager lockfile.
- Keep source and tests in strict TypeScript and the published runtime free of
  third-party dependencies.
- Run `bun run check`, `bun run test:coverage`, `bun run binary:check`,
  `bun run binary:cross-check`, and `bun run pack:check` before handoff.
- Do not weaken coverage thresholds, repository rules, immutable action pins,
  or release provenance to make a change pass.
