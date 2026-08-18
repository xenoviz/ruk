#!/usr/bin/env bash
# Cloud Agent install: provision the pinned Bun toolchain and refresh project
# dependencies from the committed lockfile. Safe to run repeatedly.
set -euo pipefail

# Keep this in sync with package.json "packageManager" / "engines".
BUN_VERSION="1.3.14"

export BUN_INSTALL="${BUN_INSTALL:-$HOME/.bun}"
export PATH="$BUN_INSTALL/bin:$PATH"

if ! command -v bun >/dev/null 2>&1 || [ "$(bun --version)" != "$BUN_VERSION" ]; then
  curl -fsSL https://bun.sh/install | bash -s "bun-v${BUN_VERSION}"
fi

# Expose Bun on the global PATH so start commands, terminals, and non-login
# shells resolve it without sourcing a shell profile.
if command -v sudo >/dev/null 2>&1; then
  sudo ln -sf "$BUN_INSTALL/bin/bun" /usr/local/bin/bun
  sudo ln -sf "$BUN_INSTALL/bin/bun" /usr/local/bin/bunx
fi

bun --version
node --version || true

# Install exactly from the committed lockfile (see CONTRIBUTING.md).
bun install --frozen-lockfile
