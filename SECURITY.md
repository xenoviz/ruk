# Security Policy

## Supported Versions

Security fixes are provided for the latest stable release. Reports affecting the
current `main` branch are also welcome when they describe a vulnerability that
would affect a future release.

## Reporting a Vulnerability

Please report vulnerabilities through
[GitHub private vulnerability reporting](https://github.com/xenoviz/ruk/security/advisories/new).

Do not open a public issue for an undisclosed vulnerability. Include the affected
version and platform, realistic impact, reproduction steps, and any suggested
mitigation. Please allow reasonable time for investigation and remediation before
public disclosure.

## Security Scope

Security reports are especially relevant when Ruk could:

- modify or delete files outside a managed workspace or dependency projection;
- release, reuse, or collect a workspace without the correct assignment fence;
- signal a process that Ruk cannot tie to the recorded assignment;
- expose or corrupt owner-only workspace, lock, or port-reservation state;
- accept an unverified update or release asset;
- contact an unexpected remote during an operation documented as local-only.

## Project Boundaries

Ruk coordinates workspaces and cooperative port reservations on one local host.
It does not provide distributed locking, multi-host coordination, or exclusive
socket reservation against unrelated processes.

Vulnerabilities in Git, Node.js, Bun, pnpm, npm, Yarn, or the operating system
should normally be reported upstream unless Ruk introduces an independently
exploitable condition or violates its documented safety boundaries.
