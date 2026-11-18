// The prelude of a plugin's runtime: it builds the mako object a plugin's
// activate is given, from the Host API's generated bindings, keeps the
// blocks and commands the plugin registers with their state, and is what
// the daemon calls into. Nothing here is a boundary: the plugin runs in
// the same realm and may change any of it, so the daemon checks every
// call of the Host API and every tree a render returns.
import { hostApi } from "@mako/host.js";

const bridge = globalThis.__makoCall;
const declared = JSON.parse(globalThis.__makoDeclared);
// What the plugin is not to lean on: the bridge, which the bindings hold,
// and timers, which would outlive the call that set them.
for (const name of ["__makoCall", "__makoDeclared", "setTimeout", "setInterval", "clearTimeout", "clearInterval", "std", "os"]) {
  try {
    delete globalThis[name];
  } catch {}
}

const host = hostApi(async (method, req) =>
  JSON.parse(bridge(method, JSON.stringify(req === undefined ? {} : req))));

const blocks = new Map();
const states = new Map();
const commands = new Map();
const reloads = new Set();
let plugin = null;

const clone = (v) => JSON.parse(JSON.stringify(v === undefined ? {} : v));

function need(id) {
  const b = blocks.get(id);
  if (!b) throw new Error(`the plugin registered no block ${id}`);
  return b;
}

const mako = Object.freeze({
  ...host,
  blocks: Object.freeze({
    register(id, def) {
      if (!declared.blocks.includes(id)) throw new Error(`block ${id} is not declared in plugin.toml`);
      if (blocks.has(id)) throw new Error(`block ${id} is registered already`);
      if (!def || typeof def.render !== "function") throw new Error(`block ${id} has no render`);
      blocks.set(id, def);
      states.set(id, clone(def.state));
    },
    state(id) {
      need(id);
      return states.get(id);
    },
    reload(id) {
      need(id);
      reloads.add(id);
    },
  }),
  commands: Object.freeze({
    ...host.commands,
    register(id, run) {
      if (!declared.commands.includes(id)) throw new Error(`command ${id} is not declared in plugin.toml`);
      if (typeof run !== "function") throw new Error(`command ${id} runs no function`);
      commands.set(id, run);
    },
  }),
});

// The UI vocabulary. A node is a plain description; its methods set what
// the options do, and JSON.stringify writes it as the daemon reads it.
class UiNode {
  constructor(kind, fields) {
    this.n = { kind, ...fields };
  }
  toJSON() {
    return this.n;
  }
  key(k) {
    this.n.key = String(k);
    return this;
  }
  tone(t) {
    this.n.tone = t;
    return this;
  }
  weight(w) {
    this.n.weight = w;
    return this;
  }
  size(s) {
    this.n.size = s;
    return this;
  }
  grow(g = 1) {
    this.n.grow = g;
    return this;
  }
  gap(px) {
    this.n.gap = px;
    return this;
  }
  icon(name) {
    this.n.icon = name;
    return this;
  }
  disabled(on = true) {
    this.n.disabled = !!on;
    return this;
  }
  onTap(name, ...args) {
    this.n.onTap = [name, ...args];
    return this;
  }
  onChange(name, ...args) {
    this.n.onChange = [name, ...args];
    return this;
  }
}

const options = ["key", "tone", "weight", "size", "grow", "gap", "icon", "disabled", "onTap", "onChange", "onSubmit", "placeholder"];

function node(kind, fields, opts) {
  const n = new UiNode(kind, fields);
  for (const [k, v] of Object.entries(opts || {})) {
    if (!options.includes(k)) throw new Error(`${k} is not an option of a node`);
    n.n[k] = k === "key" ? String(v) : v;
  }
  return n;
}

function children(c) {
  if (!Array.isArray(c)) throw new TypeError("a node's children are an array");
  return c;
}

const ui = Object.freeze({
  column: (c, o) => node("column", { children: children(c) }, o),
  row: (c, o) => node("row", { children: children(c) }, o),
  stack: (c, o) => node("stack", { children: children(c) }, o),
  text: (t, o) => node("text", { text: String(t) }, o),
  icon: (name, o) => node("icon", { icon: String(name) }, o),
  pill: (t, o) => node("pill", { text: String(t) }, o),
  chip: (t, o) => node("chip", { text: String(t) }, o),
  button: (t, o) => node("button", { text: String(t) }, o),
  list: (items, row, o) => node("list", { children: children(items).map((it, i) => row(it, i)) }, o),
  input: (v, o) => node("input", { value: String(v ?? "") }, o),
  toggle: (c, label, o) => node("toggle", { checked: !!c, text: String(label ?? "") }, o),
  select: (v, opts, o) => node("select", { value: String(v ?? ""), options: children(opts).map(String) }, o),
  markdown: (t, o) => node("markdown", { text: String(t) }, o),
  code: (t, language, o) => node("code", { text: String(t), language: language ?? "" }, o),
  progress: (v, o) => node("progress", { progress: Number(v) }, o),
  divider: (o) => node("divider", {}, o),
});

// What the daemon calls.
const R = {
  boot(p) {
    plugin = p;
    return R;
  },
  async activate() {
    if (!plugin || typeof plugin.activate !== "function") {
      throw new Error("the entry module's default export has no activate");
    }
    await plugin.activate(mako);
  },
  blocks: () => JSON.stringify([...blocks.keys()]),
  async load(id) {
    const b = need(id);
    if (typeof b.load === "function") await b.load(states.get(id));
  },
  render(id) {
    return JSON.stringify(need(id).render(states.get(id), ui));
  },
  async event(id, name, args) {
    const b = need(id);
    const on = b.on || {};
    const h = Object.prototype.hasOwnProperty.call(on, name) ? on[name] : undefined;
    if (typeof h !== "function") throw new Error(`block ${id} takes no message ${name}`);
    await h(states.get(id), ...JSON.parse(args));
  },
  async command(id) {
    const run = commands.get(id);
    if (!run) throw new Error(`the plugin registered nothing for command ${id}`);
    await run();
  },
  save: (id) => JSON.stringify(states.has(id) ? states.get(id) : null),
  restore(id, json) {
    const v = JSON.parse(json);
    if (blocks.has(id) && v !== null) states.set(id, v);
  },
  refresh: (id) => String(Number(need(id).refresh) || 0),
  takeReloads() {
    const out = [...reloads];
    reloads.clear();
    return JSON.stringify(out);
  },
};

export default R;
