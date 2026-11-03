import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const source = readFileSync(new URL('./extension.js', import.meta.url), 'utf8');
const statusSource = readFileSync(new URL('./status.js', import.meta.url), 'utf8');
const {displayStatus, readStatus} = await import(`data:text/javascript;base64,${Buffer.from(statusSource).toString('base64')}`);

function extension({missing = false} = {}) {
    let launches = 0, clock = 0, callback;
    const indicator = {
        icon: {}, label: {}, menu: {removeAll() {}, addMenuItem() {}}, destroy() {},
    };
    const context = {
        console: {error() {}}, Map, JSON, TextDecoder, displayStatus, readStatus,
        Extension: class {},
        Indicator: function () { return indicator; },
        Gio: {
            FileIcon: class {},
            Cancellable: class { cancel() {} },
            DesktopAppInfo: {new() { return missing ? null : {
                list_actions: () => ['background'],
                launch_action(action) { assert.equal(action, 'background'); launches++; },
            }; }},
        },
        GLib: {
            get_monotonic_time: () => clock, timeout_add_seconds: () => 1,
            source_remove() {}, SOURCE_CONTINUE: true,
        },
        Soup: {Session: class { abort() {} }},
        PopupMenu: {
            PopupMenuItem: class { constructor(text) { this.label = {text}; } connect() {} },
            PopupSeparatorMenuItem: class {},
        },
        Main: {panel: {addToStatusArea() {}}},
        global: {create_app_launch_context() {}},
    };
    vm.createContext(context);
    vm.runInContext(source.slice(source.indexOf('export default class')).replace('export default class', 'globalThis.Radar = class'), context);
    const radar = new context.Radar();
    radar.dir = {get_child() { return this; }};
    radar._request = (_path, _method, done) => { callback = done; };
    radar.enable();
    return {
        radar, indicator, get launches() { return launches; },
        fail() { callback(new Error('not available')); },
        advance() { clock += 13_000_000; },
        ready() { callback(null, JSON.stringify({schema: 1, now: Date.now(), working: 0, waiting: 0, waits: []})); },
    };
}

test('enable launches once; startup timeout and later poll failures never relaunch', () => {
    const state = extension();
    assert.equal(state.launches, 1);
    assert.match(state.indicator.accessible_name, /Starting/);
    state.fail();
    assert.match(state.indicator.accessible_name, /Starting/);
    state.advance();
    state.radar._poll();
    state.fail();
    assert.match(state.indicator.accessible_name, /could not start/);
    state.radar._poll();
    state.ready();
    assert.match(state.indicator.accessible_name, /No agent is active/);
    for (let i = 0; i < 3; i++) {
        state.radar._poll();
        state.fail();
    }
    assert.equal(state.launches, 1);
    assert.match(state.indicator.accessible_name, /disconnected/);
    state.radar.disable();
    state.radar.enable();
    assert.equal(state.launches, 2);
    state.radar.disable();
    assert.equal(state.radar._startup, null);
    assert.equal(state.radar._startupDeadline, 0);
});

test('missing background desktop action reports a startup error, never idle', () => {
    const state = extension({missing: true});
    assert.equal(state.launches, 0);
    assert.match(state.indicator.accessible_name, /could not start/);
    state.fail();
    assert.match(state.indicator.accessible_name, /could not start/);
    state.radar.disable();
});
