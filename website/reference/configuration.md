# Configuration

Create `.rukrc.json` in the repository root. Ruk rejects unknown keys.

## `dependencyMode`

```json
{
  "dependencyMode": "shared"
}
```

Accepted values are `managed` and `shared`. Automatically detected Bun and pnpm
projects default to `shared`; other detected package managers default to
`managed`. A custom `installCommand` always defaults to `managed`, regardless
of its executable name. Select `shared` explicitly only when a custom Bun or
pnpm command produces a compatible shared layout.

## `installCommand`

```json
{
  "installCommand": ["npm", "ci"]
}
```

The value must be a non-empty array of non-empty strings. Ruk invokes it
directly without a shell. Supplying this option disables dependency-mode
auto-detection, so its default mode is `managed` unless `dependencyMode` is also
set.

## Environment variables

| Variable | Purpose |
| --- | --- |
| `RUK_AGENT_ID` | Default assignment owner when `--owner` is absent. |
| `RUK_DEPENDENCY_MODE` | Temporarily override `dependencyMode` with `managed` or `shared`. |
| `RUK_INSTALL_COMMAND` | Temporarily override the installer with a JSON string array. |
| `RUK_UPDATE_INSTALLER` | Force `npm`, `bun`, `pnpm`, or `yarn` for package self-update. |

Example for a temporary custom installer:

```sh
RUK_INSTALL_COMMAND='["npm","ci"]' ruk sync --json
```

Environment values override `.rukrc.json`. Use them for temporary automation;
commit stable repository policy to `.rukrc.json`.

## `sharedCheckoutPolicy`

```json
{
  "sharedCheckoutPolicy": "deny"
}
```

Accepted values are `deny`, `warn`, and `allow`; the default is `deny`. When
the primary checkout has active Ruk assignments, the policy controls `ruk run`
and `ruk sync` there. Pool and inspection commands remain available. Use
`--allow-shared-checkout` for a single intentional task command without
changing repository policy.

The guard coordinates Ruk commands. It cannot prevent direct filesystem writes
or Git commands, so treat the primary checkout as a control location.

## Package-manager detection

Without `installCommand`, Ruk reads the `packageManager` field, then checks known
lockfiles. It supports Bun, pnpm, Yarn, and npm. The selected executable must be
available on `PATH`.
