# Agent instructions

- Work on a feature branch; never push directly to `main`.
- Read `docs/architecture.md` before modifying lifecycle, state, locking, Git,
  dependency preparation, or release behavior.
- Preserve the safety invariants in that document.
- Keep runtime code dependency-free unless the user approves a justified
  architecture change.
- Add tests for success, failure, concurrency, and machine-readable output when
  changing public behavior.
- Run `npm run check`, `npm run test:coverage`, and `npm run pack:check` before
  handoff.
- Do not weaken coverage thresholds, repository rules, immutable action pins,
  or release provenance to make a change pass.
