#!/usr/bin/env python3
"""Check the native WebKit window with demo data on a graphical Linux session."""
import pathlib
import re
import subprocess
import tempfile
import urllib.request

root = pathlib.Path(__file__).resolve().parent.parent
backend = subprocess.Popen(
    [str(root / "dist/linux/agent-radar"), "-demo", "-listen", "127.0.0.1:0"],
    stderr=subprocess.PIPE, text=True,
)
try:
    line = backend.stderr.readline()
    match = re.search(r"http://127\.0\.0\.1:(\d+)", line)
    if not match:
        raise RuntimeError(f"Backend did not start: {line}")
    url = f"http://127.0.0.1:{match[1]}/"
    with urllib.request.urlopen(url + "healthz", timeout=5) as response:
        assert response.status == 200
    source = (root / "internal/tray/window_linux.js").read_text()
    source = source.replace("'io.github.awlx.AgentRadar'", "'io.github.awlx.AgentRadar.SmokeTest'")
    checks = """
let passed = false;
GLib.timeout_add(GLib.PRIORITY_DEFAULT, 250, () => {
    if (!window?.get_visible())
        return GLib.SOURCE_CONTINUE;
    const view = window.get_child().get_first_child().get_next_sibling();
    view.evaluate_javascript(`JSON.stringify({
        conn: document.getElementById('conn')?.textContent,
        sessions: typeof snapshot === 'object' ? snapshot?.sessions?.length : 0,
        graphNodes: typeof nodes === 'object' ? nodes.size : 0,
        jump: 'jump' in document.body.dataset,
        canvas: !!document.querySelector('canvas')
    })`, -1, null, null, null, (_view, result) => {
        try {
            const data = JSON.parse(view.evaluate_javascript_finish(result).to_string());
            if (data.conn !== 'live' || data.sessions !== 5 || data.graphNodes <= 5 || !data.canvas || data.jump)
                return;
            if (!view.get_network_session().is_ephemeral)
                throw new Error('The window used persistent storage');
            print('PASS: native GTK window, live demo graph, ephemeral storage, no demo jump');
            passed = true;
            app.activate_action('quit', null);
        } catch (error) {
            printerr(error.message);
        }
    });
    return GLib.SOURCE_CONTINUE;
});
GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, 12, () => {
    if (!passed)
        printerr('FAIL: the native demo graph did not become live');
    app.quit();
    return GLib.SOURCE_REMOVE;
});
app.run([]);
if (!passed)
    throw new Error('Native smoke test failed');
"""
    source = source.replace("app.run([]);", checks)
    with tempfile.TemporaryDirectory(prefix="agent-radar-native-test-") as tmp:
        script = pathlib.Path(tmp) / "smoke.js"
        script.write_text(source)
        subprocess.run(["gjs", "-m", str(script), url], check=True, timeout=20)
finally:
    backend.terminate()
    backend.wait(timeout=5)
