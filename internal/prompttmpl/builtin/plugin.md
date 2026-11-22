---
description: Have the agent build a Mako plugin from your description and install it into your plugins folder
agents: [agy, claude, codex, pi, omo, opencode]
args:
  - name: text
    type: text
    required: true
    rest: true
    description: What the plugin should do, in your words
---
Build a complete, working Mako plugin from the description below and install it
into this node's plugins directory.

If the `mako-plugin` skill is available in your environment, load and follow it
first.

The description:

{{text}}

Workflow:

1. **Find the plugins folder**:
   - Call `list_blocks` on the `mako` MCP server to read `plugin_dir` (on macOS
     typically `~/Library/Application Support/mako/plugins/`, on Linux
     `~/.config/mako/plugins/`).
   - Pick a short lowercase hyphenated plugin `id` (never starting with `mako`).
2. **Design cleanly before writing files**:
   - Request only the minimum permissions needed (`storage`, `db`, `db:sync`,
     `fs:data`, `sessions:read`, `messages`, `files:read`, `files:write`,
     `net:<host>`, `secrets`, `layout`, `agents`).
   - Pick the right `scope` for blocks, tools, and settings (`global`, `space`,
     `project`, or `session`). Use `ctx.storage`, `ctx.db`, `ctx.datafs`, and
     `ctx.settings` so data stays isolated per scope.
   - Support built-in i18n via `mako.i18n.register({ en: {...}, ko: {...} })`
     and `ctx.t(...)` (matching the user's language, keeping stored canonical
     values locale-independent).
   - Use Revision 44 UI components cleanly:
     - Pin a compact sticky topbar with root `fill: true` + inner
       `ui.scroll([...], { grow: 1 })`.
     - Use `ui.wrap` for tags/chips, `ui.grid` for KPIs, `ui.split` for
       master-detail, `ui.tree`/`ui.treeItem` for hierarchies, `ui.collapsible`
       and `ui.breadcrumb` for structure, `ui.graph` and `ui.chart`
       (`line`/`bar`/`sparkline`/`donut`/`pie`) for visualization, and
       `ui.markdown(..., { onLink })` for clickable internal links and local
       Mermaid diagrams.
   - Expose useful agent automation via `[[contributes.tool]]` (invoked by
     agents via the `mako-plugin` CLI) and call `mako.blocks.reload(id, ctx.scopeKey)`
     after mutating state so open UI blocks refresh live.
3. **Write modular files into `<plugin_dir>/<id>/`**:
   - `plugin.toml`, `main.js`, and optional `lib/*.js` helpers (ES modules).
   - Follow strict `uiblock` rules: keys must not contain `/` or `#` (<= 128 B),
     use bundled Lucide icon names only (e.g. `shield-alert`, not `alert-triangle`),
     keep chart labels <= 32 UTF-8 bytes, declare `[contributes.tool.params]` as
     a TOML table with types `string`, `number`, or `boolean` only, and read
     live form values with `mako.blocks.values("<blockId>")` on submit.
4. **Activate & report**:
   - Tell the user the exact folder written, the permissions requested, and how
     to grant & open it (`Settings ▸ Plugins` or `mako plugins grant <id>`).
   - If the plugin already has permissions granted, offer to place its block
     into the layout with `patch_layout`.

## Mako Plugin Quick Reference (Revision 44)

{#raw}
```toml
id = "example-plugin"
version = "0.1.0"
publisher = "local"
api = 1
entry = "main.js"
permissions = ["storage"]   # storage | db | db:sync | fs:data | sessions:read | messages | files:read | files:write | net:<host> | secrets | layout | agents

[[contributes.block]]
id = "main"
title = "Example"
slots = ["right", "bottom"]
scope = "project"           # global | space | project | session

[[contributes.command]]
id = "refresh"
title = "Refresh Example"

[[contributes.tool]]
id = "list_items"
description = "List items in the current project"
scope = "project"
[contributes.tool.params]
query = { type = "string", description = "Optional search filter" }
limit = { type = "number", description = "Maximum results" }

[contributes.settings]
mode = { type = "select", label = "Mode", options = ["compact", "full"], default = "full", scope = "project" }
```

```js
export default {
  activate(mako) {
    mako.i18n.register({
      en: { empty: "No items yet.", add: "Add" },
      ko: { empty: "아직 항목이 없습니다.", add: "추가" },
    });

    mako.blocks.register("main", {
      state: { items: [], draft: "" },
      async load(s, ctx) {
        const res = await ctx.storage.getItem("items");
        s.items = res.found ? JSON.parse(res.value) : [];
      },
      render(s, ui, ctx) {
        return ui.column([
          ui.row([
            ui.input(s.draft, { key: "draft", placeholder: ctx.t("add"), grow: 1, onSubmit: ["addItem"] }),
            ui.button(ctx.t("add"), { key: "add-btn", size: "small", variant: "primary", onTap: ["addItem"] }),
          ], { gap: 6 }),
          ui.scroll([
            s.items.length === 0
              ? ui.card([ui.text(ctx.t("empty"), { tone: "muted" })])
              : ui.list(s.items, (it, i) => ui.row([ui.text(it, { grow: 1 })], { key: `item-${i}` }), { gap: 4 }),
          ], { grow: 1 }),
        ], { gap: 8, fill: true });
      },
      on: {
        async addItem(s) {
          const ctx = mako.blocks.context("main");
          const text = String(mako.blocks.values("main")["draft"] ?? s.draft).trim();
          if (!text) return;
          s.items = [...s.items, text];
          s.draft = "";
          await ctx.storage.setItem("items", JSON.stringify(s.items));
        },
      },
    });

    mako.tools.register("list_items", async (args, ctx) => {
      const res = await ctx.storage.getItem("items");
      return { project: ctx.projectName, items: res.found ? JSON.parse(res.value) : [] };
    });
  },
};
```
{/raw}
