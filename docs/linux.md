# Linux and GNOME

Agent Radar has a GNOME top-bar extension and a native GTK window.
The window shows the same full graph as the web UI. It uses WebKitGTK, not a browser tab.
The extension and the window use one local backend.

## Requirements

- GNOME Shell 45 through 51 for the top-bar extension.
- GJS, GTK 4, WebKitGTK 6, and the Soup 3 introspection library.
- A systemd user session and `xdg-open`.
- Go 1.22 or later to build from source. A release package does not need Go.

GNOME Shell 51 with Wayland is tested. Other listed Shell versions are not tested.
The extension uses the modern GNOME module format.
The installer checks the window libraries. It does not install system packages.
Package names differ by distribution. Ask the distribution package manager for the
GJS, GTK 4, WebKitGTK 6, and Soup 3 runtime packages if a library is not available.

## Install

From source:

```sh
sh linux/build-app.sh
sh linux/install.sh
```

From a Linux app release, extract `Agent-Radar_<version>_Linux_<arch>.tar.gz`,
open a terminal in the extracted folder, and run `sh linux/install.sh`.

The installer writes only to the current user's folders:

| Item | Default location |
|------|------------------|
| Backend and launcher | `~/.local/bin/agent-radar` and `~/.local/bin/agent-radar-gnome` |
| Window | `~/.local/share/agent-radar/window.js` |
| Desktop entry | `~/.local/share/applications/io.github.awlx.AgentRadar.desktop` |
| Shell extension | `~/.local/share/gnome-shell/extensions/agent-radar@awlx.github.io` |
| User service | `~/.config/systemd/user/agent-radar.service` |

The data and configuration paths follow `XDG_DATA_HOME` and `XDG_CONFIG_HOME` if set.
The installer does not start the backend, enable the extension, or enable login autostart.
Close a running Radar window before an update. Restart the backend after an update.

### Fedora Workstation

