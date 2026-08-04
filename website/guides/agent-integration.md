# Agent integration

Use Ruk's JSON output as the boundary between the workspace coordinator and the
agent. Do not scrape human-readable output.

For a ready-made instruction package, use the maintained
[Ruk workspace skill](/skills/).

## Required lifecycle

```text
acquire -> work at returned path -> renew when needed -> release exact ID
```

1. Call `ruk acquire <branch> --owner <stable-id> --json` from the source
   repository.
2. Parse and retain `path`, `assignmentId`, and `expiresAt`.
3. Set the agent's working directory to `path`.
4. Launch commands with `ruk run -- ...`.
5. Call `ruk sync --json` after dependency files change.
6. Renew before `expiresAt` when work continues.
7. Commit or export intended work, then release the exact assignment ID.

## Example orchestration

```sh
assignment=$(ruk acquire agent/auth-flow --owner agent-17 --json)
```

Parse JSON with your host language rather than shell text tools. Treat the
assignment ID as opaque.

```text
workspace = assignment.path
fence = assignment.assignmentId

run(cwd=workspace, command=["ruk", "run", "--", "bun", "test"])
run(cwd=workspace, command=["ruk", "release", fence, "--json"])
```

## Output rules

With `--json`, a successful command writes one JSON value and a trailing newline
to stdout. Progress, installer output, and diagnostics go to stderr. A failure
exits nonzero and does not emit a success record.

Consumers must ignore unknown object fields. Within a major release, documented
fields keep their names, types, and meanings.

## Process ownership

Only processes launched through `ruk run` are recorded. Ruk does not discover
commands started directly in another terminal or arbitrary daemons. The agent
runner remains responsible for those processes.

## Failure handling

- If acquisition fails, do not guess a workspace path or assignment ID.
- If renewal fails, assume the fence is stale and inspect current state.
- If release fails because the tree is dirty, save the work before retrying.
- Use forced release only when discarding workspace changes is intentional.
- Report a release failure; never substitute an ID derived from a path.

See the [JSON contracts](/reference/json) for response shapes.
