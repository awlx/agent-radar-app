#!/bin/sh
# Builds the release archives and checksums.txt in dist/release.
# Run on macOS: the menu bar app needs cgo and the Apple tools.
# The script also builds the Windows tray app, which does not need cgo.
set -eu
# The CLI archives have the prefix "copilot-radar-cli". The app archives have the prefix "Copilot-Radar".
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
	name="copilot-radar-cli_${VERSION}_${os}_${arch}"
	mkdir -p "$TMP/$name"
	exe=copilot-radar
	[ "$os" = windows ] && exe=copilot-radar.exe
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$TMP/$name/$exe" ./cmd/copilot-radar
	cp README.md LICENSE "$TMP/$name/"
	if [ "$os" = windows ]; then
		(cd "$TMP" && zip -qr "$OLDPWD/$OUT/$name.zip" "$name")
	else
		tar -C "$TMP" -czf "$OUT/$name.tar.gz" "$name"
	fi
done

VERSION="$VERSION" ./macos/build-app.sh
name="copilot-radar-cli_${VERSION}_darwin_universal"
mkdir -p "$TMP/$name"
cp "dist/Copilot Radar.app/Contents/MacOS/copilot-radar" README.md LICENSE "$TMP/$name/"
tar -C "$TMP" -czf "$OUT/$name.tar.gz" "$name"
ditto -c -k --keepParent "dist/Copilot Radar.app" "$OUT/Copilot-Radar_${VERSION}_macOS.zip"

VERSION="$VERSION" ./windows/build-app.sh
for arch in amd64 arm64; do
	label=x64
	[ "$arch" = arm64 ] && label=ARM64
	name="Copilot-Radar_${VERSION}_Windows_${label}"
	mkdir -p "$TMP/$name"
	cp "dist/windows/$arch/Copilot Radar.exe" README.md LICENSE "$TMP/$name/"
	(cd "$TMP" && zip -qr "$OLDPWD/$OUT/$name.zip" "$name")
done

(cd "$OUT" && shasum -a 256 * > checksums.txt)
ls -1 "$OUT"
