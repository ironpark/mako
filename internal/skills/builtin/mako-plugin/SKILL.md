---
name: mako-plugin
description: >-
  Build, update, and verify Mako plugins (Revision 44) using sandboxed QuickJS-on-Wasm,
  scoped storage/SQLite/datafs, rich UI components (tree, graph, charts, wrap/grid/split,
  markdown links), built-in i18n, and CLI-callable mako-plugin agent tools.
---

# Mako Plugin Authoring Skill (Revision 44)

Use this skill whenever the user asks to create, extend, fix, or package a Mako
plugin (UI blocks, commands, CLI agent tools, scoped data, or P2P sync).

## 1. Quick Start & Plugin Location

1. **Locate the plugins directory**:
   - Call `list_blocks` on the `mako` MCP server (it returns `plugin_dir`), or inspect `~/.config/mako/plugins/` (Linux) / `~/Library/Application Support/mako/plugins/` (macOS).
   - Or scaffold with `mako plugins init <id> [dir]`.
2. **Folder layout**:
   ```text
   <pluginsDir>/<id>/
   ├── plugin.toml      # Manifest: metadata, permissions, blocks, commands, tools, settings
   ├── main.js          # ES module default export: { activate(mako) { ... } }
   └── lib/             # Optional ES modules imported via "./lib/..."
   ```
   - `id`: lowercase letters, digits, hyphens (`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`), must NOT start with `mako`.
3. **Activation & testing loop**:
   - The daemon watches `<pluginsDir>` automatically.
   - New or changed permissions start in `waiting` state until the user grants them (Settings ▸ Plugins or `mako plugins grant <id>`).
   - Place a block into the window layout via `patch_layout` (e.g. insert `"plugin:<id>/<blockId>"` into `"details"`) or Settings ▸ Plugins.
   - Reload after edits via the block frame's `↻` button or `mako plugins reload <id>`.

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
scope = "project"           # "", "global", "space", "project", "session"

[[contributes.command]]
id = "refresh"
title = "Refresh My Plugin"

[[contributes.tool]]
id = "list_items"
description = "List items in the active project"
scope = "project"
[contributes.tool.params]   # TOML table (NOT [[...]] array!)
filter = { type = "string", description = "Optional filter" }
limit = { type = "number", description = "Max items" }
include_done = { type = "boolean", required = false }

