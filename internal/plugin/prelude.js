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
// states maps blockId -> Map<scopeKey, stateObj>
const states = new Map();
// formValues maps blockId -> Map<scopeKey, valuesObj>
const formValues = new Map();
const blockContexts = new Map();
const commands = new Map();
const tools = new Map();
// reloads maps `${id}\0${scopeKey}` -> { id, scope }
const reloads = new Map();
// i18nCatalogs maps normalized BCP-47 locale tag (e.g. "ko-kr", "ko", "en") -> Map<string, string>
const i18nCatalogs = new Map();
let plugin = null;
let activeScope = "";
let activeCtx = null;

function normalizeLocaleTag(raw) {
  if (typeof raw !== "string") return "";
  return raw.trim().replace(/_/g, "-").toLowerCase();
}

function flattenCatalogEntries(entries, prefix, out) {
  if (!entries || typeof entries !== "object" || Array.isArray(entries)) return;
  for (const [k, v] of Object.entries(entries)) {
    const full = prefix ? `${prefix}.${k}` : k;
    if (v !== null && typeof v === "object" && !Array.isArray(v)) {
      flattenCatalogEntries(v, full, out);
    } else if (v !== undefined && v !== null) {
      out.set(full, String(v));
    }
  }
}

function registerI18nCatalog(catalog) {
  if (!catalog || typeof catalog !== "object" || Array.isArray(catalog)) {
    throw new TypeError("i18n catalog must be an object keyed by locale");
  }
  for (const [loc, entries] of Object.entries(catalog)) {
    const norm = normalizeLocaleTag(loc);
    if (!norm) continue;
    let map = i18nCatalogs.get(norm);
    if (!map) {
      map = new Map();
      i18nCatalogs.set(norm, map);
    }
    flattenCatalogEntries(entries, "", map);
  }
}

function translate(key, params, locale) {
  const k = String(key ?? "");
  const effective = (locale !== undefined && locale !== null && String(locale).trim() !== "")
    ? String(locale)
    : (activeCtx && activeCtx.locale) || "en";
  const norm = normalizeLocaleTag(effective) || "en";
  const chain = [norm];
  const dash = norm.indexOf("-");
  if (dash > 0) {
    const primary = norm.slice(0, dash);
    if (primary && !chain.includes(primary)) chain.push(primary);
  }
  if (!chain.includes("en")) chain.push("en");
  let template = k;
  for (const tag of chain) {
    const cat = i18nCatalogs.get(tag);
    if (cat && cat.has(k)) {
      template = cat.get(k);
      break;
    }
  }
  if (!params || typeof params !== "object") return template;
  return template.replace(/\{([A-Za-z0-9_.-]+)\}/g, (match, name) => (
    Object.prototype.hasOwnProperty.call(params, name) && params[name] !== undefined && params[name] !== null
      ? String(params[name])
      : match
  ));
}

const clone = (v) => JSON.parse(JSON.stringify(v === undefined ? {} : v));

function need(id) {
  const b = blocks.get(id);
  if (!b) throw new Error(`the plugin registered no block ${id}`);
  return b;
}

function resolveScopeArg(scopeKey) {
  return scopeKey === undefined ? activeScope : String(scopeKey || "");
}

function getState(id, scopeKey) {
  const b = need(id);
  const sk = scopeKey || "";
  let byScope = states.get(id);
  if (!byScope) {
    byScope = new Map();
    states.set(id, byScope);
  }
  if (!byScope.has(sk)) {
    byScope.set(sk, clone(b.state));
  }
  return byScope.get(sk);
}

const decisionPayload = (req) => (typeof req === "string" ? req : JSON.stringify(req === undefined ? {} : req));

async function decisionEvaluate(req, opts) {
  const res = await host.decision.evaluate({
    requestJson: decisionPayload(req),
    timeoutMs: Number(opts?.timeoutMs || 0),
  });
  const parsed = res.responseJson ? JSON.parse(res.responseJson) : {};
  if (res.provider && !parsed.provider) parsed.provider = res.provider;
  if (res.model && !parsed.model) parsed.model = res.model;
  parsed.elapsedMs = Number(res.elapsedMs || 0);
  return parsed;
}

