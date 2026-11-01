import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import Soup from 'gi://Soup?version=3.0';
import St from 'gi://St';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import {BASE_URL, readStatus, displayStatus} from './status.js';

const Indicator = GObject.registerClass(class Indicator extends PanelMenu.Button {
    _init() {
        super._init(0.0, 'Agent Radar');
        const box = new St.BoxLayout({style_class: 'panel-status-menu-box'});
        this.icon = new St.Icon({style_class: 'system-status-icon'});
        this.label = new St.Label({y_align: 2});
        box.add_child(this.icon);
        box.add_child(this.label);
        this.add_child(box);
    }
});

export default class AgentRadar extends Extension {
    enable() {
        this._indicator = new Indicator();
        this._session = new Soup.Session({timeout: 4});
        this._cancel = new Gio.Cancellable();
        this._inFlight = false;
        this._render(null);
        Main.panel.addToStatusArea(this.uuid, this._indicator);
        this._poll();
        this._timer = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, 2, () => {
            this._poll();
            return GLib.SOURCE_CONTINUE;
        });
    }

    _render(data) {
        const display = displayStatus(data);
        this._indicator.icon.icon_name = display.icon;
        this._indicator.icon.style_class = `system-status-icon radar-${display.state}`;
        this._indicator.label.text = display.label;
        this._indicator.accessible_name = `Agent Radar: ${display.title}`;
        // Do not replace focused menu items just because the snapshot time changed.
        const key = JSON.stringify(data ? [data.working, data.waiting, data.waits] : null);
        if (this._menuKey === key)
            return;
        this._menuKey = key;
        this._indicator.menu.removeAll();
        this._statusItem = new PopupMenu.PopupMenuItem(display.title, {reactive: false});
        this._indicator.menu.addMenuItem(this._statusItem);
        if (data?.waiting) {
            this._indicator.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem('Waiting for you'));
            for (const wait of data.waits.slice(0, 50)) {
                const item = new PopupMenu.PopupMenuItem(wait.title);
                item.label.add_style_class_name('radar-session');
                item.connect('activate', () => {
                    if (wait.jumpable)
                        this._jump(wait.id);
                    else
                        this._open();
                });
                this._indicator.menu.addMenuItem(item);
            }
            if (data.waiting > 50)
                this._indicator.menu.addMenuItem(new PopupMenu.PopupMenuItem('More sessions in Radar', {reactive: false}));
            this._indicator.menu.addMenuItem(new PopupMenu.PopupMenuItem(
                'Terminal sessions open Radar; Wayland cannot select their tabs.', {reactive: false}));
        }
        this._indicator.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        const open = new PopupMenu.PopupMenuItem('Open Radar');
        open.connect('activate', () => this._open());
        this._indicator.menu.addMenuItem(open);
    }

    _open() {
        try {
            const desktop = Gio.DesktopAppInfo.new('io.github.awlx.AgentRadar.desktop');
            if (!desktop)
                throw new Error('Install the Agent Radar desktop launcher first');
            desktop.launch([], global.create_app_launch_context(0, -1));
        } catch (error) {
            this._statusItem.label.text = `Cannot open Radar: ${error.message}`;
            console.error(`Agent Radar: ${error.message}`);
        }
    }

    _request(path, method, done) {
        const session = this._session;
        const cancel = this._cancel;
        const message = Soup.Message.new(method, BASE_URL + path);
        message.set_flags(Soup.MessageFlags.NO_REDIRECT);
        message.request_headers.replace('Origin', BASE_URL.slice(0, -1));
        session.send_and_read_async(message, GLib.PRIORITY_DEFAULT, cancel, (source, result) => {
            // A callback from a disabled or previous instance must not change Shell actors.
            if (this._cancel !== cancel || cancel.is_cancelled())
                return;
            let text;
            try {
                const bytes = source.send_and_read_finish(result);
                if (message.status_code !== 200 && message.status_code !== 204)
                    throw new Error(`Local backend returned HTTP ${message.status_code}`);
                if (bytes.get_size() > 256 * 1024)
                    throw new Error('Radar status is too large');
                text = new TextDecoder().decode(bytes.get_data());
            } catch (error) {
                done(error);
                return;
            }
            done(null, text);
        });
    }

    _poll() {
        if (this._inFlight)
            return;
        this._inFlight = true;
        this._request('api/status', 'GET', (error, text) => {
            this._inFlight = false;
            try {
                if (error)
                    throw error;
                this._render(readStatus(text));
            } catch (failure) {
                if (this._menuKey !== 'null')
                    console.error(`Agent Radar disconnected: ${failure.message}`);
                this._render(null);
            }
        });
    }

    _jump(id) {
        this._request(`api/jump?session=${encodeURIComponent(id)}`, 'POST', error => {
            if (error) {
                this._statusItem.label.text = 'Cannot show this session. Open Radar to inspect it.';
                console.error(`Agent Radar: ${error.message}`);
            }
        });
    }

    disable() {
        if (this._timer)
            GLib.source_remove(this._timer);
        this._timer = 0;
        this._cancel?.cancel();
        this._session?.abort();
        this._cancel = null;
        this._session = null;
        this._indicator?.destroy();
        this._indicator = null;
        this._statusItem = null;
        this._menuKey = undefined;
    }
}
