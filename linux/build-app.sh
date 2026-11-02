#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
ARCH="${GOARCH:-$(go env GOARCH)}"
mkdir -p dist/linux
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o dist/linux/agent-radar ./cmd/agent-radar
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
NAME="Agent-Radar_${VERSION}_Linux_${ARCH}"
mkdir -p "$TMP/$NAME/dist/linux" "$TMP/$NAME/internal/tray" "$TMP/$NAME/docs"
cp dist/linux/agent-radar "$TMP/$NAME/dist/linux/"
cp -R linux "$TMP/$NAME/"
cp internal/tray/window_linux.js "$TMP/$NAME/internal/tray/"
cp docs/linux.md "$TMP/$NAME/docs/"
cp AGENT.md README.md LICENSE "$TMP/$NAME/"
tar -C "$TMP" -czf "dist/linux/$NAME.tar.gz" "$NAME"
echo "Built dist/linux/agent-radar and dist/linux/$NAME.tar.gz. Install with sh linux/install.sh."
