# Ruk workspace skill

Ruk includes a maintained agent skill at
`.agents/skills/ruk-workspaces/SKILL.md`. It teaches coding agents to acquire a
prepared workspace, preserve the assignment fence, run tracked processes, and
release the workspace safely.

## Install the skill

Install with the [Skills CLI](https://skills.sh/docs/cli). It discovers
`ruk-workspaces` from Ruk's `.agents/skills` directory and installs it into the
selected agent's skills directory.

```sh
npx skills add https://github.com/xenoviz/ruk --skill ruk-workspaces
```

The CLI detects installed coding agents and prompts for the installation target.
Use flags for a repeatable, non-interactive install.

### Install for one project

Project scope is the default. Run the command from the project that will use
Ruk:

```sh
npx skills add https://github.com/xenoviz/ruk --skill ruk-workspaces --agent codex --yes
```

For Claude Code, replace `codex` with `claude-code`.

### Install globally

Add `--global` to make the skill available across projects for the selected
agent:

```sh
npx skills add https://github.com/xenoviz/ruk --skill ruk-workspaces --agent codex --global --yes
```

The Skills CLI uses agent-specific directories. For example, project-scoped
Codex skills use `.agents/skills/`; global Codex skills use `~/.codex/skills/`.

### List and update

```sh
npx skills list
npx skills update ruk-workspaces
```

The skill assumes that `ruk` is installed on `PATH` and that the agent starts
inside a Git repository managed by Ruk.

## Invoke it

Ask the agent to use the skill when work needs an isolated Git workspace:

```text
Use the ruk-workspaces skill to implement this change on agent/auth-flow.
```

The agent should then follow this lifecycle:

```text
acquire -> work at returned path -> sync when dependencies change
        -> renew before expiry -> release the exact assignment ID
```

## Values the agent must retain

`ruk acquire --json` returns three values needed throughout the assignment:

| Field | Purpose |
| --- | --- |
| `path` | Absolute working directory for every subsequent command. |
| `assignmentId` | Opaque fence required by `renew` and `release`. |
| `expiresAt` | Renewal deadline for work that continues. |

The agent must never derive an assignment ID from a path or branch. Reusing a
workspace creates a new assignment ID.

## Command pattern

```sh
ruk acquire agent/auth-flow --owner agent-17 --json
cd <returned-path>
ruk run -- bun test
ruk sync --json
ruk renew <returned-assignmentId> --json
ruk release <returned-assignmentId> --json
```

Use `ruk run -- ...` for long-lived agent processes so Ruk can record and stop
them during release. Run `ruk sync --json` after changing a manifest, lockfile,
package-manager configuration, or patch.

## Author a compatible skill

A custom skill that delegates workspace management to Ruk should preserve these
rules:

1. Request JSON output for automation.
2. Change the working directory to the returned `path`.
3. Store the exact `assignmentId` and `expiresAt` values.
4. Launch owned processes through `ruk run -- ...`.
5. Renew before expiry when work continues.
6. Commit or export intended work before release.
7. Report release failures instead of guessing another assignment ID.

Keep domain-specific build or test instructions in the custom skill. Link to
the [agent integration guide](/guides/agent-integration) and
[JSON contracts](/reference/json) instead of copying their details.

## Safety boundaries

The skill coordinates one host. It does not provide cross-host locking, port
allocation, or discovery of processes started outside `ruk run`. Garbage
collection is a dry run unless explicitly applied; reclaiming expired
assignments also requires explicit force authority.
