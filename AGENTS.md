# Agent instructions

## Workspace quickstart

Use JSON output for automation and keep the returned path and assignment ID:
the path tells you where to work; the ID fences renew and release operations.

```text
ruk acquire agent/my-task --owner <stable-agent-id> --json
cd <returned-path>
ruk run -- <command> [args...]
ruk sync --json
ruk status --json
ruk renew <returned-assignmentId> --json
ruk release <returned-assignmentId> --json
```

Renew before `expiresAt` when work continues. Release the exact assignment ID
when finished; never infer an assignment from a path. See
[docs/agent-interface.md](docs/agent-interface.md) for the JSON contract and
[the lifecycle design](docs/plans/2026-08-03-workspace-lifecycle-design.md) for
safety boundaries.

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
- Keep source and tests in strict TypeScript and the published runtime free of
  third-party dependencies.
- Run `bun run check`, `bun run test:coverage`, `bun run binary:check`,
  `bun run binary:cross-check`, and `bun run pack:check` before handoff.
- Do not weaken coverage thresholds, repository rules, immutable action pins,
  or release provenance to make a change pass.
