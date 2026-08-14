# Cloud and CI verification split

## Goal

Ruk needs broad platform coverage without repeating every Linux check on every
runner. The cloud development environment provides a cached Ubuntu checkout
with Bun 1.3.14 and the committed lockfile. GitHub Actions remains the
authoritative, required gate because it runs automatically for every pull
request and supplies Windows and macOS runners.

The split reduces paid runner time while preserving the checks that catch
platform-specific process, shell, path, and executable failures. Cloud tasks do
not replace a required status check and never stand in for another operating
system.

## Verification model

During development, agents run the complete Linux suite in the configured
`xenoviz/ruk` cloud environment: repository validation, coverage, native and
cross-compiled executables, package installation, and documentation. The cached
setup installs Bun 1.3.14 once and refreshes dependencies with
`bun install --frozen-lockfile` for each checked-out branch.

Pull requests use one Ubuntu quality job for the same repository, coverage,
package, executable, cross-compilation, and documentation checks. A separate
compatibility matrix runs the compiled Node.js tests on Ubuntu Node 22,
Windows Node 22 and 24, and macOS Node 24. Windows and macOS also exercise their
native executables. The protected `main` branch adds macOS Node 22 before release
work can proceed.

This layout cuts pull-request runner jobs from thirteen to eight. It also runs
repository validation and coverage once instead of six times and installs Linux
dependencies once for all Linux quality checks. The stable `Required checks`
status still fails when any required job fails, so existing branch protection
does not need a new check name.

## Failure handling

Cloud failures block the developer from requesting review or merging, but they
do not publish a GitHub status. GitHub failures remain authoritative for branch
protection. A failure on protected `main`, including the deferred macOS Node 22
job, blocks release preparation and must be fixed or reverted.

Release workflows keep their independent source, package, binary, provenance,
and updater checks. The CI reduction does not remove release verification.
