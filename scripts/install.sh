#!/usr/bin/env bash
# Build and install the single Black Box binary.
set -euo pipefail
cd "$(dirname "$0")/.."

DEST="${1:-$HOME/.local/bin}"
mkdir -p "$DEST"

go build -trimpath -ldflags "-s -w" -o bin/blackbox ./cmd/blackbox
install -m 0755 bin/blackbox "$DEST/blackbox"

echo "installed $DEST/blackbox"
"$DEST/blackbox" version
