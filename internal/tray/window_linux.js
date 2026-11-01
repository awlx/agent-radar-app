import Gio from 'gi://Gio';
import Gtk from 'gi://Gtk?version=4.0';
import WebKit from 'gi://WebKit?version=6.0';

const url = ARGV[0] || 'http://127.0.0.1:47900/';
const address = /^http:\/\/127\.0\.0\.1:(\d+)\/(?:\?token=[^#]*)?$/.exec(url);
if (!address)
    throw new Error('The native window requires a loopback Radar URL');

const app = new Gtk.Application({
    application_id: 'io.github.awlx.AgentRadar' + (address[1] === '47900' ? '' : `.Port${address[1]}`),
    flags: Gio.ApplicationFlags.DEFAULT_FLAGS,
});
let background = ARGV.includes('--background');
const keepRunning = background;
let window;
app.connect('startup', () => {
    if (keepRunning)
        app.hold();
});
app.connect('activate', () => {
    if (background) {
        background = false;
        return;
    }
    if (!window) {
        window = new Gtk.ApplicationWindow({
            application: app, title: 'Agent Radar',
            default_width: 1280, default_height: 800,
        });
        const header = new Gtk.HeaderBar();
        const reload = new Gtk.Button({icon_name: 'view-refresh-symbolic', tooltip_text: 'Reconnect to Radar'});
        header.pack_end(reload);
        window.set_titlebar(header);
        const box = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL});
        const error = new Gtk.Label({
            label: 'Radar is disconnected. Start the local backend, then select Reconnect.',
            wrap: true, visible: false, margin_top: 12, margin_bottom: 12,
        });
        // Session data must not remain in a persistent browser profile.
        const view = new WebKit.WebView({
            hexpand: true, vexpand: true,
            network_session: WebKit.NetworkSession.new_ephemeral(),
        });
        view.connect('load-failed', (_view, _event, _uri, failure) => {
            error.label = `Radar is disconnected: ${failure.message}`;
            error.visible = true;
            return true;
        });
        view.connect('load-changed', (_view, event) => {
            if (event === WebKit.LoadEvent.STARTED)
                error.visible = false;
        });
        view.connect('decide-policy', (_view, decision, type) => {
            if (type !== WebKit.PolicyDecisionType.NAVIGATION_ACTION)
                return false;
            const target = decision.get_navigation_action().get_request().get_uri();
            if (target === url || target === url.split('?')[0])
                return false;
            decision.ignore();
            error.label = 'Navigation outside the local Radar window is not allowed.';
            error.visible = true;
            return true;
        });
        view.connect('web-process-terminated', () => {
            error.label = 'The web view stopped. Select Reconnect.';
            error.visible = true;
        });
        reload.connect('clicked', () => view.load_uri(url));
        box.append(error);
        box.append(view);
        window.set_child(box);
        window.connect('close-request', () => {
            if (keepRunning)
                window.hide();
            return keepRunning;
        });
        view.load_uri(url);
    }
    window.present();
});
app.run([]);
