#!/usr/bin/env python3
"""Check primary GTK backend ownership, close/relaunch, failures and clean Quit."""
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread

ROOT = Path(__file__).resolve().parent.parent
BINARY = ROOT / "dist/linux/agent-radar"


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def http_info(port):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/api/info", timeout=1) as response:
            return json.load(response)
    except (urllib.error.URLError, TimeoutError):
        return None


def wait_for(check, description, timeout=15):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        result = check()
        if result:
            return result
        time.sleep(0.1)
    raise AssertionError(description)


def action(port, name, demo=False):
    suffix = ".Demo" if demo else ""
    path_suffix = "/Demo" if demo else ""
    subprocess.run([
        "gdbus", "call", "--session", "--dest", f"io.github.awlx.AgentRadar.Port{port}{suffix}",
        "--object-path", f"/io/github/awlx/AgentRadar/Port{port}{path_suffix}",
        "--method", "org.gtk.Actions.Activate", name, "[]", "{}",
    ], check=True, capture_output=True, timeout=5)


def test_app(tmp, port, backend, case, demo=False):
    state_file = tmp / f"{case}.json"
    script = tmp / f"{case}.js"
    source = (ROOT / "internal/tray/window_linux.js").read_text()
    source = source.replace("app.run([]);", f"""
app.connect('startup', () => {{
    const close = new Gio.SimpleAction({{name: 'test-close'}});
    close.connect('activate', () => window?.close());
    app.add_action(close);
    GLib.timeout_add(GLib.PRIORITY_DEFAULT, 100, () => {{
        GLib.file_set_contents({json.dumps(str(state_file))}, JSON.stringify({{
            ready, starting, owned: backend?.get_identifier() || null,
            visible: !!window?.get_visible(), error: errorLabel?.visible ? errorLabel.label : '',
        }}));
        return GLib.SOURCE_CONTINUE;
    }});
}});
app.run([]);
""")
    script.write_text(source)
    log = (tmp / f"{case}.log").open("w+")
    args = ["gjs", "-m", str(script), f"http://127.0.0.1:{port}/", "--backend", str(backend)]
    if demo:
        args.append("--demo")
    app = subprocess.Popen(args, stdout=log, stderr=log)

    def state():
        if state_file.exists():
            return json.loads(state_file.read_text())
        return {}

    return app, args, state, log


