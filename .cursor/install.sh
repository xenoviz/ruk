#!/usr/bin/env bash
# Cloud Agent install: provision the pinned Bun and Go toolchains and refresh
# project dependencies from the committed lockfile. Safe to run repeatedly.
set -euo pipefail

# Keep this in sync with package.json "packageManager" / "engines".
BUN_VERSION="1.3.14"
# Keep this in sync with go.mod and .github/workflows/ci.yml (Go migration).
GO_VERSION="1.24.6"

export BUN_INSTALL="${BUN_INSTALL:-$HOME/.bun}"
export PATH="$BUN_INSTALL/bin:/usr/local/go/bin:$PATH"

# --- Bun toolchain -----------------------------------------------------------
if ! command -v bun >/dev/null 2>&1 || [ "$(bun --version)" != "$BUN_VERSION" ]; then
  curl -fsSL https://bun.sh/install | bash -s "bun-v${BUN_VERSION}"
fi

# Expose Bun on the global PATH so start commands, terminals, and non-login
# shells resolve it without sourcing a shell profile.
if command -v sudo >/dev/null 2>&1; then
  sudo ln -sf "$BUN_INSTALL/bin/bun" /usr/local/bin/bun
  sudo ln -sf "$BUN_INSTALL/bin/bun" /usr/local/bin/bunx
fi

# --- Go toolchain (for the in-progress Go migration) -------------------------
if ! command -v go >/dev/null 2>&1 || [ "$(go version 2>/dev/null | awk '{print $3}')" != "go${GO_VERSION}" ]; then
  case "$(uname -m)" in
    x86_64|amd64) GO_ARCH="amd64" ;;
    aarch64|arm64) GO_ARCH="arm64" ;;
    *) echo "Unsupported architecture for Go: $(uname -m)" >&2; exit 1 ;;
  esac
  GO_TARBALL="go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
  curl -fsSL "https://go.dev/dl/${GO_TARBALL}" -o "/tmp/${GO_TARBALL}"
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "/tmp/${GO_TARBALL}"
  rm -f "/tmp/${GO_TARBALL}"
fi

# Expose Go on the global PATH for every process.
if command -v sudo >/dev/null 2>&1; then
  sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go
  sudo ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi

bun --version
node --version || true
go version

# Install exactly from the committed lockfile (see CONTRIBUTING.md).
bun install --frozen-lockfile
