import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const html = readFileSync(new URL("./ui.html", import.meta.url), "utf8");
const source = html.split("<script>")[1].split("// ---------- Panels ----------")[0];

function graph(width = 1000, height = 500) {
  const labels = [], paths = [];
  const ctx = new Proxy({
    measureText: (text) => ({ width: text.length * 8 }),
    getImageData: () => ({ data: [13, 17, 23, 255] }),
    fillText: (text, x, y) => labels.push({ text, x, y }),
    moveTo: (x, y) => paths.push(["move", x, y]),
    lineTo: (x, y) => paths.push(["line", x, y]),
  }, { get: (target, key) => key in target ? target[key] : () => {} });
  const elements = new Map();
  const element = (id) => {
    if (!elements.has(id)) elements.set(id, {
      style: { setProperty() {} }, clientWidth: width, clientHeight: height,
      innerHTML: "", checked: false, addEventListener() {}, getContext: () => ctx,
    });
    return elements.get(id);
  };
  const sandbox = {
    document: { getElementById: element, createElement: element, body: element("body") },
    window: { devicePixelRatio: 2 }, performance: { now: () => 1000 },
    matchMedia: () => ({ matches: false, addEventListener() {} }),
    getComputedStyle: () => ({ backgroundColor: "#0d1117", getPropertyValue: () => "" }),
    ResizeObserver: class { observe() {} },
    requestAnimationFrame() {}, renderSidebar() {},
  };
  vm.createContext(sandbox);
  vm.runInContext(source + `
    reduceMotion = true;
    globalThis.graph = { build, draw, pick, toggleSelect, nodeInfo, label, nodes,
      get links() { return links; }, get selected() { return selected; },
      get size() { return [W, H]; } };
  `, sandbox);
  // Sidebar rendering is covered by browser interaction checks.
  sandbox.sidebarArgs = null;
  sandbox.highlightCard = () => {};
  element("showIdle");
  return { api: sandbox.graph, elements, labels, paths };
}

function session(id, count = 0) {
  return {
    id, name: "Parent " + id, surface: "Copilot CLI", busy: true, waiting: false,
    runningTools: [], flows: [], peers: [],
    subagents: Array.from({ length: count }, (_, i) => ({
      id: "child-" + i, name: "Worker " + i, running: true,
      model: "Demo model", doing: "Inspecting fabricated test files",
    })),
  };
}
const snapshot = (...sessions) => ({ now: Date.now(), sessions, ides: [] });

test("children stay below and inside their parent group, including large batches", () => {
  const { api } = graph(420);
  api.build(snapshot(session("one", 40), session("two", 3)));
  assert.equal(api.size[0], 900);
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
