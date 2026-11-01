export const BASE_URL = 'http://127.0.0.1:47900/';

export function readStatus(text, now = Date.now()) {
    const data = JSON.parse(text);
    const count = n => Number.isSafeInteger(n) && n >= 0;
    if (data.schema !== 1 || !Number.isSafeInteger(data.now) ||
        now - data.now > 10000 || data.now - now > 60000 ||
        !count(data.working) || !count(data.waiting) || !Array.isArray(data.waits) ||
        data.waits.length !== data.waiting ||
        data.waits.some(s => typeof s.id !== 'string' || !s.id ||
            typeof s.title !== 'string' || typeof s.jumpable !== 'boolean'))
        throw new Error('Invalid or stale Radar status');
    return data;
}

export function displayStatus(data) {
    if (!data)
        return {label: 'Radar !', title: 'Radar disconnected - data is stale', state: 'offline', icon: 'network-offline-symbolic'};
    if (data.waiting)
        return {label: `${data.working} / ${data.waiting}`, title: `${data.working} working, ${data.waiting} waiting for you`, state: 'waiting', icon: 'dialog-warning-symbolic'};
    if (data.working)
        return {label: `${data.working}`, title: `${data.working} working, none waiting`, state: 'working', icon: 'emblem-ok-symbolic'};
    return {label: '0', title: 'No agent is active', state: 'idle', icon: 'media-playback-pause-symbolic'};
}
