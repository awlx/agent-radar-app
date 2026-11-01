#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
mkdir -p dist/linux
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o dist/linux/agent-radar ./cmd/agent-radar
echo "Built dist/linux/agent-radar. Install with sh linux/install.sh."
