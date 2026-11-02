import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Gtk from 'gi://Gtk?version=4.0';
import Soup from 'gi://Soup?version=3.0';
import WebKit from 'gi://WebKit?version=6.0';

const addUnixSignal = GLib.check_version(2, 86, 0) === null
    ? (await import('gi://GLibUnix?version=2.0')).default.signal_add
    : GLib.unix_signal_add;
const url = ARGV[0] || 'http://127.0.0.1:47900/';
const address = /^http:\/\/127\.0\.0\.1:(\d+)\/$/.exec(url);
if (!address || Number(address[1]) < 1 || Number(address[1]) > 65535)
    throw new Error('The native window requires a fixed 127.0.0.1 Radar port');
const option = name => {
    const index = ARGV.indexOf(name);
    if (index < 0)
        return null;
    if (!ARGV[index + 1] || ARGV[index + 1].startsWith('--'))
        throw new Error(`Missing value for ${name}`);
    return ARGV[index + 1];
};
const backendPath = option('--backend');
const demo = ARGV.includes('--demo');
const app = new Gtk.Application({
    application_id: 'io.github.awlx.AgentRadar' + (address[1] === '47900' ? '' : `.Port${address[1]}`) +
        (demo ? '.Demo' : ''),
    flags: Gio.ApplicationFlags.DEFAULT_FLAGS,
});
let background = ARGV.includes('--background');
let window, errorLabel, view;
let backend = null, starting = false, ready = !backendPath, quitting = false;
let cancel = null, retry = 0, quitTimer = 0;
const session = new Soup.Session({timeout: 2});
const signals = [];

function showError(message) {
    createWindow();
    errorLabel.label = message;
    errorLabel.visible = true;
    window.present();
    console.error(`Agent Radar: ${message}`);
}

function createWindow() {
    if (window)
        return;
    window = new Gtk.ApplicationWindow({
        application: app, title: 'Agent Radar',
        default_width: 1280, default_height: 800,
    });
    const header = new Gtk.HeaderBar();
    const reload = new Gtk.Button({
        icon_name: 'view-refresh-symbolic', tooltip_text: 'Reconnect to Radar', action_name: 'app.reconnect',
    });
    const quit = new Gtk.Button({label: 'Quit Radar', action_name: 'app.quit'});
    header.pack_end(quit);
    header.pack_end(reload);
    window.set_titlebar(header);
    const box = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL});
    errorLabel = new Gtk.Label({
        label: 'Starting Radar...', wrap: true, visible: !!backendPath && !ready,
        margin_top: 12, margin_bottom: 12,
    });
    view = new WebKit.WebView({
        hexpand: true, vexpand: true,
        network_session: WebKit.NetworkSession.new_ephemeral(),
    });
    view.connect('load-failed', (_view, _event, _uri, failure) => {
        showError(`Radar is disconnected: ${failure.message}`);
        return true;
    });
    view.connect('load-changed', (_view, event) => {
        if (event === WebKit.LoadEvent.STARTED)
            errorLabel.visible = false;
    });
    view.connect('decide-policy', (_view, decision, type) => {
        if (type !== WebKit.PolicyDecisionType.NAVIGATION_ACTION)
            return false;
        if (decision.get_navigation_action().get_request().get_uri() === url)
            return false;
        decision.ignore();
        showError('Navigation outside the local Radar window is not allowed.');
        return true;
    });
    view.connect('web-process-terminated', () => showError('The web view stopped. Select Reconnect.'));
    box.append(errorLabel);
    box.append(view);
    window.set_child(box);
    window.connect('close-request', () => {
        window.hide();
        return true;
    });
    if (ready)
        view.load_uri(url);
}

function probe(done) {
    const requestCancel = cancel;
    const message = Soup.Message.new('GET', url + 'api/info');
    message.set_flags(Soup.MessageFlags.NO_REDIRECT);
    session.send_and_read_async(message, GLib.PRIORITY_DEFAULT, requestCancel, (source, result) => {
        if (quitting || cancel !== requestCancel || requestCancel.is_cancelled())
            return;
        let bytes;
        try {
            bytes = source.send_and_read_finish(result);
        } catch (error) {
            done('unreachable', error.message);
            return;
        }
        if (message.status_code !== 200 || bytes.get_size() > 4096) {
            done('incompatible', `Port ${address[1]} is used by an incompatible server.`);
            return;
        }
        try {
            const info = JSON.parse(new TextDecoder().decode(bytes.get_data()));
            if (info.app !== 'agent-radar' || info.schema !== 1)
                throw new Error('Unknown Radar protocol');
            if (demo && !backend)
                throw new Error('Demo mode cannot reuse a running server.');
        } catch (error) {
            done('incompatible', error.message);
            return;
        }
        done('ready');
    });
}

