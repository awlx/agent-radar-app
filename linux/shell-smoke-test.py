#!/usr/bin/env python3
"""Check extension lifecycle in an isolated headless GNOME Shell and D-Bus session."""
import os
import json
import pathlib
import shlex
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

UUID = "agent-radar@awlx.github.io"
ROOT = pathlib.Path(__file__).resolve().parent.parent


def wait_for(check, description, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(.2)
    raise RuntimeError(description)


def call(*args, check=True):
    return subprocess.run(args, text=True, capture_output=True, check=check, timeout=10)


def check_shell(base):
    config = json.loads((base / "test.json").read_text())
    port = config["port"]
    app_id = f"io.github.awlx.AgentRadar.Port{port}.Demo"
    app_path = f"/io/github/awlx/AgentRadar/Port{port}/Demo"

    def app_state():
        file = base / "app-state.json"
        return json.loads(file.read_text()) if file.exists() else {}

    def live():
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/api/status", timeout=1) as response:
                return response.status == 200
        except (urllib.error.URLError, TimeoutError):
            return False

    def quit_app():
        return call(
            "gdbus", "call", "--session", "--dest", app_id,
            "--object-path", app_path, "--method", "org.gtk.Actions.Activate",
            "quit", "[]", "{}", check=False,
        )

    call("gsettings", "set", "org.gnome.shell", "enabled-extensions", f"['{UUID}']")
    with (base / "shell.log").open("w+") as log:
        shell = subprocess.Popen([
            "gnome-shell", "--headless", "--wayland", "--no-x11",
            "--virtual-monitor", "1280x800", "--wayland-display", os.environ["WAYLAND_DISPLAY"],
        ], stdout=log, stderr=log)
        try:
            for _ in range(40):
                if shell.poll() is not None:
                    raise RuntimeError("Isolated Shell exited")
                info = call("gnome-extensions", "info", UUID, check=False)
                if info.returncode == 0:
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError("Isolated Shell did not discover the extension")
            wait_for(lambda: live() and app_state().get("ready"), "enable did not start a background backend")
            if app_state().get("visible") or not app_state().get("owned"):
                raise RuntimeError("fresh enable did not start a windowless, app-owned backend")
            quit_app().check_returncode()
            wait_for(lambda: not live(), "Quit did not stop the app-owned backend")
            time.sleep(5)
            if live():
                raise RuntimeError("polling restarted Radar after deliberate Quit")
            print("PASS: fresh-login enable starts the backend without a graph; Quit is not respawned")
            for _ in range(2):
                call("gnome-extensions", "disable", UUID)
                time.sleep(.5)
                call("gnome-extensions", "enable", UUID)
                wait_for(lambda: live() and app_state().get("ready"), "re-enable did not restore background Radar")
                if app_state().get("visible"):
                    raise RuntimeError("re-enable presented the graph of the existing primary")
                time.sleep(2.5)
                info = call("gnome-extensions", "info", UUID).stdout
                if "State: ACTIVE" not in info and "State: ENABLED" not in info:
                    raise RuntimeError(f"Extension did not enable:\n{info}")
                errors = call(
                    "gdbus", "call", "--session", "--dest", "org.gnome.Shell.Extensions",
                    "--object-path", "/org/gnome/Shell/Extensions",
                    "--method", "org.gnome.Shell.Extensions.GetExtensionErrors", UUID,
                ).stdout
                if errors.strip() not in ("(@as [],)", "([],)", "(@as [], )"):
                    raise RuntimeError(f"Extension runtime errors: {errors}")
            call("gnome-extensions", "disable", UUID)
            time.sleep(.5)
            log.seek(0)
            output = log.read()
            if output.count("AGENT_RADAR_TEST_CLEAN") < 3 or "AGENT_RADAR_TEST_ICON" not in output:
                raise RuntimeError("Extension cleanup did not complete")
            if "AGENT_RADAR_TEST_STATUS live" not in output or "AGENT_RADAR_TEST_STATUS offline" not in output:
                raise RuntimeError("Extension did not show both live and disconnected state")
            if output.count("AGENT_RADAR_TEST_BACKGROUND") != 3:
                raise RuntimeError("background startup was not exactly once per enable")
            if "Starting Radar in the background" not in output:
                raise RuntimeError("extension did not report startup explicitly")
            print("PASS: isolated GNOME Shell live/disconnected states; one background launch per enable; cleanup")
        except Exception:
            log.seek(0)
            print(log.read()[-12000:], file=sys.stderr)
            raise
        finally:
            quit_app()
            shell.terminate()
            try:
                shell.wait(timeout=5)
            except subprocess.TimeoutExpired:
                shell.kill()
                shell.wait()


if len(sys.argv) == 2:
    check_shell(pathlib.Path(sys.argv[1]))
else:
    with tempfile.TemporaryDirectory(prefix="agent-radar-shell-test-") as tmp:
        base = pathlib.Path(tmp)
        ext = base / "data/gnome-shell/extensions" / UUID
        ext.parent.mkdir(parents=True)
        shutil.copytree(ROOT / "linux/extension", ext)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        (base / "test.json").write_text(json.dumps({"port": port}))
        url = f"http://127.0.0.1:{port}/"
        status = ext / "status.js"
        status.write_text(status.read_text().replace("http://127.0.0.1:47900/", url))
        script = ext / "extension.js"
        source = script.read_text().replace(
            "_render(data) {",
            "_render(data) {\n        console.log('AGENT_RADAR_TEST_STATUS ' + (data ? 'live' : 'offline'));",
        ).replace(
            "_startBackground() {",
            "_startBackground() {\n        console.log('AGENT_RADAR_TEST_BACKGROUND');",
        ).replace(
            "this._indicator.accessible_name = `Agent Radar: ${display.title}`;",
            """this._indicator.accessible_name = `Agent Radar: ${display.title}`;
        console.log('AGENT_RADAR_TEST_TITLE ' + display.title);""",
        ).replace(
            "this._indicator.icon.gicon = this._icons.get(display.icon);",
            """this._indicator.icon.gicon = this._icons.get(display.icon);
        if (!(this._indicator.icon.gicon instanceof Gio.FileIcon) ||
            !this._indicator.icon.gicon.get_file().query_exists(null))
            throw new Error('The Radar status icon is not bundled artwork');
        console.log('AGENT_RADAR_TEST_ICON ' + display.state);""",
        ).replace(
            "this._menuKey = undefined;",
            """this._menuKey = undefined;
        if (this._timer !== 0 || this._cancel !== null || this._session !== null || this._indicator !== null)
            throw new Error('Extension cleanup failed');
        console.log('AGENT_RADAR_TEST_CLEAN');""",
        )
        script.write_text(source)
        app_script = base / "window.js"
        app_source = (ROOT / "internal/tray/window_linux.js").read_text()
        app_source = app_source.replace("app.run(['agent-radar', ...ARGV]);", f"""
app.connect('startup', () => {{
    GLib.timeout_add(GLib.PRIORITY_DEFAULT, 100, () => {{
        GLib.file_set_contents({json.dumps(str(base / 'app-state.json'))}, JSON.stringify({{
            ready, owned: backend?.get_identifier() || null, visible: !!window?.get_visible(),
        }}));
        return GLib.SOURCE_CONTINUE;
    }});
}});
app.run(['agent-radar', ...ARGV]);""")
        app_script.write_text(app_source)
        launcher = base / "launch-radar"
        launcher.write_text(
            f"#!/bin/sh\nexec gjs -m {shlex.quote(str(app_script))} {url} "
            f"--backend {shlex.quote(str(ROOT / 'dist/linux/agent-radar'))} --demo \"$@\"\n"
        )
        launcher.chmod(0o755)
        applications = base / "data/applications"
        applications.mkdir()
        (applications / "io.github.awlx.AgentRadar.desktop").write_text(
            f"[Desktop Entry]\nType=Application\nName=Test Radar\nExec={launcher}\nActions=background;\n"
            f"[Desktop Action background]\nName=Start in Background\nExec={launcher} --background\n"
        )
        for folder in ("config", "cache", "runtime"):
            (base / folder).mkdir(mode=0o700)
        env = dict(
            os.environ, XDG_DATA_HOME=str(base / "data"), XDG_CONFIG_HOME=str(base / "config"),
            XDG_CACHE_HOME=str(base / "cache"), XDG_RUNTIME_DIR=str(base / "runtime"),
            WAYLAND_DISPLAY="agent-radar-test", GIO_USE_VFS="local",
        )
        env.pop("DISPLAY", None)
        result = subprocess.run(
            ["dbus-run-session", "--", sys.executable, str(pathlib.Path(__file__).resolve()), tmp],
            env=env, timeout=60,
        )
    sys.exit(result.returncode)
