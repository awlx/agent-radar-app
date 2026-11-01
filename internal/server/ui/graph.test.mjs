import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const html = readFileSync(new URL("./ui.html", import.meta.url), "utf8");
const source = html.split("<script>")[1].split("// ---------- Panels ----------")[0];
const panelSource = html.split("<script>")[1].split("// Feed resize grip")[0];

function graph(width = 1000, height = 500, panels = false) {
  const labels = [], paths = [], arcs = [], listeners = new Map(), requests = [];
  const ctx = new Proxy({
    measureText: (text) => ({ width: text.length * 8 }),
    getImageData: () => ({ data: [13, 17, 23, 255] }),
    fillText: (text, x, y) => labels.push({ text, x, y }),
    moveTo: (x, y) => paths.push(["move", x, y]),
    lineTo: (x, y) => paths.push(["line", x, y]),
    arc: (x, y, r) => arcs.push({ x, y, r }),
  }, { get: (target, key) => key in target ? target[key] : () => {} });
  const elements = new Map();
  const on = (id, type, listener) => {
    const key = id + ":" + type;
    if (!listeners.has(key)) listeners.set(key, []);
    listeners.get(key).push(listener);
  };
  const element = (id) => {
    if (!elements.has(id)) elements.set(id, {
      style: { setProperty() {} }, clientWidth: width, clientHeight: height,
      innerHTML: "", checked: false, hidden: true, offsetWidth: 260, offsetHeight: 100,
      dataset: {}, addEventListener: (type, listener) => on(id, type, listener), getContext: () => ctx,
      contains(target) { return this === target || this.querySelectorAll("button").includes(target); },
      setAttribute(key, value) { this[key] = value; },
      focus() { document.activeElement = this; },
      getBoundingClientRect: () => ({ left: 20, bottom: 60 }),
      click() { return this.onclick?.(); },
      querySelector(selector) { return this.querySelectorAll(selector)[0]; },
      querySelectorAll(selector) {
        if (selector === ".details details") return [];
        const cards = selector === ".card", pattern = cards ? /<div class="card [^>]+>/g : /<button[^>]+>[^<]*<\/button>/g;
        if (this.parsedHTML !== this.innerHTML) { this.parsedHTML = this.innerHTML; this.parsed = new Map(); }
        if (!this.parsed?.has(selector)) this.parsed.set(selector, [...this.innerHTML.matchAll(pattern)].map(([tag]) => {
          const attrs = Object.fromEntries([...tag.matchAll(/([\w-]+)="([^"]*)"/g)].map(([, k, v]) => [k, v]));
          const el = { ...element("parsed:" + id + ":" + attrs["data-id"]), dataset: {}, textContent: tag.replace(/<[^>]+>/g, "") };
          for (const [k, v] of Object.entries(attrs)) if (k.startsWith("data-")) el.dataset[k.slice(5)] = v;
          return el;
        }));
        return this.parsed.get(selector);
      },
    });
    return elements.get(id);
  };
  const document = {
    getElementById: element, createElement: element, body: element("body"),
    addEventListener: (type, listener) => on("document", type, listener),
    querySelectorAll: (selector) => element("list").querySelectorAll(selector),
  };
  const sandbox = {
    document, innerWidth: width, innerHeight: height,
    window: { devicePixelRatio: 2 }, performance: { now: () => 1000 },
    matchMedia: () => ({ matches: false, addEventListener() {} }),
    getComputedStyle: () => ({ backgroundColor: "#0d1117", getPropertyValue: () => "" }),
    ResizeObserver: class { observe() {} },
    requestAnimationFrame() {}, renderSidebar() {},
    getSelection: () => null, addEventListener: (type, listener) => on("window", type, listener),
    CSS: { escape: (s) => s }, setTimeout() {},
    fetch: async (url, options) => { requests.push({ url, options }); return { ok: true }; },
  };
  vm.createContext(sandbox);
  vm.runInContext((panels ? panelSource : source) + `
    reduceMotion = true;
    globalThis.graph = { build, draw, pick, toggleSelect, nodeInfo, label, nodes, focusedNodes, messagePoint, drawMessages, spawnParticles,
      setFocus, sessionActions, openSessionMenu, closeSessionMenu, runSessionAction,
      ${panels ? "showApp," : ""}
      get focusID() { return focusID; },
      setReducedMotion(value) { reduceMotion = value; },
      get messagePulses() { return messagePulses; }, get particles() { return particles; },
      get links() { return links; }, get selected() { return selected; },
      get size() { return [W, H]; } };
  `, sandbox);
  // Sidebar rendering is covered by browser interaction checks.
  sandbox.sidebarArgs = null;
  sandbox.highlightCard = () => {};
  element("showIdle");
  return {
    api: sandbox.graph, elements, labels, paths, arcs, requests, document,
    setFetch(fn) { sandbox.fetch = fn; },
    emit(id, type, event = {}) { for (const listener of listeners.get(id + ":" + type) || []) listener(event); },
  };
}

function session(id, count = 0) {
  return {
    id, name: "Parent " + id, surface: "Copilot CLI", busy: true, waiting: false,
    lastEventAt: Date.now(), tokens: { in: 0, out: 0 },
    runningTools: [], flows: [], peers: [],
    subagents: Array.from({ length: count }, (_, i) => ({
      id: "child-" + i, name: "Worker " + i, running: true,
      model: "Demo model", doing: "Inspecting fabricated test files",
    })),
  };
}
const snapshot = (...sessions) => ({ now: Date.now(), sessions, ides: [] });

test("children stay below and inside their parent group, including large batches", () => {
  const { api, elements } = graph(420);
  api.build(snapshot(session("one", 40), session("two", 3)));
  assert.equal(api.size[0], 900);
  assert.equal(elements.get("graphHint").hidden, false);
  assert.ok(api.size[1] > 2200);
  const parent = api.nodes.get("s:one"), next = api.nodes.get("s:two");
  const children = [...api.nodes.values()].filter((n) => n.parent?.id === "one");
  assert.equal(children.length, 40);
  let previous = parent.ty;
  for (const child of children) {
    assert.ok(child.tx > parent.tx);
    assert.ok(child.ty - previous >= 52);
    assert.ok(child.ty + 20 < parent.ty + (parent.rows - .5) * parent.slot);
    assert.ok(child.tx + child.r + 7 + child.labelWidth < parent.familyRight);
    previous = child.ty;
  }
  assert.ok(next.ty - previous >= 52);
  assert.equal(api.links.filter((l) => l.kind === "sub").length, 43);
});

test("idle filtering keeps node identities and data flow attribution", () => {
  const { api, elements } = graph();
  const s = session("one", 2);
  s.subagents[1].running = false;
  s.flows.push({ sink: "Filesystem", agentId: "child-0", endAt: Date.now() });
  api.build(snapshot(s));
  const child = api.nodes.get("a:one:child-0");
  assert.ok(child);
  assert.ok(!api.nodes.has("a:one:child-1"));
  assert.equal(api.links.find((l) => l.kind === "flow").a, child.id);
  elements.get("showIdle").checked = true;
  api.build(snapshot(s));
  assert.equal(api.nodes.get(child.id), child);
  assert.ok(api.nodes.has("a:one:child-1"));
  s.subagents = [];
  api.build(snapshot(s));
  assert.ok(!api.nodes.has(child.id));
  assert.equal(api.links.find((l) => l.kind === "flow").a, "s:one");
});

test("in-flight particles follow rebuilt links and drop with removed links", () => {
  const { api, elements } = graph();
  const idle = session("two");
  idle.busy = false;
  const s = session("one", 1);
  api.setReducedMotion(false);
  api.build(snapshot(s, idle));
  api.spawnParticles(1000);
  assert.ok(api.particles.some((p) => p.l.b === "a:one:child-0"));
  elements.get("showIdle").checked = true;
  api.build(snapshot(s, idle));
  assert.ok(api.particles.length > 0);
  for (const p of api.particles) assert.ok(api.links.includes(p.l));
  elements.get("showIdle").checked = false;
  s.subagents[0].running = false;
  api.build(snapshot(s, idle));
  assert.ok(api.particles.length > 0);
  assert.ok(!api.particles.some((p) => p.l.b === "a:one:child-0"));
  for (const p of api.particles) assert.ok(api.links.includes(p.l));
});

test("drawing shows diamond, name, status and task with pixel-bounded labels", () => {
  const { api, labels, paths } = graph(900);
  const s = session("one", 1);
  s.subagents[0].name = "W".repeat(100);
  api.build(snapshot(s));
  api.draw(1000);
  const child = api.nodes.get("a:one:child-0");
  const lines = labels.filter((l) => l.x === child.x + child.r + 7);
  assert.equal(lines.length, 3);
  assert.ok(lines[0].text.endsWith("…"));
  assert.match(lines[1].text, /^Running/);
  assert.match(lines[2].text, /^Inspecting/);
  for (const line of lines) assert.ok(line.text.length * 8 <= child.labelWidth);
  assert.ok(paths.some(([kind, x, y]) => kind === "move" && x === child.x && y === child.y - child.r));
  assert.ok(paths.some(([kind, x, y]) => kind === "line" && x === child.x + child.r && y === child.y));
  assert.equal(api.pick(child.x, child.y), child);
  assert.match(api.nodeInfo(child), /in Parent one/);
  api.toggleSelect(child);
  assert.equal(api.selected, child);
  api.draw(1000);
  api.toggleSelect(child);
  assert.equal(api.selected, null);
});

test("completed and tool-free subagents retain explicit status and task text", () => {
  const { api, elements, labels } = graph();
  const s = session("one", 2);
  s.subagents[0].running = false;
  s.subagents[0].doing = "";
  s.subagents[1].doing = "";
  elements.get("showIdle").checked = true;
  api.build(snapshot(s));
  api.draw(1000);
  assert.ok(labels.some((l) => l.text.startsWith("Done")));
  assert.ok(labels.some((l) => l.text === "Task complete"));
  assert.ok(labels.some((l) => l.text === "Thinking"));
});

test("empty snapshots and wide layouts remain finite and bounded", () => {
  const { api } = graph(1600, 800);
  api.build(snapshot());
  api.draw(1000);
  assert.equal(api.size[0], 1600);
  assert.equal(api.nodes.size, 1);
  api.build(snapshot(session("one", 2)));
  for (const n of api.nodes.values()) {
    assert.ok(Number.isFinite(n.tx) && Number.isFinite(n.ty));
    assert.ok(n.ty + 20 < api.size[1]);
  }
});

test("the details offer Show app only when the app can show the session", () => {
  const { api, elements } = graph(1000, 500, true);
  const s = session("one");
  s.pid = 42;
  const button = () => elements.get("list").innerHTML.match(/<button[^>]*class="jump"[^>]*>/)?.[0];
  api.build(snapshot(s));
  api.toggleSelect(api.nodes.get("s:one"));
  assert.equal(button(), undefined);
  elements.get("body").dataset = { jump: "" };
  api.build(snapshot(s));
  assert.match(button(), /data-session="one"/);
  s.pid = 0;
  api.build(snapshot(s));
  assert.equal(button(), undefined);
  s.surface = "Copilot app";
  api.build(snapshot(s));
  assert.match(button(), /data-session="one"/);
});

test("background shells stay visible under idle or disconnected owners without becoming agents", () => {
  const { api, elements, labels } = graph(1000, 500, true);
  const s = session("owner", 1);
  s.busy = false;
  s.live = false;
  s.subagents[0].running = false;
  s.backgroundShells = [{ id: "preview-server", pid: 123, startedAt: Date.now() - 3600000 }];
  api.build(snapshot(s));
  const parent = api.nodes.get("s:owner"), shell = api.nodes.get("b:owner:preview-server");
  assert.ok(parent && shell);
  assert.equal(shell.type, "shell");
  assert.equal(parent.rows, 2);
  assert.equal(api.nodes.has("a:owner:child-0"), false);
  assert.equal(api.links.filter((l) => l.kind === "sub").length, 0);
  assert.equal(api.links.filter((l) => l.kind === "background").length, 1);
  assert.equal(elements.get("sBusy").textContent, 0);
  assert.equal(elements.get("sSub").textContent, 0);
  assert.equal(elements.get("sShell").textContent, 1);
  assert.equal(elements.get("sAll").textContent, 1);
  assert.match(elements.get("list").innerHTML, /running background shell/);
  api.draw(1000);
  assert.ok(labels.some((l) => l.text === "Running · background shell"));
  assert.ok(labels.some((l) => l.text === "Detached process · PID 123"));
  assert.ok(shell.ty > parent.ty && shell.ty + 20 < parent.ty + (parent.rows - .5) * parent.slot);
  assert.equal(api.pick(shell.x, shell.y), shell);
  assert.match(api.nodeInfo(shell), /not a sub-agent/);
  assert.match(api.nodeInfo(shell), /Owner not connected/);
  api.toggleSelect(shell);
  assert.match(elements.get("list").innerHTML, /<details[^>]* open/);
  assert.match(elements.get("list").innerHTML, /background shell · running · pid 123/);
  elements.get("showIdle").checked = true;
  api.build(snapshot(s));
  assert.equal(api.nodes.get(shell.id), shell);
  assert.equal(api.nodes.get(parent.id).rows, 3);
  assert.equal(elements.get("sSub").textContent, 0);
  s.backgroundShells = [];
  api.build(snapshot(s));
  assert.equal(api.nodes.has(shell.id), false);
  assert.equal(api.selected, null);
  elements.get("showIdle").checked = false;
  api.build(snapshot(s));
  assert.equal(api.nodes.has(parent.id), false);
  assert.equal(elements.get("sShell").textContent, 0);
});

test("idle parent → child session → subagent remains a durable, nested ownership chain", () => {
  const { api, elements, labels } = graph(420, 500, true);
  const parent = session("parent"), child = session("child", 2), sibling = session("sibling");
  parent.busy = false;
  parent.surface = "Copilot app";
  parent.childSessions = [child.id, sibling.id];
  parent.peers = [{ to: child.id, kind: "spawn", at: Date.now() - 86400000 }];
  child.surface = "Copilot CLI";
  child.backgroundShells = [{ id: "server", pid: 123, startedAt: Date.now() }];
  child.flows = [{ sink: "Filesystem", agentId: "child-0", endAt: Date.now() }];
  api.build(snapshot(child, sibling, parent));
  const root = api.nodes.get("s:parent"), branch = api.nodes.get("s:child");
  const worker = api.nodes.get("a:child:child-0"), shell = api.nodes.get("b:child:server");
  assert.ok(root.s.contextOnly);
  assert.equal(branch.parentSessionID, root.id);
  assert.ok(branch.tx > root.tx && worker.tx > branch.tx);
  assert.ok(branch.ty > root.ty && worker.ty > branch.ty);
  assert.ok(shell.ty + 20 < branch.ty + (branch.rows - .5) * branch.slot);
  assert.ok(shell.ty + 20 < root.ty + (root.rows - .5) * root.slot);
  assert.equal(api.links.filter((l) => l.kind === "child").length, 2);
  assert.ok(!api.links.some((l) => l.kind === "own" && l.b === branch.id));
  assert.ok(!api.links.some((l) => l.kind === "peer"));
  const focused = api.focusedNodes(worker);
  assert.ok(focused.has(root.id) && focused.has(branch.id) && focused.has("k:Filesystem"));
  assert.ok(!focused.has("s:sibling") && !focused.has(shell.id) && !focused.has("a:child:child-1"));
  const sinkFocus = api.focusedNodes(api.nodes.get("k:Filesystem"));
  assert.ok(sinkFocus.has(root.id) && sinkFocus.has(worker.id));
  assert.ok(!sinkFocus.has("s:sibling"));
  api.draw(1000);
  assert.ok(labels.some((l) => l.text.startsWith("Idle parent context")));
  assert.ok(labels.some((l) => l.text.startsWith("Child session")));
  assert.match(api.nodeInfo(branch), /Child session of Parent parent/);
  assert.equal(elements.get("sBusy").textContent, 2);
  assert.equal(elements.get("sSub").textContent, 2);
  assert.equal(elements.get("sShell").textContent, 1);
  child.busy = false;
  child.subagents.forEach((a) => a.running = false);
  child.backgroundShells = [];
  sibling.busy = false;
  api.build(snapshot(parent, child, sibling));
  assert.equal(api.nodes.has(root.id), false);
  elements.get("showIdle").checked = true;
  api.build(snapshot(parent, child, sibling));
  assert.equal(api.nodes.get(branch.id).parentSessionID, root.id);
});

test("messages travel once along ownership in the correct direction, without extra edges", () => {
  const { api } = graph();
  const a = session("a"), b = session("b");
  a.childSessions = [b.id];
  const snap = snapshot(a, b);
  a.peers = [{ id: "history", to: b.id, kind: "message", at: snap.now - 1000 }];
  api.build(snap);
  assert.equal(api.messagePulses.length, 0);
  a.peers.push({ id: "new", to: b.id, kind: "message", at: snap.now + 1 });
  snap.now += 2;
  api.build(snap);
  api.draw(1000);
  assert.equal(api.messagePulses.length, 1);
  const forward = api.messagePulses[0], source = api.nodes.get("s:a"), target = api.nodes.get("s:b");
  assert.equal(api.messagePoint(forward, 0)[1], source.y + source.r);
  assert.equal(api.messagePoint(forward, 1)[1], target.y);
  assert.equal(api.links.filter((l) => l.kind === "child").length, 1);
  assert.ok(!api.links.some((l) => l.kind === "peer"));
  api.build(snap);
  assert.equal(api.messagePulses.length, 1);
  b.peers.push({ id: "reply", to: a.id, kind: "message", at: snap.now + 1 });
  snap.now += 2;
  api.build(snap);
  const reverse = api.messagePulses[1];
  assert.equal(api.messagePoint(reverse, 0)[1], target.y);
  assert.equal(api.messagePoint(reverse, 1)[1], source.y + source.r);
  api.drawMessages(2201);
  assert.equal(api.messagePulses.length, 0);
  api.build(snap);
  assert.equal(api.messagePulses.length, 0);
});

test("unrelated messages are transient only and reduced motion stays stationary", () => {
  const { api, arcs } = graph();
  const a = session("a"), b = session("b");
  const snap = snapshot(a, b);
  api.build(snap);
  a.peers.push({ id: "new", to: b.id, kind: "message", at: snap.now + 1 });
  snap.now += 2;
  api.build(snap);
  const receiver = api.nodes.get("s:b");
  assert.equal(receiver.parentSessionID, null);
  assert.ok(!api.links.some((l) => l.kind === "child" || l.kind === "peer"));
  api.drawMessages(1000);
  api.drawMessages(1800);
  assert.deepEqual(arcs.slice(-2), [
    { x: receiver.x, y: receiver.y - receiver.r - 7, r: 3.5 },
    { x: receiver.x, y: receiver.y - receiver.r - 7, r: 3.5 },
  ]);
  api.setReducedMotion(false);
  const pulse = api.messagePulses[0];
  assert.notEqual(api.messagePoint(pulse, .2)[1], api.messagePoint(pulse, .8)[1]);
  api.drawMessages(2201);
  assert.equal(api.messagePulses.length, 0);
  const message = a.peers.pop();
  api.build(snap);
  a.peers.push(message);
  api.build(snap);
  assert.equal(api.messagePulses.length, 0);
});

test("nested ancestry is cycle safe, deterministic, and never creates missing or alias nodes", () => {
  const { api } = graph(420);
  const a = session("a"), b = session("b"), c = session("c", 1);
  a.busy = b.busy = false;
  a.childSessions = [b.id, "missing-app-alias", a.id];
  b.childSessions = [c.id];
  c.childSessions = [a.id];
  api.build(snapshot(c, b, a));
  assert.ok(api.nodes.has("s:a") && api.nodes.has("s:b") && api.nodes.has("s:c"));
  assert.ok(!api.nodes.has("s:missing-app-alias"));
  assert.equal(api.links.filter((l) => l.kind === "child").length, 2);
  assert.ok(api.nodes.get("s:a").ty < api.nodes.get("s:b").ty);
  assert.ok(api.nodes.get("s:b").ty < api.nodes.get("s:c").ty);
  assert.ok(api.size[0] >= 1060);
});

test("resumed background agents reappear under the same owner, completed history stays opt-in", () => {
  const { api, elements } = graph(1000, 500, true);
  const s = session("owner", 4);
  s.busy = false;
  s.backgroundShells = [{ id: "server", pid: 123, startedAt: Date.now() }];
  s.subagents.forEach((a) => { a.running = false; a.mode = "background"; });
  api.build(snapshot(s));
  assert.equal([...api.nodes.values()].filter((n) => n.type === "agent").length, 0);
  assert.match(elements.get("subHistory").textContent, /4 inactive hidden/);
  elements.get("showIdle").checked = true;
  api.build(snapshot(s));
  assert.equal([...api.nodes.values()].filter((n) => n.type === "agent").length, 4);
  assert.equal(elements.get("subHistory").textContent, " · 4 inactive");
  elements.get("showIdle").checked = false;
  s.subagents[3].running = true;
  s.flows = [{ sink: "Filesystem", agentId: "child-3", endAt: Date.now() }];
  api.build(snapshot(s));
  assert.equal([...api.nodes.values()].filter((n) => n.type === "agent").length, 1);
  assert.match(elements.get("subHistory").textContent, /3 inactive hidden/);
  assert.equal(api.links.find((l) => l.kind === "flow").a, "a:owner:child-3");
  assert.equal(api.nodes.get("a:owner:child-3").parent.id, s.id);
  assert.equal(api.nodes.get("b:owner:server").type, "shell");
});

test("session focus scopes the graph, counters, feed and model history without following peers", () => {
  const { api, elements } = graph(1000, 500, true);
  const parent = session("parent"), root = session("root", 2), child = session("child"), leaf = session("leaf"), peer = session("peer");
  parent.childSessions = [root.id, peer.id];
  root.childSessions = [child.id];
  root.subagents[1].running = false;
  child.childSessions = [leaf.id];
  child.busy = leaf.busy = false;
  root.backgroundShells = [{ id: "server", pid: 123 }];
  root.flows = [{ sink: "Filesystem", tool: "view", desc: "Root work", endAt: Date.now() }];
  peer.flows = [{ sink: "Outside sink", tool: "view", desc: "Unrelated work", endAt: Date.now() }];
  root.peers = [{ to: peer.id, kind: "message", at: Date.now() }];
  leaf.modelTokens = { "Idle leaf model": { in: 23000, out: 1000, calls: 1 } };
  peer.modelTokens = { "Outside model": { in: 999000, out: 5000, calls: 1 } };
  api.build(snapshot(parent, root, child, leaf, peer));
  api.toggleSelect(api.nodes.get("s:peer"));
  api.setFocus(root.id);
  assert.equal(api.selected, null);
  assert.ok(api.nodes.has("s:root") && api.nodes.has("a:root:child-0") && api.nodes.has("b:root:server"));
  assert.ok(!api.nodes.has("s:parent") && !api.nodes.has("s:peer") && !api.nodes.has("k:Outside sink"));
  assert.ok(!api.nodes.has("s:child") && !api.nodes.has("s:leaf") && !api.nodes.has("a:root:child-1"));
  assert.equal(api.nodes.get("s:root").parentSessionID, null);
  assert.equal(elements.get("sAll").textContent, 1);
  assert.equal(elements.get("sBusy").textContent, 1);
  assert.equal(elements.get("sSub").textContent, 1);
  assert.equal(elements.get("sShell").textContent, 1);
  assert.equal(elements.get("sFlow").textContent, 1);
  assert.match(elements.get("feed").innerHTML, /Root work/);
  assert.doesNotMatch(elements.get("feed").innerHTML, /Unrelated work/);
  assert.match(elements.get("models").innerHTML, /Idle leaf model/);
  assert.doesNotMatch(elements.get("models").innerHTML, /Outside model/);
  assert.equal(elements.get("focusScope").hidden, false);
  elements.get("showIdle").checked = true;
  api.build(snapshot(parent, root, child, leaf, peer));
  assert.equal(elements.get("sAll").textContent, 3);
  assert.ok(api.nodes.has("s:child") && api.nodes.has("s:leaf") && api.nodes.has("a:root:child-1"));
  api.setFocus(null);
  assert.equal(elements.get("sAll").textContent, 5);
  assert.match(elements.get("feed").innerHTML, /Unrelated work/);
  assert.match(elements.get("models").innerHTML, /Outside model/);
  assert.equal(elements.get("focusScope").hidden, true);
});

test("focused idle roots stay visible, new descendants join, missing roots stay explicitly empty", () => {
  const { api, elements } = graph(1000, 500, true);
  const root = session("root"), child = session("child"), unrelated = session("unrelated");
  root.busy = false;
  api.build(snapshot(root, unrelated));
  api.setFocus(root.id);
  assert.ok(api.nodes.has("s:root"));
  assert.equal(api.nodes.get("s:root").s.contextOnly, false);
  root.childSessions = [child.id];
  api.build(snapshot(root, unrelated, child));
  assert.ok(api.nodes.has("s:child"));
  assert.equal(elements.get("sAll").textContent, 2);
  api.build(snapshot(unrelated, child));
  assert.equal(elements.get("sAll").textContent, 0);
  assert.equal(api.focusID, root.id);
  assert.match(elements.get("focusLabel").textContent, /Session no longer available: Parent root/);
  assert.match(elements.get("list").innerHTML, /Show all sessions/);
  assert.doesNotMatch(elements.get("list").innerHTML, /Parent unrelated/);
  assert.match(elements.get("models").innerHTML, /No model usage/);
  api.build(snapshot(root, child, unrelated));
  assert.ok(api.nodes.has("s:root") && api.nodes.has("s:child"));
  api.setFocus(null);
  assert.ok(api.nodes.has("s:unrelated"));
});

test("focus uses the same deterministic ownership as layout for cycles and duplicate claims", () => {
  const { api, elements } = graph(1000, 500, true);
  const a = session("a"), b = session("b"), c = session("c"), other = session("other");
  a.childSessions = [a.id, b.id, "missing", b.id];
  b.childSessions = [c.id];
  c.childSessions = [a.id];
  other.childSessions = [c.id];
  api.build(snapshot(other, c, b, a));
  api.setFocus(b.id);
  assert.equal(elements.get("sAll").textContent, 2);
  assert.ok(api.nodes.has("s:b") && api.nodes.has("s:c"));
  assert.ok(!api.nodes.has("s:a") && !api.nodes.has("s:other") && !api.nodes.has("s:missing"));
});

test("focus changes flush every panel immediately but later updates still defer while interacting", () => {
  const { api, elements, emit } = graph(1000, 500, true);
  const a = session("a"), b = session("b");
  a.flows = [{ sink: "Filesystem", tool: "view", desc: "A work", endAt: Date.now() }];
  b.modelTokens = { "B model": { in: 1000 } };
  api.build(snapshot(a, b));
  for (const id of ["list", "feed", "models"]) emit(id, "pointerenter");
  api.setFocus(a.id);
  assert.doesNotMatch(elements.get("list").innerHTML, /Parent b/);
  assert.doesNotMatch(elements.get("models").innerHTML, /B model/);
  a.name = "Renamed";
  a.flows[0].desc = "New description";
  a.modelTokens = { "New model": { in: 1000 } };
  api.build(snapshot(a, b));
  assert.doesNotMatch(elements.get("list").innerHTML, /Renamed/);
  assert.doesNotMatch(elements.get("feed").innerHTML, /New description/);
  assert.doesNotMatch(elements.get("models").innerHTML, /New model/);
  for (const id of ["list", "feed", "models"]) emit(id, "pointerleave");
  assert.match(elements.get("list").innerHTML, /Renamed/);
  assert.match(elements.get("feed").innerHTML, /New description/);
  assert.match(elements.get("models").innerHTML, /New model/);
});

test("context actions work on graph and sidebar with keyboard navigation and dismissal", () => {
  const { api, elements, document, emit } = graph(1000, 500, true);
  const root = session("root");
  root.pid = 42;
  api.build(snapshot(root));
  const n = api.nodes.get("s:root"), menu = elements.get("sessionMenu");
  let prevented = 0;
  const preventDefault = () => prevented++;
  emit("cv", "click", { offsetX: n.x, offsetY: n.y });
  assert.equal(api.selected, n);
  assert.equal(api.focusID, null);
  emit("cv", "contextmenu", { offsetX: n.x, offsetY: n.y, clientX: 999, clientY: 499, preventDefault });
  assert.equal(menu.hidden, false);
  assert.equal(menu.style.left, "732px");
  assert.equal(menu.style.top, "392px");
  assert.doesNotMatch(menu.innerHTML, /Show app/);
  let buttons = menu.querySelectorAll("button");
  assert.equal(document.activeElement, buttons[0]);
  menu.onkeydown({ key: "Escape", preventDefault });
  assert.equal(menu.hidden, true);
  assert.equal(document.activeElement.dataset.id, "s:root");
  const card = elements.get("list").querySelectorAll(".card")[0];
  card.onkeydown({ key: "F10", shiftKey: true, preventDefault });
  assert.equal(menu.hidden, false);
  buttons = menu.querySelectorAll("button");
  buttons[0].click();
  assert.equal(api.focusID, root.id);
  assert.equal(menu.hidden, true);
  elements.get("body").dataset = { jump: "" };
  card.oncontextmenu({ clientX: 20, clientY: 60, preventDefault });
  buttons = menu.querySelectorAll("button");
  assert.equal(buttons.length, 3);
  menu.onkeydown({ key: "End", preventDefault });
  assert.equal(document.activeElement.dataset.action, "jump");
  menu.onkeydown({ key: "ArrowDown", preventDefault });
  assert.equal(document.activeElement.dataset.action, "focus");
  menu.onkeydown({ key: "ArrowUp", preventDefault });
  assert.equal(document.activeElement.dataset.action, "jump");
  menu.onkeydown({ key: "Home", preventDefault });
  assert.equal(document.activeElement.dataset.action, "focus");
  menu.onkeydown({ key: "Tab", preventDefault });
  assert.equal(menu.hidden, true);
  api.openSessionMenu(root.id, -30, -30);
  assert.equal(menu.style.left, "8px");
  emit("document", "pointerdown", { target: menu.querySelectorAll("button")[0] });
  assert.equal(menu.hidden, false);
  emit("document", "pointerdown", { target: elements.get("stage") });
  assert.equal(menu.hidden, true);
  api.openSessionMenu(root.id, 0, 0);
  emit("window", "resize");
  assert.equal(menu.hidden, true);
  api.openSessionMenu(root.id, 0, 0);
  emit("document", "scroll", { target: elements.get("stage") });
  assert.equal(menu.hidden, true);
  api.openSessionMenu(root.id, 0, 0);
  menu.querySelectorAll("button")[1].click();
  assert.equal(api.focusID, null);
  api.openSessionMenu("missing", 0, 0);
  assert.equal(menu.hidden, true);
  assert.ok(prevented >= 8);
});

test("details offer accessible focus actions without app support and escape session metadata", () => {
  const { api, elements, document } = graph(1000, 500, true);
  const root = session('root"<>');
  api.build(snapshot(root));
  api.toggleSelect(api.nodes.get("s:" + root.id));
  assert.match(elements.get("list").innerHTML, /Focus this session and descendants/);
  assert.match(api.sessionActions(root, true), /data-session="root&quot;&lt;&gt;"/);
  assert.doesNotMatch(api.sessionActions(root, true), /Show app/);
  api.setFocus(root.id);
  assert.match(api.sessionActions(root), /Show all sessions/);
  elements.get("clearFocus").click();
  assert.equal(api.focusID, null);
  assert.equal(document.activeElement, elements.get("showIdle"));
});

test("Linux does not offer unsupported terminal or unsaved-window jumps", () => {
  const { api, elements } = graph(1000, 500, true);
  elements.get("body").dataset = { jump: "", jumpLinux: "" };
  const cli = { ...session("cli"), pid: 42 };
  const app = { ...session("app"), surface: "Copilot app" };
  const saved = { ...session("saved"), kind: "vscode-chat", surface: "Code chat", folders: ["/demo"] };
  const unsaved = { ...saved, id: "unsaved", folders: ["/demo/a", "/demo/b"] };
  api.build(snapshot(cli, app, saved, unsaved));
  assert.doesNotMatch(api.sessionActions(cli), /Show app/);
  assert.doesNotMatch(api.sessionActions(unsaved), /Show app/);
  assert.match(api.sessionActions(app), /Show app/);
  assert.match(api.sessionActions(saved), /Show app/);
  const agent = { ...session("agent"), surface: "VS Code agent", cwd: "/demo/a/sub", pid: 42 };
  const snap = snapshot(agent);
  snap.ides = [
    { id: "far", ide: "Visual Studio Code", folders: ["/demo"] },
    { id: "near", ide: "Visual Studio Code", folders: ["/demo/a", "/demo/b"] },
  ];
  api.build(snap);
  assert.doesNotMatch(api.sessionActions(agent), /Show app/);
  snap.ides[1].workspace = "/demo/saved.code-workspace";
  api.build(snap);
  assert.match(api.sessionActions(agent), /Show app/);
});

test("a connected native graph never labels old or invalid snapshots as live", () => {
  let now = 1800000000000, stream, timer;
  const conn = { textContent: "" }, live = { classList: { toggle() {} } };
  const sandbox = {
    document: { getElementById: id => id === "conn" ? conn : live },
    Date: { now: () => now },
    EventSource: class { constructor() { stream = this; this.readyState = 1; } },
    setInterval(fn) { timer = fn; },
  };
  vm.createContext(sandbox);
  const liveSource = html.split("function setConn(ok, text)")[1].split("// ---------- Demo mode")[0];
  vm.runInContext(`let snapshot; function build(data) { snapshot = data; }
    function setConn(ok, text)${liveSource}\nconnect();`, sandbox);
  stream.onopen();
  assert.match(conn.textContent, /waiting/);
  stream.onmessage({data: JSON.stringify({now, sessions: []})});
  assert.equal(conn.textContent, "live");
  now += 10001;
  timer();
  assert.match(conn.textContent, /stale/);
  stream.onmessage({data: JSON.stringify({now, sessions: []})});
  assert.equal(conn.textContent, "live");
  stream.onmessage({data: "invalid"});
  assert.match(conn.textContent, /invalid/);
  timer();
  assert.notEqual(conn.textContent, "live");
  stream.readyState = 0;
  stream.onerror();
  assert.match(conn.textContent, /disconnected/);
});

test("Show app reuses the encoded guarded POST and reports launch and network failures", async () => {
  const { api, elements, requests, setFetch } = graph(1000, 500, true);
  const btn = { dataset: { session: "session/with ?query" } };
  await api.showApp(btn);
  assert.equal(requests[0].url, "api/jump?session=session%2Fwith%20%3Fquery");
  assert.equal(requests[0].options.method, "POST");
  assert.equal(btn.disabled, false);
  assert.equal(elements.get("actionStatus").textContent, "");
  for (const status of [404, 403, 500]) {
    setFetch(async () => ({ ok: false, status }));
    await api.showApp(btn);
    assert.match(elements.get("actionStatus").textContent, /Could not show app/);
    assert.match(elements.get("actionStatus").textContent, status === 404 ? /not found/ : new RegExp("HTTP " + status));
    assert.equal(btn.disabled, false);
  }
  setFetch(async () => { throw new Error("Network unavailable"); });
  await api.showApp(btn);
  assert.match(elements.get("actionStatus").textContent, /Network unavailable/);
});
