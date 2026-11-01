#!/usr/bin/env python3
"""Check extension lifecycle in an isolated headless GNOME Shell and D-Bus session."""
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

UUID = "agent-radar@awlx.github.io"
ROOT = pathlib.Path(__file__).resolve().parent.parent


def call(*args, check=True):
    return subprocess.run(args, text=True, capture_output=True, check=check, timeout=10)


def check_shell(base):
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
            for _ in range(3):
                call("gnome-extensions", "enable", UUID)
                time.sleep(1)
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
                time.sleep(0.5)
            log.seek(0)
            output = log.read()
            if output.count("AGENT_RADAR_TEST_CLEAN") < 3:
                raise RuntimeError("Extension cleanup did not complete")
            try:
                with urllib.request.urlopen("http://127.0.0.1:47900/api/status", timeout=5):
                    backend_live = True
            except (urllib.error.URLError, TimeoutError):
                backend_live = False
            expected = "live" if backend_live else "offline"
            if f"AGENT_RADAR_TEST_STATUS {expected}" not in output:
                raise RuntimeError(f"Extension did not render {expected} status")
            print(f"PASS: isolated GNOME Shell, {expected} status, three enable/disable cycles with cleanup")
        except Exception:
            log.seek(0)
            print(log.read()[-12000:], file=sys.stderr)
            raise
        finally:
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
        script = ext / "extension.js"
        source = script.read_text().replace(
            "_render(data) {",
            "_render(data) {\n        console.log('AGENT_RADAR_TEST_STATUS ' + (data ? 'live' : 'offline'));",
        ).replace(
            "this._menuKey = undefined;",
            """this._menuKey = undefined;
        if (this._timer !== 0 || this._cancel !== null || this._session !== null || this._indicator !== null)
            throw new Error('Extension cleanup failed');
        console.log('AGENT_RADAR_TEST_CLEAN');""",
        )
        script.write_text(source)
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
