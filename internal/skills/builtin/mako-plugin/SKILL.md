---
name: mako-plugin
description: >-
  Build, update, and verify Mako plugins (Revision 46) using sandboxed QuickJS-on-Wasm,
  scoped storage/SQLite/datafs, standalone panel chrome, folder scope, rich UI components
  (toolbar, menu/contextMenu, checkbox, dialog, dag, tree, graph, charts, wrap/grid/split),
  Git/Forge Host APIs, window intents, built-in i18n, and CLI-callable mako-plugin agent tools.
---

# Mako Plugin Authoring Skill (Revision 46)

Use this skill whenever the user asks to create, extend, fix, or package a Mako
plugin (UI blocks, standalone panels, commands, CLI agent tools, Git/Forge integrations,
scoped data, or P2P sync).

## 1. Quick Start & Plugin Location

1. **Locate the plugins directory**:
   - Call `list_blocks` on the `mako` MCP server (it returns `plugin_dir`), or inspect `~/.config/mako/plugins/` (Linux) / `~/Library/Application Support/mako/plugins/` (macOS).
   - Or scaffold with `mako plugins init <id> [dir]`.
2. **Folder layout**:
   ```text
   <pluginsDir>/<id>/
   ├── plugin.toml      # Manifest: metadata, permissions, blocks, commands, tools, settings
   ├── main.js          # ES module default export: { activate(mako) { ... }, on?: { ... } }
   └── lib/             # Optional ES modules imported via "./lib/..."
   ```
   - `id`: lowercase letters, digits, hyphens (`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`), must NOT start with `mako` (`mako-` and `publisher = "mako"` are reserved for bundled first-party plugins).
3. **Activation & testing loop**:
   - The daemon watches `<pluginsDir>` automatically.
   - New or changed permissions start in `waiting` state until the user grants them (Settings ▸ Plugins or `mako plugins grant <id>`).
   - Place a block into the window layout via `patch_layout` (e.g. insert `"plugin:<id>/<blockId>"` into `"details"`) or Settings ▸ Plugins.
   - Reload after edits via the panel header's `⋯` menu (*Reload block*) or `mako plugins reload <id>`.
   - For bundled first-party plugins in `internal/plugin/builtin`, set `MAKO_DEV_BUNDLED_PLUGINS=$PWD/internal/plugin/builtin` to hot-reload edits from disk.

## 2. `plugin.toml` Manifest Reference

```toml
id = "my-plugin"
version = "0.1.0"
publisher = "local"
api = 1
entry = "main.js"
permissions = ["storage"]   # request only what you need

[[contributes.block]]
id = "main"
title = "My Plugin"
slots = ["right", "bottom"]
scope = "project"           # "", "global", "space", "project", "session", or "folder" (Rev 46)
chrome = "auto"             # "auto" (default) or "toolbar" (lifted ui.toolbar replaces solo dock title, Rev 46)
# hidden = true             # bundled first-party plugins only (omits from block catalogs)

[[contributes.command]]
id = "refresh"
title = "Refresh My Plugin"

[[contributes.tool]]
id = "list_items"
description = "List items in the active project"
scope = "project"           # "", "global", "space", "project", or "session" (NOT "folder")
[contributes.tool.params]   # TOML table (NOT [[...]] array!)
filter = { type = "string", description = "Optional filter" }
limit = { type = "number", description = "Max items" }
include_done = { type = "boolean", required = false }

[contributes.settings]
default_view = { type = "select", label = "Default view", options = ["list", "tree"], default = "list", scope = "project" }
api_token = { type = "secret", label = "API Token" }
```

