#!/bin/sh
# Builds dist/windows/<arch>/Copilot Radar.exe, the Windows tray app, for x64 and ARM64.
# The script runs on macOS, Linux, or Windows (Git Bash). You do not need cgo.
set -eu
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
WINRES="github.com/tc-hib/go-winres@v0.3.3"
TMP="$(mktemp -d)"
trap 'rm -f cmd/copilot-radar/rsrc_windows_*.syso; rm -rf "$TMP"' EXIT

# The numeric file version, for example 0.2.0.0. A "dev" build gets 0.0.0.0.
num="$(echo "$VERSION" | sed -n 's/^v\{0,1\}\([0-9]*\)\.\([0-9]*\)\.\([0-9]*\).*/\1.\2.\3.0/p')"
[ -n "$num" ] || num=0.0.0.0

go run ./macos/icon "$TMP/icon"
(cd cmd/copilot-radar && go run "$WINRES" simply \
	--arch amd64,arm64 \
	--manifest gui \
	--icon "$TMP/icon/icon_256x256.png" \
	--product-name "Copilot Radar" \
	--file-description "Copilot Radar" \
	--original-filename "Copilot Radar.exe" \
	--copyright "Annika Wickert. MIT License." \
	--product-version "$num" \
	--file-version "$num")

for arch in amd64 arm64; do
	mkdir -p "dist/windows/$arch"
	CGO_ENABLED=0 GOOS=windows GOARCH="$arch" go build -trimpath \
		-ldflags "-H windowsgui -s -w -X main.version=$VERSION -X main.appBuild=true" \
		-o "dist/windows/$arch/Copilot Radar.exe" ./cmd/copilot-radar
done
echo "Built dist/windows/{amd64,arm64}/Copilot Radar.exe ($VERSION)"