const decisionApi = Object.freeze({
  status: () => host.decision.status({}),
  evaluate: decisionEvaluate,
  async tryEvaluate(req, opts) {
    try {
      const result = await decisionEvaluate(req, opts);
      return { ok: true, result };
    } catch (err) {
      const msg = err && err.message ? String(err.message) : String(err);
      return { ok: false, unavailable: /decision unavailable/i.test(msg), error: msg };
    }
  },
  submit: (req, opts) => host.decision.submit({
    requestJson: decisionPayload(req),
    timeoutMs: Number(opts?.timeoutMs || 0),
    tag: opts?.tag !== undefined ? String(opts.tag) : "",
  }),
  score: (state, instructions, levels, opts) => decisionEvaluate({ state, type: "score", instructions, levels }, opts),
  choice: (state, instructions, options, opts) => decisionEvaluate({ state, type: "choice", instructions, options }, opts),
  async yes(state, instructions, opts) {
    const res = await decisionEvaluate({ state, type: "noul", instructions }, opts);
    if (typeof res.yes === "boolean") return res.yes;
    return Number(res.probability || 0) >= 0.5;
  },
});

const agentsApi = Object.freeze({
  ...host.agents,
  async list() {
    const res = await host.agents.listAgents({});
    return Array.isArray(res.agents) ? res.agents : [];
  },
  ask(req) {
    const r = req || {};
    return host.agents.askAgent({
      agent: String(r.agent || ""),
      profile: String(r.profile || ""),
      project: String(r.project || ""),
      root: String(r.root || ""),
      prompt: String(r.prompt || ""),
      format: String(r.format || ""),
      jsonSchema: typeof r.jsonSchema === "string"
        ? r.jsonSchema
        : (r.jsonSchema ? JSON.stringify(r.jsonSchema) : ""),
      timeoutMs: Number(r.timeoutMs || 0),
      title: String(r.title || ""),
      tag: String(r.tag || ""),
    });
  },
  cancel(requestId) {
    return host.agents.cancelAsk(
      typeof requestId === "string" ? { requestId } : (requestId || {}),
    );
  },
});

