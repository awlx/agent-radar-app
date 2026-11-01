# Linux and GNOME

Agent Radar has a GNOME top-bar extension and a native GTK window.
The window shows the same full graph as the web UI. It uses WebKitGTK, not a browser tab.
Launch **Agent Radar**. The app starts its local backend. You do not need a systemd service.

## Requirements

- GNOME Shell 45 through 51 for the top-bar extension.
- GJS, GTK 4, WebKitGTK 6, and Soup 3 introspection libraries.
- A graphical session with a user D-Bus bus, and `xdg-open`.
- Go 1.22 or later to build from source. A built package does not need Go.

GNOME Shell 51 with Wayland is tested. Other listed Shell versions and Fedora itself
are not runtime-tested. The installer checks the libraries and Shell version.
It does not install system packages.

## Install

From source:

```sh
sh linux/build-app.sh
sh linux/install.sh
```

From a built Linux package, extract `Agent-Radar_<version>_Linux_<arch>.tar.gz`,
enter its folder, and run `sh linux/install.sh`.

The installer writes only to the current user's folders:

| Item | Default location |
|------|------------------|
| Backend and launcher | `~/.local/bin/agent-radar` and `~/.local/bin/agent-radar-gnome` |
| Window | `~/.local/share/agent-radar/window.js` |
| Desktop entry | `~/.local/share/applications/io.github.awlx.AgentRadar.desktop` |
| Shell extension | `~/.local/share/gnome-shell/extensions/agent-radar@awlx.github.io` |

The data paths follow `XDG_DATA_HOME` if set. The installer does not start the app,
enable the extension, add a service, or enable login autostart.
Quit Radar before an update. Installing changed JavaScript does not change an already
running app or Shell module.

### Fedora Workstation: test a pull request

