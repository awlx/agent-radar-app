import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('./status.js', import.meta.url), 'utf8');
const {readStatus, displayStatus} = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
const now = 1800000000000;
const status = {schema: 1, now, working: 2, waiting: 1,
    waits: [{id: 'demo-session', title: 'Demo - Copilot app', jumpable: true}]};

test('working, waiting, idle and disconnected are distinct', () => {
    assert.equal(displayStatus(readStatus(JSON.stringify(status), now)).state, 'waiting');
    assert.equal(displayStatus({...status, waiting: 0}).state, 'working');
    assert.equal(displayStatus({...status, working: 0, waiting: 0}).state, 'idle');
    assert.equal(displayStatus(null).state, 'offline');
    assert.match(displayStatus(null).title, /stale/);
});

test('invalid, stale or incompatible state never becomes idle', () => {
    for (const value of [null, {}, {...status, schema: 2}, {...status, now: now - 10001},
        {...status, now: now + 60001}, {...status, working: -1}, {...status, working: 1.5},
        {...status, waits: []}, {...status, waits: [{id: '', title: 'Demo', jumpable: true}]}])
        assert.throws(() => readStatus(JSON.stringify(value), now));
    assert.throws(() => readStatus('not JSON', now));
});
