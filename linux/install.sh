#!/bin/sh
# Install for the current user. Do not start services or change login settings.
set -eu
cd "$(dirname "$0")/.."
for tool in gjs gnome-shell gnome-extensions systemctl; do
    command -v "$tool" >/dev/null || { echo "Required command not found: $tool" >&2; exit 1; }
done
gjs -c "imports.gi.versions.Gtk='4.0'; imports.gi.versions.WebKit='6.0'; imports.gi.versions.Soup='3.0'; imports.gi.Gtk; imports.gi.WebKit; imports.gi.Soup;" ||
    { echo "Install the GJS, GTK 4 and WebKitGTK 6 runtime packages for your distribution." >&2; exit 1; }
case "$(gnome-shell --version | awk '{split($3, v, "."); print v[1]}')" in
    45|46|47|48|49|50|51) ;;
    *) echo "This extension requires GNOME Shell 45 through 51." >&2; exit 1 ;;
esac

DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}"
BIN="$HOME/.local/bin"
APP="$DATA/agent-radar"
TMP="$(mktemp -d)"
trap 'rm -f "$TMP/agent-radar@awlx.github.io.shell-extension.zip"; rmdir "$TMP"' EXIT
gnome-extensions pack linux/extension --extra-source=status.js --force --out-dir="$TMP"
mkdir -p "$BIN" "$APP" "$DATA/applications" "$CONFIG/systemd/user"
if [ -x dist/linux/agent-radar ]; then
    install -m 755 dist/linux/agent-radar "$BIN/agent-radar"
else
    echo "Build the Linux package first: sh linux/build-app.sh" >&2
    exit 1
fi
install -m 644 internal/tray/window_linux.js "$APP/window.js"
gnome-extensions install --force --print-uuid "$TMP/agent-radar@awlx.github.io.shell-extension.zip"

# Quote paths for both the shell and the desktop-entry Exec field.
quote_shell() { printf "'"; printf '%s' "$1" | sed "s/'/'\\\\''/g"; printf "'"; }
quote_exec() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/`/\\`/g; s/\$/\\$/g; s/%/%%/g'; }
quote_systemd() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/\$/$$/g; s/%/%%/g'; }
{
    printf '#!/bin/sh\nset -eu\n'
    printf 'systemctl --user start agent-radar.service\n'
    printf 'exec gjs -m '
    quote_shell "$APP/window.js"
    printf ' http://127.0.0.1:47900/\n'
} > "$BIN/agent-radar-gnome"
chmod 755 "$BIN/agent-radar-gnome"
{
    printf '[Desktop Entry]\nType=Application\nName=Agent Radar\n'
    printf 'Comment=Show local agent sessions in a native window\n'
    printf 'Exec="%s"\n' "$(quote_exec "$BIN/agent-radar-gnome")"
    printf 'Icon=network-workgroup-symbolic\nTerminal=false\nCategories=Development;Monitor;\n'
    printf 'StartupWMClass=io.github.awlx.AgentRadar\n'
} > "$DATA/applications/io.github.awlx.AgentRadar.desktop"
{
    printf '[Unit]\nDescription=Agent Radar local GNOME backend\n'
    printf 'After=graphical-session.target\nPartOf=graphical-session.target\n\n'
    printf '[Service]\nType=exec\n'
    printf 'ExecStart="%s" -gnome -listen 127.0.0.1:47900\n' "$(quote_systemd "$BIN/agent-radar")"
    printf 'Restart=on-failure\nRestartSec=3\nTimeoutStopSec=5\n'
    printf '\n[Install]\nWantedBy=graphical-session.target\n'
} > "$CONFIG/systemd/user/agent-radar.service"
systemctl --user daemon-reload
if command -v update-desktop-database >/dev/null; then
    update-desktop-database "$DATA/applications"
fi
echo "Installed. No service or login autostart was enabled."
echo "Start: systemctl --user start agent-radar.service"
echo "Enable the top bar: gnome-extensions enable agent-radar@awlx.github.io"
echo "GNOME Shell 51 does not register a new local UUID until a fresh Shell session."
echo "You can use the native window now; enable the top bar after your next login if it is not found."
