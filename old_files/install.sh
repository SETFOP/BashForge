#!/usr/bin/env bash
set -euo pipefail

REPO="SETFOP/BashForge"
BIN_NAME="forge"
INSTALL_DIR="${HOME}/.local/bin"

echo "Installing BashForge..."

# Get latest release tag from GitHub API
LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' \
  | sed 's/.*"tag_name": *"\(.*\)".*/\1/')

URL="https://github.com/${REPO}/releases/download/${LATEST}/forge-linux-x86_64"

mkdir -p "$INSTALL_DIR"
curl -fsSL "$URL" -o "${INSTALL_DIR}/${BIN_NAME}"
chmod +x "${INSTALL_DIR}/${BIN_NAME}"

# Add to PATH if not already there
if [[ ":$PATH:" != *":${INSTALL_DIR}:"* ]]; then
    echo "export PATH=\"\$PATH:${INSTALL_DIR}\"" >> "${HOME}/.bashrc"
    echo "Added ${INSTALL_DIR} to PATH in .bashrc"
    echo "Run: source ~/.bashrc"
fi

echo "Done! forge $LATEST installed to ${INSTALL_DIR}/forge"