[contributes.settings]
default_view = { type = "select", label = "Default view", options = ["list", "tree"], default = "list", scope = "project" }
api_token = { type = "string", label = "API Token", secret = true }
```

### Permissions
- `storage` — per-scope KV store (`ctx.storage`: `getItem`, `setItem`, `deleteItem`, `listKeys`, 1 MiB)
- `db` — per-scope sandboxed SQLite (`ctx.db`: `query`, `exec`, `batch`, `stats`, 16 MiB)
- `db:sync` — row-level HLC CRDT P2P sync across paired Mako nodes (`syncEnable`, `syncPut`, `syncDelete`)
- `fs:data` — sandboxed per-scope files/folders under `plugin-data/<id>/files/<scope>/` (`ctx.datafs`: `read`, `write`, `list`, `stat`, `mkdir`, `delete`, `move`, `stats`, 64 MiB)
- `sessions:read` — `mako.sessions.listSessions`, `getSession`, `readTranscript`, `subscribe`
- `messages` — send/notify sessions
- `files:read`, `files:write` — read/write project files
- `net:<host>` — HTTPS fetch to that exact host only (`mako.http.fetch`)
- `secrets` — OS keychain secrets (`mako.secrets`)
- `layout` — propose window layout patches (`mako.layout`)
- `agents` — request spawning agent sessions (user-approved)
- `decision` — ask this node's configured decision model typed questions (`mako.decision`: `status`, `evaluate`, `tryEvaluate`, `submit`, `score`, `choice`, `yes`; events `on.decisionAnswered` / `on.decisionFailed`)
- `agents:ask` — list and ask one-shot questions of installed, `allow_template`-enabled agents without YOLO (`mako.agents`: `list`, `ask`, `cancel`; `ctx.agents.ask`; events `on.agentAnswered` / `on.agentAskFailed`, user-approved per agent/profile pair)

**Important**: Tool parameter types MUST be `"string"`, `"number"`, or `"boolean"` (never `"integer"` or `"array"`).

## 3. Scopes, Context (`ctx`), and Built-in i18n

Every scoped block `load(s, ctx)`, `render(s, ui, ctx)`, command `(ctx)`, and tool `(args, ctx)` receives a `ScopeContext`:
- `ctx.scope` (`"global" | "space" | "project" | "session"`), `ctx.scopeKey` (e.g. `"project:<id>"` or `""`)
- `ctx.spaceId`, `ctx.spaceName`, `ctx.projectId`, `ctx.projectName`, `ctx.roots` (array of project folder paths), `ctx.sessionId`
- `ctx.storage`, `ctx.settings`, `ctx.db`, `ctx.datafs` — automatically bound to `ctx.scopeKey`!
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

## 4. Rich UI Component Catalog (Revision 44)

All trees are validated strictly by `internal/uiblock`. Raw hex colors are forbidden; use tone tokens: `""`, `"muted"`, `"faint"`, `"accent"`, `"success"`, `"warning"`, `"danger"`.

### Layout & Containers
- `ui.column(children, { gap, fill, ... })` / `ui.row(children, { gap, fill, ... })` / `ui.stack(children)`
  - **`fill: true`** (root `column`/`row`/`split` ONLY): skips the outer frame scroll so you can pin a sticky top toolbar and scroll an inner `ui.scroll([...], { grow: 1 })`!
- `ui.wrap(children, { gap })` — flex-wrap container (use for tags, chips, and link pills so they never clip horizontally).
- `ui.grid(columns, children, { gap })` — 1..6 equal columns per row (great for KPI stat cards).
- `ui.split(first, second, { direction: "horizontal"|"vertical", ratio: 0.35 })` — resizable 2-pane split (`ratio` 0.1..0.9).
- `ui.card(children, { gap, tone, variant, onTap, drag, dragGroup, onDrop, dropAccept })` — `variant`: `"" | "primary" | "secondary" | "outline" | "ghost" | "danger" | "soft"`.
- `ui.scroll(children, { direction: "vertical"|"horizontal", maxHeight, grow, gap, variant })` — `variant: "follow"` pins scroll to the end (Revision 45).
- `ui.list(items, (item, i) => node, { gap })` / `ui.table(headers, rowNodes)` (every table child must be `ui.row`).

### Navigation & Disclosure
- `ui.tabs(activeValue, options, { key, onChange })`
- `ui.breadcrumb(segments, { key, onChange })` — clicking an ancestor fires `onChange` with its 0-based index string (`"0"`, `"1"`, ...).
- `ui.collapsible(title, children, { key, icon, tone, checked, onChange })` — `checked` = initial open state; `onChange` receives boolean.
- `ui.tree(items, { key, gap })` + `ui.treeItem(label, children?, { key, value, icon, tone, variant, checked, onTap, onChange, drag, dragGroup, onDrop, dropAccept })` — native tree with vertical indent guides, expand/collapse chevrons (`onChange` receives boolean), tap, `variant: "badge"` (tone on icon only + badge pill for `value`, Revision 45), and drag-and-drop!

### Controls, Text & Markdown
- `ui.text(str, { size: "small"|""|"large", weight: ""|"medium"|"semibold", tone, grow, onTap })`
- `ui.icon(name, { size, tone })` — use bundled Lucide names only (e.g. `book-open`, `folder`, `folder-open`, `folder-plus`, `file-text`, `search`, `plus`, `x`, `check`, `circle-check`, `shield-alert`, `network`, `tag`, `layers`, `sparkles`, `refresh-cw`, `arrow-left`, `chevron-right`, `chevron-down`, `git-branch`, `external-link`). Avoid unbundled names like `alert-triangle`.
- `ui.pill(text, { tone, icon, variant })` / `ui.chip(text, { tone, icon, variant, onTap })` — `variant`: `"soft" | "solid" | "outline"`, plus optional leading `icon` (Revision 45).
- `ui.button(text, { key, icon, size: "small"|""|"large", variant: "primary"|"secondary"|"outline"|"ghost"|"danger", tone, disabled, onTap })` — `text: ""` with `icon` renders a compact icon-only button!
- `ui.input(val, { key, placeholder, grow, onChange, onSubmit })`
- `ui.textarea(val, { key, placeholder, size: "small"|""|"large", onChange })` (`size: "large"` gives 14 lines!)
- `ui.toggle(checked, label, { key, onChange })` / `ui.select(val, options, { key, onChange })`
- `ui.progress(0..1)` or `ui.progress(0, { variant: "spinner", text })` (Revision 45) / `ui.divider()` / `ui.code(text, lang)`
- `ui.markdown(md, { key, onLink })`:
  - Renders Markdown + local pure-Go Mermaid diagrams (zero network).
  - `onLink: ["handler"]` intercepts non-`http(s)://` links (e.g. `[Target](myplugin:path)`) and passes the target string to your handler, while `http(s)://` links open externally.