Use a GNOME desktop session. These package names are from the
[Fedora package index](https://packages.fedoraproject.org/).
Fedora itself has not been tested.

Install the runtime and source-build tools:

```sh
sudo dnf install git golang gjs gtk4 webkitgtk6.0 libsoup3 gnome-shell xdg-utils
gnome-shell --version
```

The installer accepts GNOME Shell 45 through 51. Only Shell 51 has native runtime tests.
To try an unmerged pull request, get its number from the pull request page.
Use the current **Code** tab to check the source before you build it:

```sh
git clone https://github.com/awlx/agent-radar-app.git
cd agent-radar-app
PR_NUMBER=123
git fetch origin "pull/$PR_NUMBER/head"
git switch --detach FETCH_HEAD
sh linux/build-app.sh
sh linux/install.sh
systemctl --user start agent-radar.service
gnome-extensions enable agent-radar@awlx.github.io
~/.local/bin/agent-radar-gnome
```

Replace `123` with the actual pull request number. Do not run a different Radar server
on port 47900 at the same time. The app opens a native window, not a browser tab.
If Shell does not find the extension, use the window now and enable the extension
after your next normal login. Updating already imported extension JavaScript can also
require a fresh Shell session. Disabling and enabling is not a JavaScript reload.
Login autostart stays off.

The build also creates `dist/linux/Agent-Radar_<version>_Linux_<arch>.tar.gz`.
You can give this file to a user with the same CPU architecture.
They need the runtime packages above, but not Git or Go. They extract it, enter the
extracted folder, and run `sh linux/install.sh`, then the three start commands above.
This is a local build artifact, not a published release.

## Start

Stop an older Radar server if it uses port 47900. Then run:

```sh
systemctl --user start agent-radar.service
gnome-extensions enable agent-radar@awlx.github.io
~/.local/bin/agent-radar-gnome
```

You can also open **Agent Radar** from the GNOME app grid.
The desktop launcher starts the user service if needed.
It uses a fixed loopback address, `http://127.0.0.1:47900/`.
If another program uses that port, the service fails. It does not silently switch ports.

The installer uses GNOME's normal local ZIP installer.
On GNOME Shell 51, this installs the files but does not register a new extension
with the running Shell. The enable command can report that the extension does not exist.
You can use the native window now and enable the top bar after your next login.
Do not interrupt your work to log out.

GNOME can hot-install extensions through its `extensions.gnome.org` installer.
That path downloads and registers an extension inside Shell.
This new extension is not published there. Shell 51 exposes no local ZIP registration
method, and its old `ReloadExtension` method returns "deprecated and does not work".
This is a local installation limit, not a rule that all GNOME extensions need a logout.
On Wayland, do not use Alt-F2 `r` to restart Shell.
For an extension update, disable it and install the update. A fresh Shell session can
also be needed before GNOME uses changed JavaScript files.

The top bar shows working and waiting session counts. Sub-agents do not add a second session count.
The fixed Radar mark follows the macOS icon: two rings, a sweep line, and blips.
Its lower-right badge shows a check for working, a stop square for waiting, and pause bars for idle.
GNOME's symbolic palette gives those badges success, error, and warning colors.
The Radar strokes use the panel foreground color for light, dark, and high-contrast themes.
The shapes also identify status without color. An exclamation badge with **Radar !**
means disconnected or stale data, not idle. The icon does not move.
The menu lists up to 50 waiting sessions. The total still includes all waiting sessions.
**Open Radar** opens the native graph window.

Collection runs in the Go backend, outside GNOME Shell.
The extension reads the cached status asynchronously every 2 seconds.
It cancels requests and removes its timer and menu when disabled.
Disabling the extension does not stop the backend.

Closing the installed graph window does not stop the backend.
Opening it again creates or presents one window. The window uses temporary browser storage.
If the backend stops, the graph shows disconnected data. If the page cannot load,
the native window shows an error and a **Reconnect** button.

To stop collection:

```sh
systemctl --user stop agent-radar.service
```

Login autostart is optional. Enable it only if you want it:

```sh
systemctl --user enable agent-radar.service
```

To remove login autostart, use `systemctl --user disable agent-radar.service`.
The service also stops when the graphical session ends.

## Session navigation

A waiting Copilot app session opens its registered `ghapp://sessions/<id>` link.
A VS Code session opens its owning folder or saved workspace with the editor's
`--reuse-window` command. The editor command must be in `PATH`.
The request can start an app, but it does not prove that the app selected the session.

Wayland has no portable way to select a terminal tab by process ID.
An unsaved multi-root editor window also has no safe folder target.
Those waiting menu entries open Radar instead. The graph does not offer terminal-app focus on Linux.
No window-control workaround or Shell permission bypass is used.
Jump requests are off in demo mode and on LAN listeners.

## Standalone window

For a temporary window with its own backend, run:

```sh
agent-radar -app
```

Do not run this on port 47900 while the user service is active.
Closing this standalone window also stops its backend.
`-app -background` keeps the standalone app running without an initial window.
The installed desktop launcher is the normal way to use the GNOME integration.
`-gnome` starts only the Linux loopback backend with local session navigation.

## Check

```sh
go test ./...
go vet ./...
node --test internal/server/ui/graph.test.mjs linux/extension/status.test.mjs
sh linux/build-app.sh
python3 linux/smoke-test.py
python3 linux/shell-smoke-test.py
python3 linux/icon-smoke-test.py
```

The native smoke test needs a graphical session. It opens and closes a temporary
window with demo data. It checks the live graph, temporary storage, and disabled demo jumps.
It does not read real session data.

The Shell smoke test uses a separate headless compositor, D-Bus session, and temporary
configuration folders. It checks three extension enable/disable cycles.
It does not restart your desktop or change its extension settings.
The test can show an offline indicator if no backend runs on port 47900.

The icon smoke test needs Python GObject bindings and GTK 3 introspection
(`python3-gobject` and `gtk3` on Fedora). It checks four different symbolic badges
at 16, 24, and 32 pixels with light, dark, and high-contrast foregrounds.

For installed service errors, use:

```sh
systemctl --user status agent-radar.service
journalctl --user -u agent-radar.service -n 30
gnome-extensions info agent-radar@awlx.github.io
```

Do not share logs or screenshots with real session data.

## Remove

Close the Radar window, then disable the extension and service:

```sh
gnome-extensions disable agent-radar@awlx.github.io
systemctl --user disable --now agent-radar.service
```

Remove the named files in the installation table, and the four extension files
`metadata.json`, `extension.js`, `status.js`, and `stylesheet.css`.
Remove the four `icons/radar-*-symbolic.svg` files as well.
Remove their empty folders if needed. Run `systemctl --user daemon-reload`.
This does not remove Copilot or editor session files.