function makeContext(ctxJson) {
  const raw = ctxJson ? JSON.parse(ctxJson) : {};
  const scopeKey = raw.scopeKey || "";
  const scope = raw.scope || (scopeKey ? scopeKey.split(":")[0] : "global");
  const locale = (typeof raw.locale === "string" && raw.locale.trim()) ? raw.locale.trim() : "en";
  const t = (key, params) => translate(key, params, locale);
  const scopedStorage = Object.freeze({
    getItem: (k) => host.storage.getItem(typeof k === "string" ? { key: k, scope: scopeKey } : { ...(k || {}), scope: scopeKey }),
    setItem: (k, v) => host.storage.setItem(typeof k === "string" ? { key: k, value: String(v ?? ""), scope: scopeKey } : { ...(k || {}), scope: scopeKey }),
    deleteItem: (k) => host.storage.deleteItem(typeof k === "string" ? { key: k, scope: scopeKey } : { ...(k || {}), scope: scopeKey }),
    listKeys: (p) => host.storage.listKeys(p === undefined || typeof p === "string" ? { prefix: p || "", scope: scopeKey } : { ...(p || {}), scope: scopeKey }),
  });
  const scopedSettings = Object.freeze({
    getSetting: (k) => host.settings.getSetting(typeof k === "string" ? { key: k, scope: scopeKey } : { ...(k || {}), scope: scopeKey }),
    listSettings: (req) => host.settings.listSettings({ ...(req || {}), scope: scopeKey }),
  });
  const encodeParams = (params) => Array.isArray(params) ? params.map((v) => JSON.stringify(v === undefined ? null : v)) : [];
  const scopedDb = Object.freeze({
    async query(sql, params) {
      const res = await host.db.query({ sql: String(sql || ""), paramsJson: encodeParams(params), scope: scopeKey });
      const cols = res.columns || [];
      const rows = (res.rowsJson || []).map((raw) => {
        const arr = JSON.parse(raw);
        const obj = {};
        for (let i = 0; i < cols.length; i++) obj[cols[i]] = arr[i];
        return obj;
      });
      return { columns: cols, rows, truncated: Boolean(res.truncated) };
    },
    exec: (sql, params) => host.db.exec({ sql: String(sql || ""), paramsJson: encodeParams(params), scope: scopeKey }),
    batch: (stmts) => host.db.batch({
      statements: (stmts || []).map((st) => ({ sql: String(st.sql || ""), paramsJson: encodeParams(st.params) })),
      scope: scopeKey,
    }),
    stats: () => host.db.stats({ scope: scopeKey }),
    syncEnable: (table) => host.db.syncEnable({ table: String(table || ""), scope: scopeKey }),
    syncPut: (table, id, doc) => host.db.syncPut({
      table: String(table || ""),
      id: String(id || ""),
      docJson: typeof doc === "string" ? doc : JSON.stringify(doc === undefined ? {} : doc),
      scope: scopeKey,
    }),
    syncDelete: (table, id) => host.db.syncDelete({ table: String(table || ""), id: String(id || ""), scope: scopeKey }),
  });
  const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  const bytesToBase64 = (bytes) => {
    let out = "";
    for (let i = 0; i < bytes.length; i += 3) {
      const b0 = bytes[i];
      const hasB1 = i + 1 < bytes.length;
      const hasB2 = i + 2 < bytes.length;
      const b1 = hasB1 ? bytes[i + 1] : 0;
      const b2 = hasB2 ? bytes[i + 2] : 0;
      const n = (b0 << 16) | (b1 << 8) | b2;
      out += base64Alphabet[(n >>> 18) & 63];
      out += base64Alphabet[(n >>> 12) & 63];
      out += hasB1 ? base64Alphabet[(n >>> 6) & 63] : "=";
      out += hasB2 ? base64Alphabet[n & 63] : "=";
    }
    return out;
  };
  const encodeDataBase64 = (value) => {
    if (typeof value === "string") return value;
    if (value instanceof ArrayBuffer) return bytesToBase64(new Uint8Array(value));
    if (ArrayBuffer.isView(value)) return bytesToBase64(new Uint8Array(value.buffer, value.byteOffset, value.byteLength));
    throw new TypeError("datafs.write data must be a base64 string, ArrayBuffer, or TypedArray");
  };
  const scopedDatafs = Object.freeze({
    read: (p, opts) => host.datafs.readData({
      path: String(p || ""),
      offset: opts?.offset || 0,
      length: opts?.length || 0,
      scope: scopeKey,
    }),
    async write(p, content, opts) {
      if (content instanceof ArrayBuffer || ArrayBuffer.isView(content)) {
        return host.datafs.writeData({
          path: String(p || ""),
          data: encodeDataBase64(content),
          makeDirs: opts?.makeDirs !== false,
          scope: scopeKey,
        });
      }
      if (content !== null && typeof content === "object") {
        return host.datafs.writeData({
          path: String(p || ""),
          text: content.text !== undefined ? String(content.text) : "",
          data: content.data !== undefined ? encodeDataBase64(content.data) : undefined,
          makeDirs: content.makeDirs !== undefined ? Boolean(content.makeDirs) : (opts?.makeDirs !== false),
          scope: scopeKey,
        });
      }
      return host.datafs.writeData({
        path: String(p || ""),
        text: String(content ?? ""),
        makeDirs: opts?.makeDirs !== false,
        scope: scopeKey,
      });
    },
    list: (p, opts) => host.datafs.listData({
      path: p === undefined ? "" : String(p || ""),
      recursive: Boolean(opts?.recursive),
      scope: scopeKey,
    }),
    stat: (p) => host.datafs.statData({ path: String(p || ""), scope: scopeKey }),
    mkdir: (p) => host.datafs.mkdirData({ path: String(p || ""), scope: scopeKey }),
    delete: (p, opts) => host.datafs.deleteData({
      path: String(p || ""),
      recursive: Boolean(opts?.recursive),
      scope: scopeKey,
    }),
    move: (fromPath, toPath, opts) => host.datafs.moveData({
      fromPath: String(fromPath || ""),
      toPath: String(toPath || ""),
      makeDirs: opts?.makeDirs !== false,
      scope: scopeKey,
    }),
    stats: () => host.datafs.dataStats({ scope: scopeKey }),
  });
  const scopedAgents = Object.freeze({
    list: () => agentsApi.list(),
    ask: (req) => {
      const r = req || {};
      return agentsApi.ask({
        ...r,
        project: r.project !== undefined && r.project !== "" ? r.project : (raw.projectId || ""),
        root: r.root !== undefined && r.root !== "" ? r.root : ((Array.isArray(raw.roots) && raw.roots[0]) || ""),
      });
    },
    cancel: (requestId) => agentsApi.cancel(requestId),
  });
  const node = raw.node || "";
  const folder = raw.folder || "";
  const view = Object.freeze({ standalone: Boolean(raw.view && raw.view.standalone) });
  const defaultTarget = () => (scopeKey
    ? { scope: scopeKey }
    : (node || folder ? { node, path: folder } : {}));
  const withTarget = (req) => {
    const r = req || {};
    return { ...r, target: r.target || defaultTarget() };
  };
  const scopedGit = Object.freeze({
    status: (req) => host.git.status(withTarget(req)),
    history: (req) => host.git.history(withTarget(req)),
    showCommit: (req) => host.git.showCommit(
      typeof req === "string" ? { target: defaultTarget(), hash: req } : withTarget(req),
    ),
    watch: (req) => host.git.watch(withTarget(req)),
    unwatch: (req) => host.git.unwatch(withTarget(req)),
    run: (req) => host.git.run(withTarget(req)),
  });
  const scopedForge = Object.freeze({
    branchStatus: (req) => host.forge.branchStatus(
      typeof req === "string" ? { target: defaultTarget(), branch: req } : withTarget(req),
    ),
  });
  const withFolder = (o) => {
    const r = { ...(o || {}) };
    if (r.node === undefined || r.node === "") r.node = node;
    if (r.folder === undefined || r.folder === "") r.folder = folder;
    return r;
  };
  const scopedUi = Object.freeze({
    openDiff: (o) => host.commands.runCommand({ id: "mako.openDiff", arg: JSON.stringify(withFolder(o)) }),
    openFile: (o) => {
      const r = typeof o === "string" ? { path: o } : { ...(o || {}) };
      if (r.node === undefined || r.node === "") r.node = node;
      return host.commands.runCommand({ id: "mako.openFile", arg: JSON.stringify(r) });
    },
    copy: (text) => host.commands.runCommand({ id: "mako.copyText", arg: String(text ?? "") }),
    openForge: (o) => host.commands.runCommand({ id: "mako.openForge", arg: JSON.stringify(withFolder(o)) }),
    createPullRequest: (o) => host.commands.runCommand({ id: "mako.createPullRequest", arg: JSON.stringify(withFolder(o)) }),
    toast: (msg) => host.commands.runCommand({ id: "mako.toast", arg: String(msg ?? "") }),
    openUrl: (url) => host.commands.runCommand({ id: "mako.openUrl", arg: String(url ?? "") }),
  });
  return Object.freeze({
    scope,
    scopeKey,
    spaceId: raw.spaceId || "",
    spaceName: raw.spaceName || "",
    projectId: raw.projectId || "",
    projectName: raw.projectName || "",
    sessionId: raw.sessionId || "",
    node,
    folder,
    view,
    roots: Object.freeze(Array.isArray(raw.roots) ? [...raw.roots] : []),
    locale,
    t,
    storage: scopedStorage,
    settings: scopedSettings,
    db: scopedDb,
    datafs: scopedDatafs,
    agents: scopedAgents,
    git: scopedGit,
    forge: scopedForge,
    ui: scopedUi,
  });
}

