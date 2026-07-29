#!/bin/sh
# Install the foodplace CLI by downloading a prebuilt binary from GitHub Releases.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/JSChlein/foodplace-cli/main/install.sh | sh
#
# Environment variables:
#   VERSION      version tag to install (default: latest release)
#   INSTALL_DIR  where to place the binary (default: /usr/local/bin, or ~/.local/bin if not writable)
set -eu

REPO="JSChlein/foodplace-cli"
BIN="foodplace"

# --- detect platform ---------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $os (use the Windows .zip from the Releases page)" >&2; exit 1 ;;
esac

# --- resolve version ---------------------------------------------------------
VERSION="${VERSION:-}"
if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -n1 | cut -d'"' -f4)
fi
if [ -z "$VERSION" ]; then
  echo "Could not determine latest version. Set VERSION=vX.Y.Z and retry." >&2
  exit 1
fi

# --- download & extract ------------------------------------------------------
asset="${BIN}_${VERSION}_${os}_${arch}"
url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${asset} ..."
curl -fsSL "$url" -o "$tmp/pkg.tar.gz"
tar -xzf "$tmp/pkg.tar.gz" -C "$tmp"

# --- install -----------------------------------------------------------------
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
if [ ! -w "$INSTALL_DIR" ] && [ "$(id -u)" -ne 0 ]; then
  INSTALL_DIR="$HOME/.local/bin"
fi
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/${asset}/${BIN}" "$INSTALL_DIR/${BIN}"

echo "Installed ${BIN} ${VERSION} to ${INSTALL_DIR}/${BIN}"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Note: ${INSTALL_DIR} is not on your PATH. Add it, e.g.:"
     echo "  export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
esac
