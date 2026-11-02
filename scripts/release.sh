#!/bin/sh
# Builds the release archives and checksums.txt in dist/release.
# Run on macOS: the menu bar app needs cgo and the Apple tools.
# The script also builds the Windows tray app, which does not need cgo.
set -eu
# The CLI archives have the prefix "agent-radar-cli". The app archives have the prefix "Agent-Radar".
# The names must be different without case: the macOS file system ignores case.
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="dist/release"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
rm -rf "$OUT"
mkdir -p "$OUT"

for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
	os="${target%/*}"
	arch="${target#*/}"
	name="agent-radar-cli_${VERSION}_${os}_${arch}"
	mkdir -p "$TMP/$name"
	exe=agent-radar
	[ "$os" = windows ] && exe=agent-radar.exe
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$TMP/$name/$exe" ./cmd/agent-radar
	cp README.md LICENSE "$TMP/$name/"
	if [ "$os" = windows ]; then
		(cd "$TMP" && zip -qr "$OLDPWD/$OUT/$name.zip" "$name")
	else
		tar -C "$TMP" -czf "$OUT/$name.tar.gz" "$name"
	fi
	if [ "$os" = linux ]; then
		app="Agent-Radar_${VERSION}_Linux_${arch}"
		mkdir -p "$TMP/$app/dist/linux" "$TMP/$app/internal/tray" "$TMP/$app/docs"
		cp "$TMP/$name/agent-radar" "$TMP/$app/dist/linux/"
		cp -R linux "$TMP/$app/"
		cp internal/tray/window_linux.js "$TMP/$app/internal/tray/"
		cp docs/linux.md "$TMP/$app/docs/"
		cp AGENT.md README.md LICENSE "$TMP/$app/"
		tar -C "$TMP" -czf "$OUT/$app.tar.gz" "$app"
	fi
done

VERSION="$VERSION" ./macos/build-app.sh
name="agent-radar-cli_${VERSION}_darwin_universal"
mkdir -p "$TMP/$name"
cp "dist/Agent Radar.app/Contents/MacOS/agent-radar" README.md LICENSE "$TMP/$name/"
tar -C "$TMP" -czf "$OUT/$name.tar.gz" "$name"
ditto -c -k --keepParent "dist/Agent Radar.app" "$OUT/Agent-Radar_${VERSION}_macOS.zip"

VERSION="$VERSION" ./windows/build-app.sh
for arch in amd64 arm64; do
	label=x64
	[ "$arch" = arm64 ] && label=ARM64
	name="Agent-Radar_${VERSION}_Windows_${label}"
	mkdir -p "$TMP/$name"
	cp "dist/windows/$arch/Agent Radar.exe" README.md LICENSE "$TMP/$name/"
	(cd "$TMP" && zip -qr "$OLDPWD/$OUT/$name.zip" "$name")
done

(cd "$OUT" && shasum -a 256 * > checksums.txt)
ls -1 "$OUT"
