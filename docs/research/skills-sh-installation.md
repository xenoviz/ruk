# How skills.sh installs skills

Verified 2026-08-04 against `skills` CLI 1.5.21, the official skills.sh documentation, and the `vercel-labs/skills` source repository.

## Canonical install syntax

The basic source identifier is a GitHub repository in `owner/repo` form:

```sh
npx skills add owner/repo
```

To install one named skill from a multi-skill repository, skills.sh's individual skill pages use a full repository URL plus `--skill`:

```sh
npx skills add https://github.com/owner/repo --skill skill-name
```

The CLI also accepts `owner/repo@skill-name`, although the published installation cards use `--skill`. Other supported sources are a full GitHub URL, a GitHub subtree URL, a GitLab URL, any Git URL (including SSH), a local directory, or a direct `SKILL.md`/archive download URL. Use `--list` to see discoverable skills before installing. [Official CLI README: source formats and options](https://github.com/vercel-labs/skills#source-formats), [official source parser: `owner/repo@skill`](https://github.com/vercel-labs/skills/blob/main/src/source-parser.ts#L348-L368), [example skills.sh installation card](https://www.skills.sh/0xbigboss/claude-code/react-best-practices)

For Ruk, the corresponding commands are:

```sh
# Project-local (default)
npx skills add https://github.com/xenoviz/ruk --skill ruk-workspaces --agent codex

# User-wide
npx skills add https://github.com/xenoviz/ruk --skill ruk-workspaces --agent codex --global
```

`-y`/`--yes` skips prompts. `--copy` copies files; otherwise the interactive installer recommends symlinks from agent directories to one canonical copy. `--all` means all discovered skills, all agents, and no prompts. [Official CLI README: options and installation methods](https://github.com/vercel-labs/skills#options)

## Project versus global installation

- Project scope is the default and installs under the selected agent's project skills directory.
- `-g`/`--global` installs for the current user instead.
- For Codex specifically, the documented project directory is `.agents/skills/` and the global directory is `~/.codex/skills/`.

The CLI detects installed agents and prompts for a target when none is detected; `-a`/`--agent` selects targets explicitly and `--agent '*'` selects every target. [Official scope table](https://github.com/vercel-labs/skills#installation-scope), [official supported-agent matrix](https://github.com/vercel-labs/skills#supported-agents)

The current source documents 76 `--agent` identifiers: `aider-desk`, `amp`, `antigravity`, `antigravity-cli`, `astrbot`, `autohand-code`, `augment`, `bob`, `claude-code`, `openclaw`, `cline`, `codearts-agent`, `codebuddy`, `codemaker`, `codestudio`, `codex`, `command-code`, `continue`, `cortex`, `crush`, `cursor`, `deepagents`, `devin`, `dexto`, `droid`, `eve`, `firebender`, `forgecode`, `gemini-cli`, `github-copilot`, `goose`, `grok`, `hermes-agent`, `inference-sh`, `jazz`, `junie`, `iflow-cli`, `kilo`, `kimchi`, `kimi-code-cli`, `kiro-cli`, `kode`, `lingma`, `loaf`, `mcpjam`, `minimax-code`, `mistral-vibe`, `moxby`, `mux`, `opencode`, `openhands`, `ona`, `pi`, `qoder`, `qoder-cn`, `qwen-code`, `replit`, `reasonix`, `rovodev`, `roo`, `tabnine-cli`, `terramind`, `tinycloud`, `trae`, `trae-cn`, `warp`, `windsurf`, `zed`, `zcode`, `zencoder`, `zenflow`, `neovate`, `pochi`, `promptscript`, `adal`, and `universal`. [Official agent definitions](https://github.com/vercel-labs/skills/blob/main/src/agents.ts)

## How a repository exposes a skill

A skill is a directory containing `SKILL.md`. The file needs YAML frontmatter with at least `name` and `description`:

```md
---
name: ruk-workspaces
description: Manage Ruk workspaces for coding agents.
---

# Ruk workspaces
```

The simplest supported layouts are a root `SKILL.md`, `skills/<name>/SKILL.md`, or `.agents/skills/<name>/SKILL.md`; Ruk already uses the last form. The CLI also searches its documented agent-specific directories, supports one extra category level such as `skills/<category>/<name>/SKILL.md`, and falls back to recursive discovery when standard locations yield nothing. A shallower `SKILL.md` shadows nested files unless `--full-depth` is used. [Official skill format and discovery rules](https://github.com/vercel-labs/skills#creating-skills)

Publishing requires no skills.sh manifest: push the repository, then install it by Git source. The CLI's own `skills init` output says to publish to GitHub and run `npx skills add <owner>/<repo>`. [Official CLI implementation](https://github.com/vercel-labs/skills/blob/main/src/cli.ts#L442-L482)

## Updates and checks

```sh
npx skills update                 # all, with an interactive scope prompt
npx skills update skill-name      # one installed skill
npx skills update -g              # global only
npx skills update -p              # project only
npx skills update -y              # non-interactive scope auto-detection
```

`upgrade` is an alias for `update`. The current public help and README document `update`, not a separate read-only check command. Although `skills check` is still accepted, current source dispatches `check`, `update`, and `upgrade` to the same `runUpdate` function, which can reinstall changed skills; therefore documentation should not present `check` as read-only. [Official update documentation](https://github.com/vercel-labs/skills#skills-update), [official command dispatch](https://github.com/vercel-labs/skills/blob/main/src/cli.ts#L714-L720), [official update implementation](https://github.com/vercel-labs/skills/blob/main/src/update.ts#L820-L858)