### Permissions
- `storage` — per-scope KV store (`ctx.storage`: `getItem`, `setItem`, `deleteItem`, `listKeys`, 1 MiB)
- `db` — per-scope sandboxed SQLite (`ctx.db`: `query`, `exec`, `batch`, `stats`, 32 MiB total / 16 MiB per DB)
- `db:sync` — row-level HLC CRDT P2P sync across paired Mako nodes (`syncEnable`, `syncPut`, `syncDelete`)
- `fs:data` — sandboxed per-scope files/folders under `plugin-data/<id>/files/<scope>/` (`ctx.datafs`: `read`, `write`, `list`, `stat`, `mkdir`, `delete`, `move`, `stats`, 64 MiB)
- `sessions:read` — `mako.sessions.listSessions`, `getSession`, `readTranscript`, `subscribe`, `unsubscribe`
- `sessions:write` — `mako.messages.sendMessage`, `startAgent` (user-approved), `stopAgent`
- `fs:project`, `fs:project:write` — `mako.files.listProjects`, `readFile`, `listDir`, `writeFile`
- `net:<host>` — HTTPS fetch to that exact host only (`mako.http.fetch`)
- `secrets` — OS keychain secrets (`mako.secrets`)
- `layout:write` — propose window layout patches (`mako.layout.patchLayout`)
- `decision` — ask this node's configured decision model typed questions (`mako.decision`: `status`, `evaluate`, `tryEvaluate`, `submit`, `score`, `choice`, `yes`; events `on.decisionAnswered` / `on.decisionFailed`)
- `agents:ask` — list and ask one-shot questions of installed, `allow_template`-enabled agents without YOLO (`mako.agents`: `list`, `ask`, `cancel`; `ctx.agents.ask`; events `on.agentAskStarted` / `on.agentAnswered` / `on.agentAskFailed`, user-approved per agent/profile pair)
- `git:read` — read repository status/history/commits and watch work trees across the mesh (`mako.git` / `ctx.git`: `status`, `history`, `showCommit`, `watch`, `unwatch`; 120 reads/min, 16 watches; Revision 46)
- `git:write` — run gesture-gated asynchronous git mutations (`mako.git.run` / `ctx.git.run`, 30/min, 2 min timeout; outcomes arrive at default export's `on.gitDone({ job, scopeKey, op, ok, output, error })`; Revision 46)
- `forge:read` — read pull request and CI status for a branch (`mako.forge.branchStatus` / `ctx.forge.branchStatus`, 30/min; Revision 46)

**Important**: Tool parameter types MUST be `"string"`, `"number"`, or `"boolean"` (never `"integer"` or `"array"`).

## 3. Scopes, Context (`ctx`), Standalone Panels, and Built-in i18n

Every scoped block `load(s, ctx)`, `render(s, ui, ctx)`, command `(ctx)`, and tool `(args, ctx)` receives a frozen `ScopeContext`:
- `ctx.scope` (`"global" | "space" | "project" | "session" | "folder"`), `ctx.scopeKey` (e.g. `"project:<id>"`, `"folder:<node>/<base64url-nopad(path)>"`, or `""`)
- `ctx.spaceId`, `ctx.spaceName`, `ctx.projectId`, `ctx.projectName`, `ctx.roots` (array of project folder paths), `ctx.sessionId`
- `ctx.node`, `ctx.folder` — resolved node token (`"local"` or peer node ID) and absolute folder path for `folder` scope (Revision 46)
- `ctx.view` — `{ standalone: boolean }` (Revision 46): `true` when every active viewer displays the block solo rather than tabbed (logical AND across viewers). Pair `ctx.view.standalone` (what to show) with `ui.split(..., { direction: "auto" })` (how to arrange panes at ≥ 640 px vs narrow).
- `ctx.storage`, `ctx.settings`, `ctx.db`, `ctx.datafs`, `ctx.agents` — automatically bound to `ctx.scopeKey`!
- `ctx.git` (`status`, `history`, `showCommit`, `watch`, `unwatch`, `run`) and `ctx.forge` (`branchStatus`) — auto-fill `target` from `ctx.scopeKey` (or `{ node, path }`) (Revision 46). Call `ctx.git.watch()` in `load()` (idempotent) to auto-reload the instance on repo changes without writing a watcher loop.
- `ctx.ui` — gesture-gated window intents (Revision 46; auto-fills `node`/`folder` where applicable):
  - `ctx.ui.openDiff({ path, repo?, from?, status?, added?, removed?, binary?, commit?, base?, preview? })`
  - `ctx.ui.openFile({ path, line?, node? })` (or `ctx.ui.openFile(path)`)
  - `ctx.ui.copy(text)` (UTF-8 ≤ 64 KiB)
  - `ctx.ui.openForge({ kind: "pr"|"runs", number })`
  - `ctx.ui.createPullRequest()`
  - `ctx.ui.toast(msg)` / `ctx.ui.openUrl(url)`
- **Panel chrome**: When the root's first child is `ui.toolbar([...])` (`row` with `variant: "toolbar"`), the window lifts it into the dock header (solo) or tools row (tabbed) instead of drawing a double header. Declare `chrome = "toolbar"` on `[[contributes.block]]` to let that toolbar replace the solo dock title.
- **Async Git jobs (`on.gitDone`)**: Register `on: { gitDone(ev) { ... } }` on the plugin's default export to handle `{ job, scopeKey, op, ok, output, error }` from `ctx.git.run(...)` (`activeScope` is set to `ev.scopeKey` during the callback, so `mako.blocks.state("panel")` and `mako.blocks.reload("panel")` target the right folder).
- **i18n**:
  - Register translations once in `activate(mako)`:
    ```js
    mako.i18n.register({
      en: { title: "Items ({count})", tabs: { browse: "Browse", tree: "Tree" } },
      ko: { title: "항목 ({count})", tabs: { browse: "탐색", tree: "트리" } },
    });
    ```
  - Use `ctx.locale` (normalized BCP-47 tag from Desktop GUI or Android) and `ctx.t("tabs.browse")` / `ctx.t("title", { count: 3 })` in `render`, `load`, commands, and tools (inside `on` handlers, use `mako.blocks.context("main").t(...)` or `mako.i18n.t(...)`).
  - Fallback chain: exact tag (`ko-kr`) -> primary subtag (`ko`) -> `en` -> key.
  - Keep canonical data/frontmatter values locale-independent; localize only display labels.

## 4. Rich UI Component Catalog (Revision 46)

All trees are validated strictly by `internal/uiblock`. Raw hex colors are forbidden; use tone tokens: `""`, `"muted"`, `"faint"`, `"accent"`, `"success"`, `"warning"`, `"danger"`.

### Layout & Containers
- `ui.column(children, { gap, fill, ... })` / `ui.row(children, { gap, fill, variant, ... })` / `ui.stack(children)`
  - **`fill: true`** (root `column`/`row`/`split` ONLY): skips the outer frame scroll so you can pin a sticky top toolbar and scroll an inner `ui.scroll([...], { grow: 1 })`!
- `ui.toolbar(children, opts)` — shorthand for `ui.row(children, { variant: "toolbar", ... })` (Revision 46). Lifted into the window's chrome when it is the root's first child; drawn inline elsewhere.
- `ui.wrap(children, { gap })` — flex-wrap container (use for tags, chips, and link pills so they never clip horizontally).
- `ui.grid(columns, children, { gap })` — 1..6 equal columns per row (great for KPI stat cards).
- `ui.split(first, second, { direction: "horizontal"|"vertical"|"auto", ratio: 0.35 })` — resizable 2-pane split (`ratio` 0.1..0.9; `direction: "auto"` switches between horizontal at ≥ 640 px and vertical below, Revision 46).
- `ui.card(children, { gap, tone, variant, onTap, drag, dragGroup, onDrop, dropAccept })` — `variant`: `"" | "primary" | "secondary" | "outline" | "ghost" | "danger" | "soft"`.
- `ui.scroll(children, { direction: "vertical"|"horizontal", maxHeight, grow, gap, variant })` — `variant: "follow"` pins scroll to the end (Revision 45).
- `ui.list(items, (item, i) => node, { gap })` / `ui.table(headers, rowNodes)` (every table child must be `ui.row`).

### Navigation, Menus, Dialogs & Disclosure
- `ui.tabs(activeValue, options, { key, onChange })`
- `ui.breadcrumb(segments, { key, onChange })` — clicking an ancestor fires `onChange` with its 0-based index string (`"0"`, `"1"`, ...).
- `ui.collapsible(title, children, { key, icon, tone, checked, onChange })` — `checked` = initial open state; `onChange` receives boolean.
- `ui.tree(items, { key, gap })` + `ui.treeItem(label, children?, { key, value, icon, tone, variant, checked, check, onCheck, badges, contextMenu, contextArg, onTap, onChange, drag, dragGroup, onDrop, dropAccept })` — native tree with vertical indent guides, expand/collapse chevrons (`onChange` receives boolean), tap, `variant: "badge"` (tone on icon only + badge pill for `value`, Revision 45), tri-state checkbox (`check: "on"|"off"|"mixed"` + `onCheck`, Revision 46), trailing `badges`, `contextMenu`/`contextArg`, and drag-and-drop!
- `ui.menu(face, items, { key, variant })` + `ui.menuItem(text, { key, icon, tone, disabled, checked, onTap })` (Revision 46):
  - `face` is a label string or `{ text, icon }`; `variant`: `"" | "ghost" | "outline"`. Children must be `menu_item`, `divider`, or nested `menu` (max depth 2).
- `ui.contextMenu(key, items)` (Revision 46):
  - Declares a reusable non-rendered context menu (`menu` with `variant: "context"`, unique `key` across the tree). Reference it from any node via `{ contextMenu: key, contextArg: arg }` or `.contextMenu(key, arg)`; when an item is tapped, `contextArg` (or a `dag` row's `id`) is appended as the last argument to `onTap`.
- `ui.dialog(title, children, { key, variant, onChange })` (Revision 46):
  - Declarative modal dialog (`variant: "danger"` for destructive confirms; at most one per tree). `onChange` fires with `"dismiss"` on Escape or close; form values ride with button taps.

### Controls, Text & Markdown
- `ui.text(str, { size: "small"|""|"large", weight: ""|"medium"|"semibold", tone, grow, badges, onTap })`
- `ui.icon(name, { size, tone })` — use bundled Lucide names only (e.g. `book-open`, `folder`, `folder-open`, `folder-plus`, `file-text`, `search`, `plus`, `x`, `check`, `circle-check`, `shield-alert`, `network`, `tag`, `layers`, `sparkles`, `refresh-cw`, `arrow-left`, `chevron-right`, `chevron-down`, `git-branch`, `external-link`). Avoid unbundled names like `alert-triangle`.
- `ui.pill(text, { tone, icon, variant })` / `ui.chip(text, { tone, icon, variant, badges, onTap })` — `variant`: `"soft" | "solid" | "outline"`, plus optional leading `icon` (Revision 45).
- `ui.button(text, { key, icon, size: "small"|""|"large", variant: "primary"|"secondary"|"outline"|"ghost"|"danger", tone, badges, disabled, onTap })` — `text: ""` with `icon` renders a compact icon-only button!
- `ui.checkbox(state, label, { key, disabled, onChange })` — tri-state checkbox (`"on" | "off" | "mixed"` or boolean; `onChange` fires `"on"` or `"off"`; Revision 46).
- `ui.input(val, { key, placeholder, grow, onChange, onSubmit })`
- `ui.textarea(val, { key, placeholder, size: "small"|""|"large", onChange, onSubmit })` (`size: "large"` gives 14 lines!)
- `ui.toggle(checked, label, { key, onChange })` / `ui.select(val, options, { key, onChange })`
- `ui.progress(0..1)` or `ui.progress(0, { variant: "spinner", text })` (Revision 45) / `ui.divider()` / `ui.code(text, lang)`
- `ui.markdown(md, { key, onLink })`:
  - Renders Markdown + local pure-Go Mermaid diagrams (zero network).
  - `onLink: ["handler"]` intercepts non-`http(s)://` links (e.g. `[Target](myplugin:path)`) and passes the target string to your handler, while `http(s)://` links open externally.
- **Trailing badges & fluent node methods (Revision 46)**: any node accepts `badges` (up to 8 `{ text, tone?, variant?, icon? }`, `variant` in `"soft" | "solid" | "outline" | "mono"`, text ≤ 32 runes), plus fluent methods `.badges([...])`, `.check(state)`, `.onCheck(name, ...args)`, `.contextMenu(key, arg?)`.

### Visualization
- `ui.chart(type, series, { key, caption, labels })`:
  - `type`: `"line"`, `"bar"`, `"sparkline"`, `"donut"`, `"pie"` (`donut`/`pie` added in Revision 44).
  - Each series: `{ name, tone, values: [...] }`. For `"donut"` / `"pie"`: 1 series, 1..16 non-negative finite values matching `labels.length` (labels <= 32 UTF-8 bytes!).
- `ui.graph(nodes, edges, { key, caption, value, maxHeight, onChange })` (Revision 44):
  - Interactive force-directed canvas (pan/zoom + click hit-test firing `onChange` with `node.id`!).
  - `nodes`: `[{ id, label, group?, tone?, subtitle?, weight? }]` (<= 120 unique IDs, positive finite weight).
  - `edges`: `[{ from, to, label?, tone?, dashed? }]` (<= 400 edges; `from`/`to` must exist in `nodes`).
  - `maxHeight`: 120..1200 px.
- `ui.dag(rows, { key, value, contextMenu, maxHeight, onChange })` (Revision 46):
  - Virtualized commit/DAG graph with topological lanes and Bezier merges (up to 1 000 rows, keeping trees well under the 2 000-node limit!).
  - `rows`: `[{ id, parents, text, detail?, meta?, badges?, tone?, strong?, icon? }]` (unique `id` ≤ 64 chars, ≤ 8 parents, `text`/`detail` ≤ 320 chars, `meta` ≤ 128 chars, ≤ 6 badges per row).
  - `value`: selected row `id`; `onChange` fires with clicked row `id`; `contextMenu` opens with `row.id` as the context argument; `maxHeight` up to 4 000 px.

## 5. Agent CLI Tools (`mako-plugin`)

Contributed tools (`[[contributes.tool]]` + `mako.tools.register(id, async (args, ctx) => result)`) are exposed to AI agents via the `mako-plugin` CLI in every session's `PATH` (NOT dumped into MCP schemas):
```bash
mako-plugin list
mako-plugin help <plugin-id> [tool-id]
mako-plugin call <plugin-id> <tool-id> --key value --body -
```
- Scope isolation is enforced server-side from the caller's session token; tools whose scope is unavailable in the session (e.g. `project` tool outside a registered project) are automatically hidden and rejected. (`folder` scope is not supported on tools.)
- After mutating data in a tool handler, call `mako.blocks.reload("<blockId>", ctx.scopeKey)` so open UI blocks refresh immediately.

## 6. Common Pitfalls & Validation Rules

- **Keys**: must NOT contain `/` or `#`, and must be <= 128 bytes. Always sanitize paths with a `safeKey(prefix, raw)` helper.
- **Form values**: inside `on` submit handlers (including dialog confirm buttons), read live values via `mako.blocks.values("<blockId>")` (keyed by node key) because users may click Save without blurring an input first.
- **Context menus**: declare `ui.contextMenu(key, items)` once in the tree (unique `key`, never nested inside another menu) and reference it by `key` from rows/items instead of repeating menus per row; unknown `contextMenu` references are rejected.
- **Dialogs & DAGs**: at most one `ui.dialog` per tree; use `ui.dag` (data-driven rows) instead of building hundreds of row nodes to stay within the 2 000-node / 512 KiB tree JSON limits.
- **Gesture-only calls**: `ctx.git.run(...)` and `ctx.ui.*` (except `toast`) only succeed inside user-triggered event or command handlers, never inside `load()` or `render()`.
- **Chart/Graph byte limits**: truncate labels by UTF-8 byte length (`<= 32` bytes for chart labels; `<= 32` runes for badges), not just JS string `.length` (Korean characters are 3 bytes each in UTF-8!).
- **Root `fill: true`**: always pair `fill: true` with an inner `ui.scroll([...], { grow: 1 })` or `ui.split(..., { grow: 1 })` so tall content scrolls cleanly below your sticky header.
- **No external images**: `ui.markdown` blocks remote images for security; never depend on external image URLs.
<!-- mako-managed-skill -->