### Visualization
- `ui.chart(type, series, { key, caption, labels })`:
  - `type`: `"line"`, `"bar"`, `"sparkline"`, `"donut"`, `"pie"`.
  - Each series: `{ name, tone, values: [...] }`. For `"donut"` / `"pie"`: 1 series, 1..16 non-negative finite values matching `labels.length` (labels <= 32 UTF-8 bytes!).
- `ui.graph(nodes, edges, { key, caption, value, maxHeight, onChange })`:
  - Interactive force-directed canvas (pan/zoom + click hit-test firing `onChange` with `node.id`!).
  - `nodes`: `[{ id, label, group?, tone?, subtitle?, weight? }]` (<= 120 unique IDs, positive finite weight).
  - `edges`: `[{ from, to, label?, tone?, dashed? }]` (<= 400 edges; `from`/`to` must exist in `nodes`).
  - `maxHeight`: 120..1200 px.

## 5. Agent CLI Tools (`mako-plugin`)

Contributed tools (`[[contributes.tool]]` + `mako.tools.register(id, async (args, ctx) => result)`) are exposed to AI agents via the `mako-plugin` CLI in every session's `PATH` (NOT dumped into MCP schemas):
```bash
mako-plugin list
mako-plugin help <plugin-id> [tool-id]
mako-plugin call <plugin-id> <tool-id> --key value --body -
```
- Scope isolation is enforced server-side from the caller's session token; tools whose scope is unavailable in the session (e.g. `project` tool outside a registered project) are automatically hidden and rejected.
- After mutating data in a tool handler, call `mako.blocks.reload("<blockId>", ctx.scopeKey)` so open UI blocks refresh immediately.

## 6. Common Pitfalls & Validation Rules

- **Keys**: must NOT contain `/` or `#`, and must be <= 128 bytes. Always sanitize paths with a `safeKey(prefix, raw)` helper.
- **Form values**: inside `on` submit handlers, read live values via `mako.blocks.values("<blockId>")` (keyed by node key) because users may click Save without blurring an input first.
- **Chart/Graph byte limits**: truncate labels by UTF-8 byte length (`<= 32` bytes for chart labels), not just JS string `.length` (Korean characters are 3 bytes each in UTF-8!).
- **Root `fill: true`**: always pair `fill: true` with an inner `ui.scroll([...], { grow: 1 })` so tall content scrolls cleanly below your sticky header.
- **No external images**: `ui.markdown` blocks remote images for security; never depend on external image URLs.
<!-- mako-managed-skill -->
