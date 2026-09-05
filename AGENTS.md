# Agent instructions

## Workspace management

Before workspace operations, read the [Ruk skill](.agents/skills/ruk-workspaces/SKILL.md).
Use JSON for automation, work in the returned path, retain the opaque assignment
ID, and release that exact ID when finished. Never infer ownership from a path.
Managed operations renew while active; explicitly renew long idle work.
Never force release or garbage collection without explicit user authorization.
See the [JSON contract](docs/agent-interface.md) and
[lifecycle safety boundaries](docs/plans/2026-08-03-workspace-lifecycle-design.md).

## Repository development

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
- Keep the Go runtime dependency-free and supporting TypeScript tooling strict.
- Run `bun run check`, `go test ./...`, `go test -race ./...`,
  `bun run test:conformance`, `bun run binary:check`,
  `bun run binary:cross-check`, and `bun run pack:check` before handoff.
- Do not weaken coverage thresholds, repository rules, immutable action pins,
  or release provenance to make a change pass.
