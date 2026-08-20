# Install Ruk

Ruk ships as a native Go runtime through an npm package and standalone
executables. Choose one installation method; both expose the same `ruk`
command and JSON contract.

## Package installation

Install Ruk globally with npm:

```sh
npm install --global @xenoviz/ruk
```

You can also use Bun:

```sh
bun install --global @xenoviz/ruk
```

The package manager installs the matching optional native package for the host.
When lifecycle scripts are allowed, postinstall places the native binary
eagerly. When scripts are blocked, the first `ruk` invocation finishes that
placement and then runs the native command. Node.js or Bun may participate in
installation, but neither remains resident as the command supervisor afterward.

## Standalone executable

Download the executable for your operating system and architecture from the
[GitHub Releases page](https://github.com/xenoviz/ruk/releases). Standalone
executables are native Go binaries and do not require Node.js or Bun on the
target machine.

Release assets include SHA-256 checksum files and GitHub build-provenance
attestations. Verify the downloaded native Go asset before placing it on your
`PATH`. Linux x64 releases include both glibc and musl assets.

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
before replacement. On Windows, either installation mode schedules replacement
of locked native files after the current Ruk process exits. Stable installations
stay on stable releases; a current prerelease installation follows newer
prereleases on its channel.

Next, [create your first assigned workspace](/getting-started/).