function startBackend() {
    if (starting || quitting)
        return;
    starting = true;
    ready = false;
    cancel?.cancel();
    cancel = new Gio.Cancellable();
    const deadline = GLib.get_monotonic_time() + 10_000_000;
    const finish = (state, message) => {
        if (state === 'ready') {
            starting = false;
            ready = true;
            if (view)
                view.load_uri(url);
            return;
        }
        if (state === 'incompatible' || GLib.get_monotonic_time() >= deadline) {
            starting = false;
            cancel.cancel();
            if (backend)
                backend.send_signal(15);
            showError(message || 'Radar did not become ready. Quit and start the app again.');
            return;
        }
        if (!backend) {
            try {
                const args = [backendPath, '-gnome', '-listen', `127.0.0.1:${address[1]}`,
                    '-parent-pid', String(Gio.Credentials.new().get_unix_pid())];
                if (demo)
                    args.push('-demo');
                const poll = option('--poll');
                if (poll)
                    args.push('-poll', poll);
                backend = Gio.Subprocess.new(args, Gio.SubprocessFlags.STDOUT_SILENCE);
                const owned = backend;
                owned.wait_async(null, (process, result) => {
                    process.wait_finish(result);
                    if (backend !== owned)
                        return;
                    backend = null;
                    ready = false;
                    starting = false;
                    cancel?.cancel();
                    if (retry)
                        GLib.source_remove(retry);
                    retry = 0;
                    if (quitting)
                        app.quit();
                    else
                        showError('The Radar backend stopped. Select Reconnect to start it again.');
                });
            } catch (error) {
                starting = false;
                showError(`Cannot start the Radar backend: ${error.message}`);
                return;
            }
        }
        retry = GLib.timeout_add(GLib.PRIORITY_DEFAULT, 250, () => {
            retry = 0;
            probe(finish);
            return GLib.SOURCE_REMOVE;
        });
    };
    probe(finish);
}

function quitApp() {
    if (quitting)
        return;
    quitting = true;
    cancel?.cancel();
    if (retry)
        GLib.source_remove(retry);
    retry = 0;
    if (!backend) {
        app.quit();
        return;
    }
    // Only this primary GTK instance's subprocess is ours to stop.
    backend.send_signal(15);
    quitTimer = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, 5, () => {
        quitTimer = 0;
        if (backend) {
            console.error('Agent Radar: backend did not stop after SIGTERM; forcing its exit.');
            backend.force_exit();
        }
        return GLib.SOURCE_REMOVE;
    });
}

app.connect('startup', () => {
    app.hold();
    const quit = new Gio.SimpleAction({name: 'quit'});
    quit.connect('activate', quitApp);
    app.add_action(quit);
    const reconnect = new Gio.SimpleAction({name: 'reconnect'});
    reconnect.connect('activate', () => {
        if (backendPath)
            startBackend();
        else
            view?.load_uri(url);
    });
    app.add_action(reconnect);
    signals.push(addUnixSignal(GLib.PRIORITY_DEFAULT, 15, () => {
        quitApp();
        return GLib.SOURCE_CONTINUE;
    }));
    signals.push(addUnixSignal(GLib.PRIORITY_DEFAULT, 2, () => {
        quitApp();
        return GLib.SOURCE_CONTINUE;
    }));
    if (backendPath)
        startBackend();
});
app.connect('activate', () => {
    if (background) {
        background = false;
        return;
    }
    createWindow();
    window.present();
});
app.connect('shutdown', () => {
    cancel?.cancel();
    session.abort();
    for (const source of [...signals, retry, quitTimer].filter(Boolean))
        GLib.source_remove(source);
    if (backend)
        backend.force_exit();
});
app.run([]);