with tempfile.TemporaryDirectory(prefix="agent-radar-lifecycle-test-") as directory:
    tmp = Path(directory)
    apps, servers, logs = [], [], []
    try:
        port = free_port()
        app, args, state, log = test_app(tmp, port, BINARY, "owned")
        apps.append(app)
        logs.append(log)
        live = wait_for(lambda: state() if state().get("ready") and state().get("visible") else None, "owned app ready")
        owner = live["owned"]
        assert owner and http_info(port) == {"app": "agent-radar", "schema": 1}
        action(port, "test-close")
        wait_for(lambda: not state().get("visible"), "close hides graph")
        assert app.poll() is None and http_info(port)
        # Concurrent re-launches activate the registered primary; they do not start backends.
        relaunches = [subprocess.Popen(args, stdout=log, stderr=log) for _ in range(3)]
        for remote in relaunches:
            assert remote.wait(timeout=10) == 0
        wait_for(lambda: state().get("visible"), "relaunch presents graph")
        assert state()["owned"] == owner
        demo_app, _, demo_state, demo_log = test_app(tmp, port, BINARY, "demo-primary", demo=True)
        apps.append(demo_app)
        logs.append(demo_log)
        wait_for(lambda: demo_state().get("error"), "demo launch must not activate a live primary")
        assert not demo_state()["ready"] and demo_state()["owned"] is None
        action(port, "quit", demo=True)
        assert demo_app.wait(timeout=5) == 0
        assert app.poll() is None and state()["owned"] == owner
        action(port, "quit")
        assert app.wait(timeout=8) == 0
        assert http_info(port) is None
        print("PASS: owned backend, close/reopen, three concurrent relaunches, explicit Quit")

        port = free_port()
        app, args, state, log = test_app(tmp, port, BINARY, "parent-exit")
        apps.append(app)
        logs.append(log)
        wait_for(lambda: state().get("ready"), "app ready before unexpected parent exit")
        app.kill()
        app.wait(timeout=5)
        wait_for(lambda: http_info(port) is None, "backend stops after owning app is killed")
        print("PASS: backend exits if the owning GTK app terminates unexpectedly")

        port = free_port()
        server = subprocess.Popen([str(BINARY), "-gnome", "-demo", "-listen", f"127.0.0.1:{port}"],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        servers.append(server)
        wait_for(lambda: http_info(port), "external server ready")
        app, args, state, log = test_app(tmp, port, BINARY, "external")
        apps.append(app)
        logs.append(log)
        wait_for(lambda: state().get("ready"), "external reuse ready")
        assert state()["owned"] is None
        action(port, "quit")
        assert app.wait(timeout=5) == 0
        assert server.poll() is None and http_info(port)
        print("PASS: compatible external backend reused and not stopped by Quit")

        app, args, state, log = test_app(tmp, port, BINARY, "demo-reuse", demo=True)
        apps.append(app)
        logs.append(log)
        wait_for(lambda: state().get("error"), "demo must reject external server")
        assert not state()["ready"] and state()["owned"] is None
        action(port, "quit", demo=True)
        assert app.wait(timeout=5) == 0
        assert server.poll() is None and http_info(port)
        print("PASS: demo mode cannot reuse an external server or expose its session data")

        app, args, state, log = test_app(tmp, port, BINARY, "reconnect")
        apps.append(app)
        logs.append(log)
        wait_for(lambda: state().get("ready"), "reuse before external backend exits")
        assert state()["owned"] is None
        server.terminate()
        server.wait(timeout=5)
        assert http_info(port) is None
        action(port, "reconnect")
        wait_for(lambda: state().get("ready") and state().get("owned"), "reconnect starts an owned replacement")
        assert http_info(port)
        action(port, "quit")
        assert app.wait(timeout=8) == 0
        assert http_info(port) is None
        print("PASS: Reconnect starts an owned backend only after the external server exits")

        port = free_port()
        cli = subprocess.Popen([str(BINARY), "-app", "-background", "-demo",
                                "-listen", f"127.0.0.1:{port}"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        apps.append(cli)
        wait_for(lambda: http_info(port), "CLI app starts managed backend")
        remote = subprocess.run([str(BINARY), "-app", "-demo", "-listen", f"127.0.0.1:{port}"],
                                capture_output=True, timeout=10)
        assert remote.returncode == 0
        action(port, "quit", demo=True)
        assert cli.wait(timeout=8) == 0
        assert http_info(port) is None
        print("PASS: CLI -app/-background shares the GTK single instance and managed Quit")

        class OtherServer(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'{"app":"not-radar","schema":1}')

            def log_message(self, *_args):
                pass

        other = HTTPServer(("127.0.0.1", 0), OtherServer)
        thread = Thread(target=other.serve_forever, daemon=True)
        thread.start()
        port = other.server_port
        try:
            app, args, state, log = test_app(tmp, port, BINARY, "incompatible")
            apps.append(app)
            logs.append(log)
            wait_for(lambda: state().get("error"), "incompatible server error")
            assert not state()["ready"] and state()["owned"] is None
            action(port, "quit")
            assert app.wait(timeout=5) == 0
            assert http_info(port)["app"] == "not-radar"
        finally:
            other.shutdown()
            other.server_close()
        print("PASS: incompatible server error; unrelated server left running")

        for case, backend in (("missing", tmp / "not-installed"), ("failed", Path("/bin/false"))):
            port = free_port()
            app, args, state, log = test_app(tmp, port, backend, case)
            apps.append(app)
            logs.append(log)
            wait_for(lambda: state().get("error") and not state().get("starting"), f"{case} backend error")
            assert not state()["ready"]
            action(port, "quit")
            assert app.wait(timeout=5) == 0
        print("PASS: missing executable and failed backend produce native errors")

        slow = tmp / "slow-backend"
        slow.write_text("#!/usr/bin/env python3\nimport time\ntime.sleep(30)\n")
        slow.chmod(0o755)
        port = free_port()
        app, args, state, log = test_app(tmp, port, slow, "timeout")
        apps.append(app)
        logs.append(log)
        wait_for(lambda: state().get("error") and not state().get("starting"), "startup deadline error", timeout=18)
        assert not state()["ready"]
        action(port, "quit")
        assert app.wait(timeout=8) == 0
        print("PASS: startup deadline and owned-child termination")
    except Exception:
        for log in logs:
            log.flush()
            log.seek(0)
            print(log.read()[-3000:])
        raise
    finally:
        for process in apps + servers:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        for log in logs:
            log.close()
