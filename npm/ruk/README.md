# Ruk

Dependency-aware Git workspaces for parallel coding agents.

Ruk gives each agent its own Git worktree with dependencies ready, fences every
assignment with an immutable ID, and cleans up safely afterwards. A local web
dashboard shows every workspace on the machine.

## Install

```sh
npm install --global @xenoviz/ruk
```

`bun install --global @xenoviz/ruk` works too. The package installs a native
binary for Linux, macOS, or Windows on x64 or ARM64; Node.js is not kept
running once it is in place. Standalone executables are on
[GitHub Releases](https://github.com/xenoviz/ruk/releases).

## Use

```sh
ruk acquire agent/auth-flow --owner agent-17 --json
cd <returned-path>
ruk run -- bun test
ruk release <returned-assignmentId> --json
```

See every workspace, renew or release leases, and clean up from a browser:

```sh
ruk ui --open
```

Update in place with `ruk update`.

## Documentation

- [Guide and reference](https://xenoviz.github.io/ruk/)
- [Agent JSON contract](https://github.com/xenoviz/ruk/blob/main/docs/agent-interface.md)
- [Source and issues](https://github.com/xenoviz/ruk)

MIT licensed.
