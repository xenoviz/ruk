# Agent integration

Use Ruk's JSON output as the boundary between the workspace coordinator and the
agent. Do not scrape human-readable output.

For a ready-made instruction package, use the maintained
[Ruk workspace skill](/skills/).

## Required lifecycle

```text
acquire -> work at returned path -> managed work auto-renews -> release exact ID
```

1. Call `ruk acquire <branch> --owner <stable-id> --json` from the source
   repository.
2. Parse and retain `path`, `assignmentId`, and `expiresAt`.
3. Set the agent's working directory to `path`.
4. Launch commands with `ruk run -- ...`.
5. Call `ruk sync --json` after dependency files change.
6. Managed commands renew automatically. Explicitly renew long idle work that
   continues outside a Ruk operation.
7. Commit or export intended work, then release the exact assignment ID.

For a short command that leaves a clean tree, the composed form performs the
same lifecycle:

```sh
ruk exec agent/check --owner agent-17 -- bun test
```

If safe release fails, `exec` retains the assignment and prints the path, ID,
expiry, and recovery command.

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
to stdout. Progress and installer output go to stderr. A failure exits nonzero,
writes a JSON error with stable `code` and `retryable` fields to stderr, and
does not emit a success record.

Consumers must ignore unknown object fields. Within a major release, documented
fields keep their names, types, and meanings.

## Process ownership

Only processes launched through `ruk run` are recorded. Ruk does not discover
commands started directly in another terminal or arbitrary daemons. The agent
runner remains responsible for those processes.

While a managed command is active, Ruk maintains a fenced lease keeper. Inspect
`lastActivityAt` and `autoRenewing` through status JSON. If keeper renewal loses
the assignment fence, Ruk stops the tracked command instead of allowing it to
continue under uncertain ownership.

Run task commands in the acquired path. The primary checkout denies `ruk run`
and `ruk sync` while assignments are active unless repository policy or
`--allow-shared-checkout` explicitly permits sharing.

## Named ports

Repeated `--port <name>` options reserve ports among active Ruk assignments.
The acquire response contains the mapping, and `ruk run` injects normalized
variables such as `RUK_PORT_APP`. Ruk does not hold sockets or exclude
unrelated host processes.

## Failure handling

- If acquisition fails, do not guess a workspace path or assignment ID.
- If renewal fails, assume the fence is stale and inspect current state.
- If release fails because the tree is dirty, save the work before retrying.
- Use forced release only when discarding workspace changes is intentional.
- Report a release failure; never substitute an ID derived from a path.

See the [JSON contracts](/reference/json) for response shapes.