These package names are from the [Fedora package index](https://packages.fedoraproject.org/).
Use a GNOME desktop session:

```sh
sudo dnf install git golang gjs gtk4 webkitgtk6.0 libsoup3 gnome-shell xdg-utils
gnome-shell --version
git clone https://github.com/awlx/agent-radar-app.git
cd agent-radar-app
PR_NUMBER=123
git fetch origin "pull/$PR_NUMBER/head"
git switch --detach FETCH_HEAD
sh linux/build-app.sh
sh linux/install.sh
gnome-extensions enable agent-radar@awlx.github.io
~/.local/bin/agent-radar-gnome
```

Replace `123` with the pull request number. The app grid also has **Agent Radar**.
The native window opens and the app manages its backend. There is no `systemctl` step.
If Shell cannot find the extension, use the window now and enable the extension
after your next normal login. Do not interrupt your work to log out.
Login autostart stays off.

## App lifecycle

The primary GTK app owns one backend process and listens only on `127.0.0.1:47900`.
Collection stays in Go, outside GNOME Shell. The window and the extension share this backend.
D-Bus registration makes repeated or concurrent app launches present the same window;
they do not start extra backends.

Closing the graph hides the window. The app and status backend keep running.
**Open Radar** in the top-bar menu, or another launch from the app grid, presents the window.
Use **Quit Radar** in the window header or the updated top-bar menu to stop the app.
Quit sends SIGTERM only to the backend that this app started, and waits for it to stop.
The child also stops if its owning app exits unexpectedly.

If a compatible Radar server already uses the port, the app reuses it.
It checks the `/api/info` protocol before loading the graph. Quit does not stop that server.
An older server without this protocol or an unrelated program produces an error, not a
success-shaped fallback. The app does not silently choose a different port.
Demo mode never reuses an external server, because that could show real session data.

Startup requests are asynchronous. A missing executable, failed backend, or startup
deadline produces an error in the native window. **Reconnect** can start a stopped
app-owned backend again. It does not stop or replace an external server.
The window uses temporary browser storage. Stale or disconnected graph data is identified explicitly.

### Top bar and Shell updates

The top bar shows working and waiting session counts. Sub-agents do not add a second session count.
The fixed Radar mark follows the macOS icon: two rings, a sweep line, and blips.
Its lower-right badge shows a check for working, a stop square for waiting, and pause bars for idle.
GNOME's symbolic palette gives those badges success, error, and warning colors.
The Radar strokes use the panel foreground color for light, dark, and high-contrast themes.
The shapes identify status without color. An exclamation badge with **Radar !**
means disconnected or stale data, not idle. The icon does not move.

The menu lists up to 50 waiting sessions. The total still includes all waiting sessions.
The extension reads cached status asynchronously every 2 seconds.
It cancels requests and removes its timer and actors when disabled.
Disabling the extension does not quit the app.

The installer uses GNOME's local ZIP installer. On Shell 51, it installs the files but
does not register a new UUID with the running Shell. Website installation can register
extensions inside Shell, but this new extension is not published there.
Shell 51's old `ReloadExtension` method returns "deprecated and does not work".
Already imported extension JavaScript also stays cached. Disable/enable is not a
JavaScript reload. A fresh Shell session can be needed for the new icon or **Quit Radar** menu.
Until then, quit from the native window. Do not restart Shell or use unsafe evaluation.

### Session navigation

A waiting Copilot app session opens its registered `ghapp://sessions/<id>` link.
A VS Code session opens its owning folder or saved workspace with the editor's
`--reuse-window` command. The editor command must be in `PATH`.
Accepting a launch request does not prove that the app selected the session.

Wayland has no portable way to select a terminal tab by process ID.
An unsaved multi-root editor window also has no safe folder target.
Those menu entries open Radar instead. The graph does not offer terminal-app focus on Linux.
Demo and LAN views cannot request session jumps.

## Command-line app

```sh
agent-radar -app
```

This has the same managed lifecycle as the desktop launcher.
`-app -background` starts without the graph. A later app launch shows it.
The Linux app needs a fixed `127.0.0.1` port. The GNOME extension uses port 47900.
For a separate demo, use `agent-radar -app -demo -listen 127.0.0.1:47901`.
`-gnome` is the app's internal loopback backend mode; ordinary users do not need it.

## Check

```sh
go test ./...
go vet ./...
node --test internal/server/ui/graph.test.mjs linux/extension/status.test.mjs
sh linux/build-app.sh
python3 linux/smoke-test.py
python3 linux/lifecycle-smoke-test.py
python3 linux/shell-smoke-test.py
python3 linux/icon-smoke-test.py
```

The native checks need a graphical session. The graph check uses demo data.
The lifecycle check covers ownership, repeated launches, hide/reopen/Quit, external
server reuse, incompatible servers, missing executables, backend failure, startup
deadlines, and unexpected owner exit. It uses temporary ports and app IDs.
The Shell check uses an isolated compositor, D-Bus bus, and temporary configuration.
It does not restart your desktop or change its extension settings.
The icon check needs Python GObject bindings and GTK 3 introspection
(`python3-gobject` and `gtk3` on Fedora). It checks all badges at 16, 24, and 32 pixels.

For manual testing, close and reopen the graph, then use **Quit Radar**.
Check that the app-owned server stops and the extension shows disconnected status.
Launch the app again and check that live status returns.
Do not share logs or screenshots with real session names or paths.

## Remove

Use **Quit Radar** first, then:

```sh
gnome-extensions disable agent-radar@awlx.github.io
gnome-extensions uninstall agent-radar@awlx.github.io
rm -f ~/.local/bin/agent-radar ~/.local/bin/agent-radar-gnome
rm -f ~/.local/share/agent-radar/window.js
rm -f ~/.local/share/applications/io.github.awlx.AgentRadar.desktop
```

Use your `XDG_DATA_HOME` path if set. If Shell has not registered the extension,
remove its four JavaScript/metadata/style files and four `icons/radar-*-symbolic.svg`
files from the extension folder in the installation table.
Remove empty installer-owned folders if needed. Copilot session files are not removed.

### Migrate an earlier build of this pull request

Earlier test builds installed `agent-radar.service`. The new installer does not create
or remove services. Before migration, check that the old unit is the Radar unit from
that installer, not a custom unit. Stop and remove only that unit after the new app
has been built and validated. Do not delete unrelated service files or stop other servers.
Then install the new build and launch **Agent Radar**. No service is needed after migration.
