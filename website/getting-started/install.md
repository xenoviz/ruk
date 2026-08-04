# Install Ruk

Ruk ships as a Node-compatible npm package and as standalone executables. Choose
one installation method; both expose the same `ruk` command.

## Package installation

Install globally with npm:

```sh
npm install --global @xenoviz/ruk
```

You can also use Bun. The package distribution still requires Node.js 22.14 or
newer at runtime.

```sh
bun install --global @xenoviz/ruk
```

## Standalone executable

Download the executable for your operating system and architecture from the
[GitHub Releases page](https://github.com/xenoviz/ruk/releases). Standalone
executables embed Bun and do not require Node.js or Bun on the target machine.

Release assets include SHA-256 checksum files and GitHub build-provenance
attestations. Verify the downloaded asset before placing it on your `PATH`.

::: info Before the first release
Ruk 0.1.0 is not published yet. Package and binary installation becomes
available when the first release completes.
:::

## Verify the installation

```sh
ruk --version
ruk --help
```

Ruk also needs Git and the package manager declared by each repository it
manages.

## Update later

Ruk never checks for updates in the background.

```sh
ruk update --check
ruk update
```

Package installations delegate the exact released version to their package
manager. Standalone installations verify the release manifest and checksum
before replacement.

Next, [create your first assigned workspace](/getting-started/).
