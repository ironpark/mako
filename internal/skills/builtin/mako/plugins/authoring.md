# Mako Plugin Authoring Guide (Revision 46)

Use this reference when creating, extending, fixing, or packaging a Mako plugin
(UI blocks, standalone panels, commands, CLI agent tools, Git/Forge integrations,
scoped data, or P2P sync). Pair with `plugins/ui-components.md` for the UI tree catalog.

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

## 4. Agent CLI Tools (`mako-plugin`)

Contributed tools (`[[contributes.tool]]` + `mako.tools.register(id, async (args, ctx) => result)`) are exposed to AI agents via the `mako-plugin` CLI in every session's `PATH`:
```bash
mako-plugin list
mako-plugin help <plugin-id> [tool-id]
mako-plugin call <plugin-id> <tool-id> --key value --body -
```
- Scope isolation is enforced server-side from the caller's session token; tools whose scope is unavailable in the session (e.g. `project` tool outside a registered project) are automatically hidden and rejected. (`folder` scope is not supported on tools.)
- After mutating data in a tool handler, call `mako.blocks.reload("<blockId>", ctx.scopeKey)` so open UI blocks refresh immediately.
<!-- mako-managed-skill -->
