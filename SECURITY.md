# Security policy

## Supported versions

Security fixes are provided for the latest published minor release.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private
security advisory reporting for `xenoviz/ruk`. Include the affected command,
platform, reproduction steps, impact, and any proposed mitigation.

## Security model

Ruk executes Git and the repository's package manager with the current user's
permissions. Repository manifests and lifecycle scripts are executable input;
use Ruk only with repositories and dependency changes you trust.

Shared mode increases the impact of a malicious or mutating package because
immutable package content is reused. Managed mode is therefore the default.
Ruk never shares a writable workspace-level `node_modules` directory.

Self-update is opt-in and contacts only the canonical `xenoviz/ruk` GitHub
release endpoint. Standalone updates enforce canonical asset URLs, download
size limits, and SHA-256 integrity before replacement. Release CI also records
signed GitHub build provenance. A checksum establishes that a download matches
the release asset; users requiring publisher verification should additionally
verify the GitHub attestation before installing a binary.

The updater ignores releases without a valid `ruk-release.json` readiness
manifest. That manifest is published only after the npm package and every
attested executable are complete, preventing partially published releases from
being selected.
