#!/bin/sh
# Builds dist/Agent Radar.app, a universal app for Apple silicon and Intel Macs.
set -eu
cd "$(dirname "$0")/.."

APP="dist/Agent Radar.app"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

export MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="-O2 -mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0" # also for runtime/cgo
for arch in arm64 amd64; do
	CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$TMP/agent-radar-$arch" ./cmd/agent-radar
done
lipo -create -output "$APP/Contents/MacOS/agent-radar" "$TMP/agent-radar-arm64" "$TMP/agent-radar-amd64"

sed "s/@VERSION@/$VERSION/g" macos/Info.plist > "$APP/Contents/Info.plist"
go run ./macos/icon "$TMP/AppIcon.iconset"
iconutil -c icns -o "$APP/Contents/Resources/AppIcon.icns" "$TMP/AppIcon.iconset"

codesign --force --sign - "$APP"
echo "Built $APP ($VERSION)"