const defaultGlobalCtx = makeContext("");

const mako = Object.freeze({
  ...host,
  decision: decisionApi,
  agents: agentsApi,
  blocks: Object.freeze({
    register(id, def) {
      if (!declared.blocks.includes(id)) throw new Error(`block ${id} is not declared in plugin.toml`);
      if (blocks.has(id)) throw new Error(`block ${id} is registered already`);
      if (!def || typeof def.render !== "function") throw new Error(`block ${id} has no render`);
      blocks.set(id, def);
      const byScope = new Map();
      byScope.set("", clone(def.state));
      states.set(id, byScope);
    },
    state(id, scopeKey) {
      return getState(id, resolveScopeArg(scopeKey));
    },
    values(id, scopeKey) {
      need(id);
      const sk = resolveScopeArg(scopeKey);
      const byScope = formValues.get(id);
      return Object.freeze({ ...((byScope && byScope.get(sk)) || {}) });
    },
    reload(id, scopeKey) {
      need(id);
      const sk = resolveScopeArg(scopeKey);
      reloads.set(`${id}\0${sk}`, { id, scope: sk });
    },
    context(id) {
      need(id);
      return blockContexts.get(id) || activeCtx || defaultGlobalCtx;
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
  tools: Object.freeze({
    register(id, run) {
      if (!(declared.tools || []).includes(id)) throw new Error(`tool ${id} is not declared in plugin.toml`);
      if (typeof run !== "function") throw new Error(`tool ${id} runs no function`);
      tools.set(id, run);
    },
  }),
  i18n: Object.freeze({
    register: (catalog) => registerI18nCatalog(catalog),
    t: (key, params, locale) => translate(key, params, locale),
  }),
});

function normalizeCheck(v) {
  if (v === true) return "on";
  if (v === false) return "off";
  if (v === null || v === undefined) return "";
  return String(v);
}

function normalizeBadge(b) {
  if (typeof b === "string") return { text: b };
  const out = { text: String(b?.text ?? "") };
  if (b?.tone !== undefined && b.tone !== "") out.tone = String(b.tone);
  if (b?.variant !== undefined && b.variant !== "") out.variant = String(b.variant);
  if (b?.icon !== undefined && b.icon !== "") out.icon = String(b.icon);
  return out;
}

function normalizeBadges(list) {
  return children(list).map(normalizeBadge);
}

function normalizeRow(r) {
  const out = {
    id: String(r?.id ?? ""),
    parents: Array.isArray(r?.parents) ? r.parents.map(String) : [],
    text: String(r?.text ?? ""),
  };
  if (r?.detail !== undefined && r.detail !== "") out.detail = String(r.detail);
  if (r?.meta !== undefined && r.meta !== "") out.meta = String(r.meta);
  if (Array.isArray(r?.badges) && r.badges.length > 0) out.badges = normalizeBadges(r.badges);
  if (r?.tone !== undefined && r.tone !== "") out.tone = String(r.tone);
  if (r?.strong) out.strong = true;
  if (r?.icon !== undefined && r.icon !== "") out.icon = String(r.icon);
  return out;
}

function normalizeRows(list) {
  return children(list).map(normalizeRow);
}

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
  variant(v) {
    this.n.variant = String(v ?? "");
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
  columns(n) {
    this.n.columns = Number(n);
    return this;
  }
  ratio(r) {
    this.n.ratio = Number(r);
    return this;
  }
  fill(on = true) {
    this.n.fill = !!on;
    return this;
  }
  icon(name) {
    this.n.icon = name;
    return this;
  }
  checked(on = true) {
    this.n.checked = !!on;
    return this;
  }
  value(v) {
    this.n.value = String(v ?? "");
    return this;
  }
  disabled(on = true) {
    this.n.disabled = !!on;
    return this;
  }
  badges(list) {
    this.n.badges = normalizeBadges(list);
    return this;
  }
  check(state) {
    this.n.check = normalizeCheck(state);
    return this;
  }
  onCheck(name, ...args) {
    this.n.onCheck = [name, ...args];
    return this;
  }
  contextMenu(key, arg) {
    this.n.contextMenu = String(key ?? "");
    if (arg !== undefined) this.n.contextArg = String(arg ?? "");
    return this;
  }
  contextArg(arg) {
    this.n.contextArg = String(arg ?? "");
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
  onSubmit(name, ...args) {
    this.n.onSubmit = [name, ...args];
    return this;
  }
  onLink(name, ...args) {
    this.n.onLink = [name, ...args];
    return this;
  }
  drag(payload, group) {
    this.n.drag = String(payload ?? "");
    if (group !== undefined) this.n.dragGroup = String(group);
    return this;
  }
  onDrop(name, ...args) {
    this.n.onDrop = [name, ...args];
    return this;
  }
  dropAccept(...groups) {
    this.n.dropAccept = groups.map(String);
    return this;
  }
}

const options = [
  "key", "text", "tone", "variant", "weight", "size", "grow", "gap", "columns", "ratio", "fill",
  "icon", "checked", "value", "disabled", "onTap", "onChange", "onSubmit", "onLink",
  "drag", "dragGroup", "onDrop", "dropAccept", "direction", "maxHeight", "placeholder", "labels",
  "badges", "check", "onCheck", "contextMenu", "contextArg", "rows",
];

function node(kind, fields, opts) {
  const n = new UiNode(kind, fields);
  for (const [k, v] of Object.entries(opts || {})) {
    if (!options.includes(k)) throw new Error(`${k} is not an option of a node`);
    if (v === undefined) continue;
    if (k === "key" || k === "text" || k === "drag" || k === "dragGroup" || k === "direction" || k === "variant" || k === "value" || k === "contextMenu" || k === "contextArg" || k === "placeholder") {
      n.n[k] = String(v ?? "");
    } else if (k === "check") {
      n.n.check = normalizeCheck(v);
    } else if (k === "badges") {
      n.n.badges = normalizeBadges(v);
    } else if (k === "rows") {
      n.n.rows = normalizeRows(v);
    } else if (k === "labels" || k === "dropAccept") {
      n.n[k] = children(v).map(String);
    } else if (k === "maxHeight" || k === "columns" || k === "ratio") {
      n.n[k] = Number(v);
    } else if (k === "fill" || k === "checked" || k === "disabled") {
      n.n[k] = Boolean(v);
    } else {
      n.n[k] = v;
    }
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
  toolbar: (c, o) => node("row", { variant: "toolbar", children: children(c) }, o),
  stack: (c, o) => node("stack", { children: children(c) }, o),
  card: (c, o) => node("card", { children: children(c) }, o),
  scroll: (c, o) => node("scroll", { children: children(c) }, o),
  wrap: (c, o) => node("wrap", { children: children(c) }, o),
  grid: (a, b, c) => (Array.isArray(a)
    ? node("grid", { children: children(a) }, b)
    : node("grid", { columns: Number(a), children: children(b) }, c)),
  split: (a, b, c) => (Array.isArray(a)
    ? node("split", { children: children(a) }, b)
    : node("split", { children: [a, b] }, c)),
  collapsible: (title, c, o) => node("collapsible", { text: String(title ?? ""), children: children(c) }, o),
  breadcrumb: (segments, o) => node("breadcrumb", { options: children(segments).map(String) }, o),
  tree: (items, o) => node("tree", { children: children(items) }, o),
  treeItem: (label, childrenOrOpts, o) => (Array.isArray(childrenOrOpts)
    ? node("tree_item", { text: String(label ?? ""), children: childrenOrOpts }, o)
    : node("tree_item", { text: String(label ?? "") }, childrenOrOpts)),
  menu: (face, items, o) => {
    const base = (face !== null && typeof face === "object")
      ? {
          ...(face.text !== undefined ? { text: String(face.text) } : {}),
          ...(face.icon !== undefined ? { icon: String(face.icon) } : {}),
          children: children(items),
        }
      : { text: String(face ?? ""), children: children(items) };
    return node("menu", base, o);
  },
  contextMenu: (key, items, o) => node("menu", { key: String(key ?? ""), variant: "context", children: children(items) }, o),
  menuItem: (text, o) => node("menu_item", { text: String(text ?? "") }, o),
  checkbox: (state, label, o) => {
    const c = state === undefined || state === null || state === "" ? "off" : normalizeCheck(state);
    return node("checkbox", { check: c, text: String(label ?? "") }, o);
  },
  dialog: (title, items, o) => node("dialog", { text: String(title ?? ""), children: children(items) }, o),
  dag: (rows, o) => node("dag", { rows: normalizeRows(rows) }, o),
  text: (t, o) => node("text", { text: String(t) }, o),
  icon: (name, o) => node("icon", { icon: String(name) }, o),
  pill: (t, o) => node("pill", { text: String(t) }, o),
  chip: (t, o) => node("chip", { text: String(t) }, o),
  button: (t, o) => node("button", { text: String(t ?? "") }, o),
  list: (items, row, o) => node("list", { children: children(items).map((it, i) => row(it, i)) }, o),
  input: (v, o) => node("input", { value: String(v ?? "") }, o),
  textarea: (v, o) => node("textarea", { value: String(v ?? "") }, o),
  toggle: (c, label, o) => node("toggle", { checked: !!c, text: String(label ?? "") }, o),
  select: (v, opts, o) => node("select", { value: String(v ?? ""), options: children(opts).map(String) }, o),
  tabs: (v, opts, o) => node("tabs", { value: String(v ?? ""), options: children(opts).map(String) }, o),
  table: (headers, rows, o) => node("table", { labels: children(headers).map(String), children: children(rows) }, o),
  markdown: (t, o) => node("markdown", { text: String(t) }, o),
  code: (t, language, o) => node("code", { text: String(t), language: language ?? "" }, o),
  progress: (v, o) => (v !== null && typeof v === "object"
    ? node("progress", { progress: 0 }, v)
    : node("progress", { progress: v === undefined ? 0 : Number(v) }, o)),
  divider: (o) => node("divider", {}, o),
  // A chart: "line", "bar", "sparkline", "donut" or "pie", of series
  // [{ name, values, tone }], its points/slices named by opts.labels and
  // itself by opts.caption, which is the node's text.
  chart: (type, series, o) => {
    const { caption, ...rest } = o || {};
    return node("chart", {
      chart: String(type),
      text: String(caption ?? ""),
      series: children(series).map((s) => ({ name: String(s.name ?? ""), tone: s.tone ?? "", values: children(s.values ?? []).map(Number) })),
    }, rest);
  },
  // A declarative interactive graph canvas of nodes [{ id, label, group, tone,
  // subtitle, weight }] and edges [{ from, to, label, tone, dashed }].
  graph: (nodes, edges, o) => {
    const { caption, ...rest } = o || {};
    return node("graph", {
      text: String(caption ?? ""),
      graphNodes: children(nodes).map((gn) => {
        const out = {
          id: String(gn.id ?? ""),
          label: String(gn.label ?? ""),
          group: String(gn.group ?? ""),
          tone: gn.tone ?? "",
          subtitle: String(gn.subtitle ?? ""),
        };
        if (gn.weight !== undefined) out.weight = Number(gn.weight);
        return out;
      }),
      graphEdges: children(edges).map((ge) => ({
        from: String(ge.from ?? ""),
        to: String(ge.to ?? ""),
        label: String(ge.label ?? ""),
        tone: ge.tone ?? "",
        dashed: Boolean(ge.dashed),
      })),
    }, rest);
  },
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
  async load(id, scopeKey, ctxJson) {
    const b = need(id);
    const sk = scopeKey || "";
    const ctx = makeContext(ctxJson);
    const prevScope = activeScope;
    const prevCtx = activeCtx;
    const prevBlockCtx = blockContexts.get(id);
    activeScope = sk;
    activeCtx = ctx;
    blockContexts.set(id, ctx);
    try {
      if (typeof b.load === "function") await b.load(getState(id, sk), ctx);
    } finally {
      activeScope = prevScope;
      activeCtx = prevCtx;
      if (prevBlockCtx === undefined) blockContexts.delete(id);
      else blockContexts.set(id, prevBlockCtx);
    }
  },
  render(id, scopeKey, ctxJson) {
    const sk = scopeKey || "";
    const ctx = makeContext(ctxJson);
    const prevScope = activeScope;
    const prevCtx = activeCtx;
    const prevBlockCtx = blockContexts.get(id);
    activeScope = sk;
    activeCtx = ctx;
    blockContexts.set(id, ctx);
    try {
      return JSON.stringify(need(id).render(getState(id, sk), ui, ctx));
    } finally {
      activeScope = prevScope;
      activeCtx = prevCtx;
      if (prevBlockCtx === undefined) blockContexts.delete(id);
      else blockContexts.set(id, prevBlockCtx);
    }
  },
  async event(id, scopeKey, ctxJson, name, args, valsJson) {
    const b = need(id);
    const sk = scopeKey || "";
    const ctx = makeContext(ctxJson);
    const on = b.on || {};
    const h = Object.prototype.hasOwnProperty.call(on, name) ? on[name] : undefined;
    if (typeof h !== "function") throw new Error(`block ${id} takes no message ${name}`);
    const parsedVals = valsJson ? JSON.parse(valsJson) : {};
    let byScope = formValues.get(id);
    if (!byScope) {
      byScope = new Map();
      formValues.set(id, byScope);
    }
    byScope.set(sk, parsedVals);
    const prevScope = activeScope;
    const prevCtx = activeCtx;
    const prevBlockCtx = blockContexts.get(id);
    activeScope = sk;
    activeCtx = ctx;
    blockContexts.set(id, ctx);
    try {
      await h(getState(id, sk), ...JSON.parse(args));
    } finally {
      byScope.delete(sk);
      activeScope = prevScope;
      activeCtx = prevCtx;
      if (prevBlockCtx === undefined) blockContexts.delete(id);
      else blockContexts.set(id, prevBlockCtx);
    }
  },
  async command(id, ctxJson) {
    const run = commands.get(id);
    if (!run) throw new Error(`the plugin registered nothing for command ${id}`);
    const ctx = makeContext(ctxJson);
    const prevScope = activeScope;
    const prevCtx = activeCtx;
    activeScope = ctx.scopeKey;
    activeCtx = ctx;
    try {
      await run(ctx);
    } finally {
      activeScope = prevScope;
      activeCtx = prevCtx;
    }
  },
  async callTool(id, argsJson, ctxJson) {
    const run = tools.get(id);
    if (!run) throw new Error(`the plugin registered nothing for tool ${id}`);
    const ctx = makeContext(ctxJson);
    const prevScope = activeScope;
    const prevCtx = activeCtx;
    activeScope = ctx.scopeKey;
    activeCtx = ctx;
    try {
      const res = await run(argsJson ? JSON.parse(argsJson) : {}, ctx);
      return JSON.stringify(res === undefined ? null : res);
    } finally {
      activeScope = prevScope;
      activeCtx = prevCtx;
    }
  },
  // An event a plugin subscribed to: the handler of its kind in the
  // default export's on, if it has one.
  async sessionEvent(json) {
    const ev = JSON.parse(json);
    const on = (plugin && plugin.on) || {};
    const h = Object.prototype.hasOwnProperty.call(on, ev.kind) ? on[ev.kind] : undefined;
    if (typeof h === "function") await h.call(plugin, ev);
  },
  async dbSyncEvent(json) {
    const ev = JSON.parse(json);
    const on = (plugin && plugin.on) || {};
    const h = Object.prototype.hasOwnProperty.call(on, "dbSync") ? on.dbSync : undefined;
    if (typeof h === "function") await h.call(plugin, ev);
  },
  async gitDone(json) {
    const ev = JSON.parse(json);
    const on = (plugin && plugin.on) || {};
    const h = Object.prototype.hasOwnProperty.call(on, "gitDone") ? on.gitDone : undefined;
    if (typeof h !== "function") return;
    const prevScope = activeScope;
    activeScope = (ev && typeof ev.scopeKey === "string") ? ev.scopeKey : "";
    try {
      await h.call(plugin, ev);
    } finally {
      activeScope = prevScope;
    }
  },
  save(id, scopeKey) {
    const sk = scopeKey || "";
    const byScope = states.get(id);
    return JSON.stringify(byScope && byScope.has(sk) ? byScope.get(sk) : null);
  },
  restore(id, scopeKey, json) {
    const sk = scopeKey || "";
    const v = JSON.parse(json);
    if (blocks.has(id) && v !== null) {
      let byScope = states.get(id);
      if (!byScope) {
        byScope = new Map();
        states.set(id, byScope);
      }
      byScope.set(sk, v);
    }
  },
  evict(id, scopeKey) {
    const sk = scopeKey || "";
    const byScope = states.get(id);
    if (byScope) byScope.delete(sk);
    const vals = formValues.get(id);
    if (vals) vals.delete(sk);
  },
  refresh: (id) => String(Number(need(id).refresh) || 0),
  takeReloads() {
    const out = [...reloads.values()];
    reloads.clear();
    return JSON.stringify(out);
  },
};

export default R;
