# Writing a mako plugin

A plugin adds to the mako window: blocks a window layout document places,
commands in the palette, and settings. It is a folder of a manifest,
`plugin.toml`, and JavaScript, run by the node's daemon in a sandbox of its
own; it reaches anything outside only through the Host API, and only as
far as the user allowed. The window draws what a plugin's blocks describe,
with its own components, in its own theme, inside a frame naming the
plugin. The design, and what is still to come, is in
[plugins-plan.md](plugins-plan.md); the wire format of the blocks is in
[protocol.md](protocol.md#plugin-blocks).

## Contents

- [A plugin's folder](#a-plugins-folder)
- [The manifest](#the-manifest)
- [The module](#the-module)
- [Scopes](#scopes)
- [The UI vocabulary](#the-ui-vocabulary)
- [The Host API](#the-host-api)
- [Permissions](#permissions)
- [Limits](#limits)
- [Developing one](#developing-one)
- [Installing from a registry](#installing-from-a-registry)
- [Publishing](#publishing)
- [For mako's developers](#for-makos-developers)
- [Publishing the example to the default registry](#publishing-the-example-to-the-default-registry)

## A plugin's folder

In this version a plugin is a folder of the `plugins` folder beside the
daemon's `config.json` (`~/.config/mako/plugins/<id>/`, or
`$MAKO_HOME/plugins/<id>/`), named for the plugin's ID:

    plugins/
      ci-status/
        plugin.toml
        main.js
        lib/runs.js      (any .js file the entry imports)

The daemon reads the folder again whenever a file in it changes, as in
development (see [Developing one](#developing-one)). A folder is written
there by hand, or by the daemon installing a signed package from a
registry ([Installing](#installing-from-a-registry)); the daemon runs both
alike, and tells them apart by what it installed. First-party plugins
shipped inside mako itself (such as `mako-git`, which backs the built-in
`mako.git` panel; revision 46) are loaded from an embedded bundle using
the reserved `mako-` ID prefix and `publisher = "mako"`; their declared
permissions are granted automatically (while honoring user disablement)
and they cannot be uninstalled or replaced by registry packages.

[examples/plugins/ci-status](../examples/plugins/ci-status) is a complete
plugin: a block of a GitHub repository's latest Actions runs, kept in its
store for when GitHub cannot be reached, a command that runs the latest
failed one again, and its repository and token as settings. Copy it into
the plugins folder to try it.

## The manifest

```toml
id = "ci-status"          # the folder's name; lowercase letters, digits, hyphens
version = "0.1.0"         # major.minor.patch
publisher = "ironpark"
api = 1                   # the Host API version; this mako takes 1
entry = "main.js"         # the entry module, relative to the folder
permissions = ["net:api.github.com", "storage", "sessions:read"]

[[contributes.block]]
id = "ci"                 # registered as plugin:ci-status/ci
title = "CI"
slots = ["right", "bottom"]   # where a layout might place it: left, right, top, bottom
# scope = "project"       # optional: global (default), space, project, session, or folder
# chrome = "auto"         # optional: "auto" (default) or "toolbar" (revision 46)

[[contributes.command]]
id = "rerun"
title = "Re-run the latest failed CI run"

[[contributes.tool]]      # optional MCP tool exposed to running agents as plugin_<id>_<tool>
id = "list_runs"
description = "Return the latest CI runs as JSON"
# scope = "project"       # optional: global (default), space, project, or session
[contributes.tool.params]
branch = { type = "string", description = "Filter by branch name" }

[contributes.settings]    # the settings page is made from this
repo = { type = "string", label = "Repository", description = "owner/name on GitHub", scope = "project" }
token = { type = "secret", label = "GitHub token" }
```

The manifest is read strictly: a key mako does not know, a value of the
wrong type, an ID or path out of its grammar, or an `api` other than 1 is
refused, and the plugin does not run, the reason shown in Settings ▸
Plugins and in its blocks' places.

| Key | |
|---|---|
| `id` | Lowercase letters, digits and inner hyphens, at most 64; not starting with `mako` (`mako-` is reserved for bundled first-party plugins). It must be the folder's name. |
| `version` | `major.minor.patch`, with an optional `-pre` part. |
| `publisher` | As `id` (`"mako"` is reserved for bundled first-party plugins). |
| `api` | 1. |
| `entry` | A relative path to a `.js` file in the folder: no `..`, no leading `/` or `./`, no backslash. |
| `permissions` | At most 16; see [Permissions](#permissions). |
| `contributes.block` | At most 16: `id` (lowercase letters, digits, hyphens, at most 32), `title` (at most 80 characters), `slots`, optional `scope` (`global` default, `space`, `project`, `session`, or `folder`; see [Scopes](#scopes)), optional `chrome` (`"auto"` default, or `"toolbar"` so a leading `ui.toolbar` replaces the solo dock header title; revision 46), and optional `hidden = true` (bundled first-party plugins only, omitting the block from catalogs when placed via a `mako.*` alias). |
| `contributes.command` | At most 32: `id`, `title`. |
| `contributes.tool` | At most 16: `id`, `description` (at most 240 characters), optional `scope` (`global` default, `space`, `project`, or `session`; `folder` is not supported for tools), and optional `params` map (`[contributes.tool.params]` with `type` `string`/`number`/`boolean`, `description`, `required`, at most 16). Exposed to running agents via Mako's built-in MCP server as `plugin_<id>_<tool>`. |
| `contributes.settings` | At most 32, by key (lowercase letters, digits, `_`): `type` (`string`, `secret`, `boolean`, `number`, `select`), `label`, `description`, `options` (a select's, and only a select's), `default` (of the type; none for a secret), and optional `scope` (`global` default, `space`, `project`, `session`, or `folder`). A secret's value is kept in the system's keychain ([Secrets](#secrets)), at most 2 048 bytes. |

Titles, labels and options may not hold control characters or the Unicode
marks that reorder or hide text.

## The module

The entry module's default export is the plugin. `activate` runs when the
plugin starts, and registers what the manifest declares; registering a
block or command the manifest does not declare throws, and an `activate`
that throws stops the plugin.

```js
let mako;

export default {
  activate(m) {
    mako = m;
    mako.blocks.register("ci", {
      state: { runs: [] },                       // JSON: kept while the plugin sleeps
      refresh: 60,                               // load again every 60 s while shown (at least 15)
      async load(s) {                            // when the block is first shown, and on refresh
        const res = await mako.http.fetch({ url: "https://api.github.com/repos/o/r/actions/runs" });
        s.runs = JSON.parse(res.body).workflow_runs;
      },
      render: (s, ui) => ui.column([             // the state drawn; it may not call the Host API
        ui.text("Latest runs").weight("medium"),
        ui.list(s.runs, (r) => ui.row([ui.pill(r.conclusion, { tone: "danger" }), ui.text(r.name)],
          { key: r.id, onTap: ["open", r.id] })),
      ]),
      on: {                                      // what each message a node sends does
        open: (s, id) => mako.commands.runCommand({ id: "mako.openUrl", arg: s.runs.find((r) => r.id === id).html_url }),
      },
    });
    mako.commands.register("rerun", async () => {
      const s = mako.blocks.state("ci");         // a command may change a block's state
      s.note = "started";
      mako.blocks.reload("ci");                  // and have its load run once the command ends
    });
  },
};
```

The model is Elm's: a block's state lives in the plugin; `render(state,
ui)` returns a tree; a node's `onTap: [name, ...args]` sends `name` to the
block's `on`, which changes the state, and the block is drawn again after
every call (`load`, a message, a command). An input's, toggle's or
select's `onChange` sends its value last. Calls into the plugin run one at
a time.

`mako` is the [Host API](#the-host-api), with `mako.blocks` (`register`,
`state`, `values`, `reload`, `context`), `mako.commands.register`, and
`mako.tools.register`. Lifecycle hooks (`load(state, ctx)`,
`render(state, ui, ctx)`), commands `(ctx)`, and MCP tools `(args, ctx)` each
receive a [`ScopeContext`](#scopes) `ctx`. Inside an event handler,
`mako.blocks.context(blockId)` returns the active `ScopeContext`, and
`mako.blocks.values(blockId, scopeKey?)` returns a map of the block's current
form field values (keyed by full node path and, when unique, by the node's
trailing `key`). `mako.tools.register(id, fn)` registers the handler for a
`[[contributes.tool]]` declared in the manifest: it receives `(args, ctx)` and
returns any JSON-serializable value (or a Promise of one). There are no timers:
a block that wants to be read again every so often says `refresh`. Its types,
with every method's, are in
[mako.d.ts](../internal/plugin/hostapi/mako.d.ts), generated from the schema:
point your editor at it.

## Scopes

Protocol revision 42 introduces workspace scopes (extended with `folder` in
revision 46) so blocks, MCP tools, settings, and storage can be isolated per
workspace target rather than sharing one global instance:

| Scope | Canonical `scopeKey` | Isolated per |
|---|---|---|
| `global` (default) | `""` | The node as a whole. |
| `space` | `space:<spaceId>` | A workspace space. |
| `project` | `project:<projectId>` | A project (for example, one Kanban board per project). |
| `session` | `session:<sessionId>` | A single running agent or terminal session. |
| `folder` (rev 46) | `folder:<node>/<base64url-nopad(path)>` | A single folder (work tree or directory) on one node (`<node>` is `local` for the local node or a peer node ID). Supported on blocks and settings, not tools. |

Canonical scope IDs for `space`, `project`, and `session` must be 1 to 256 safe
ASCII identifier characters (`[A-Za-z0-9_][A-Za-z0-9._:/+-]{0,255}`, without
`..`); `folder` scope IDs (`<node>/<base64url-nopad(path)>`) may be up to 1 024
bytes. Invalid scope keys are refused everywhere.

### Manifest declarations

Declare `scope` on any block, tool, or setting in `plugin.toml` (omitting it
defaults to `"global"`):

- **`[[contributes.block]]` `scope`**: The daemon keeps a separate runtime
  instance `(blockId, scopeKey)` per active scope—independent state, rendered
  tree, refresh schedule, and sleep/restore persistence. Unwatched instances are
  cached up to an LRU cap of 16 per block; actively watched instances are never
  evicted.
- **`[[contributes.tool]]` `scope`**: Declares the scope boundary required when
  an agent calls `plugin_<id>_<tool>` over MCP.
- **`[contributes.settings]` `<key>.scope`**: Allows per-scope overrides on top
  of the global default. Non-secret overrides are stored under `@scoped` in
  `settings.json`; secret overrides are stored in the system keychain under
  `setting:<key>@<scopeKey>` and are never sent to front-ends. Reading a setting
  in a scope resolves `scoped override → global value → manifest default`;
  clearing an override (`""`) reverts that scope to the global value.

### JavaScript `ScopeContext` (`ctx`)

`load(state, ctx)`, `render(state, ui, ctx)`, command handlers `(ctx)`, and
tool handlers `(args, ctx)` receive a frozen `ScopeContext`:

| Field / helper | Description |
|---|---|
| `ctx.scope` | `"global"`, `"space"`, `"project"`, `"session"`, or `"folder"`. |
| `ctx.scopeKey` | Canonical scope key (`""`, `"space:<id>"`, `"project:<id>"`, `"session:<id>"`, or `"folder:<node>/<b64path>"`). |
| `ctx.spaceId`, `ctx.spaceName` | Resolved space ID and human-readable name (when applicable). |
| `ctx.projectId`, `ctx.projectName` | Resolved project ID and human-readable name (when applicable). |
| `ctx.roots` | Read-only array of project root directories on this node (for `folder` scope, `[ctx.folder]`). |
| `ctx.sessionId` | Resolved session ID (when applicable). |
| `ctx.node`, `ctx.folder` | Resolved node ID (or `"local"`) and absolute folder path for `folder`-scoped calls (revision 46). |
| `ctx.view` | Frozen `{ standalone: boolean }` (revision 46). `true` when every active viewer displays the block solo (AND across viewers) rather than as one tab of a multi-tab stack; re-renders automatically when the aggregate flips. Combine with `ui.split(..., { direction: "auto" })` to adapt layout to panel width. |
| `ctx.storage` | Bound storage helper (`getItem(key)`, `setItem(key, value)`, `deleteItem(key)`, `listKeys(prefix?)`) auto-forwarding `{ scope: ctx.scopeKey }`. Scoped keys are isolated under `@scope/<scopeKey>/<key>` and share the plugin's storage quota; raw `@scope/` keys are refused. |
| `ctx.settings` | Bound settings helper (`getSetting(key)`, `listSettings()`) auto-forwarding `{ scope: ctx.scopeKey }`. |
| `ctx.db`, `ctx.datafs`, `ctx.agents` | Scoped helpers for SQLite (`query`, `exec`, `batch`, `stats`, `syncEnable`, `syncPut`, `syncDelete`), private files (`read`, `write`, `list`, `stat`, `mkdir`, `delete`, `move`, `stats`), and agent asks (`list`, `ask`, `cancel`). |
| `ctx.git`, `ctx.forge` | Scoped Git (`status`, `history`, `showCommit`, `watch`, `unwatch`, `run`) and Forge (`branchStatus`) helpers auto-filling `target` from `ctx.scopeKey` (or `{ node, path }`) (revision 46). |
| `ctx.ui` | Window-intent helpers (`openDiff`, `openFile`, `copy`, `openForge`, `createPullRequest`, `toast`, `openUrl`) forwarding gesture-gated `commands.runCommand` calls with `ctx.node`/`ctx.folder` filled by default (revision 46). |

Inside a block's `on` event handlers, call `mako.blocks.context(blockId)` to
retrieve the active `ScopeContext`. `mako.blocks.state(id, scopeKey?)`,
`mako.blocks.values(id, scopeKey?)`, and `mako.blocks.reload(id, scopeKey?)`
all accept an optional `scopeKey` and default to the currently active call's
scope.

```js
mako.blocks.register("board", {
  state: { cards: [] },
  async load(s, ctx) {
    const stored = await ctx.storage.getItem("board:v1");
    s.cards = stored.found ? JSON.parse(stored.value).cards : [];
  },
  render(s, ui, ctx) {
    const title = ctx.projectName ? `Kanban · ${ctx.projectName}` : "Kanban";
    return ui.text(title, { weight: "semibold" });
  },
  on: {
    async addCard(s, title) {
      const ctx = mako.blocks.context("board");
      s.cards.push({ title });
      await ctx.storage.setItem("board:v1", JSON.stringify({ cards: s.cards }));
    },
  },
});

mako.tools.register("add_card", async (args, ctx) => {
  const s = mako.blocks.state("board", ctx.scopeKey);
  s.cards.push({ title: args.title });
  await ctx.storage.setItem("board:v1", JSON.stringify({ cards: s.cards }));
  return { project: ctx.projectName, count: s.cards.length };
});
```

See [examples/plugins/kanban](../examples/plugins/kanban) for a full
`project`-scoped block and MCP tool example, and
[examples/plugins/ci-status](../examples/plugins/ci-status) for a
`project`-scoped setting (`repo`) with global fallback.

### MCP tool strict scope boundaries

When a running agent calls a plugin MCP tool (`plugin_<id>_<tool>`), the daemon
derives `ScopeContext` **strictly from the calling agent's own session** (its
session ID, the project matching its working folder, and that project's space).
Tool arguments can never override or specify another scope:

- A `project`-scoped tool called by an agent in Project A can only read or
  mutate Project A's block state and storage, and is refused if the session has
  no associated project.
- Likewise, `space`-scoped and `session`-scoped tools require a matching space
  or session ID on the calling session.
- A `global` tool always receives `scopeKey == ""` regardless of the caller's
  session or project.

### Follow / Pin, empty-scope state, and settings UI

- **Follow vs. Pin (Desktop & Mobile)**: A scoped block follows the window's
  active space, project, session, or folder by default and automatically
  re-subscribes when you switch context. Click the scope chip in the block's
  header (or use *Pin scope ▸* in the header `⋯` / tab context menu) to
  **Pin** that block instance to a specific target (stored as `"scope"` in that
  layout node's `props`) or switch back to **Follow active**. On Android,
  tapping the scope chip in the block header opens a menu to switch targets on
  that computer.
- **Standalone panel chrome & toolbars (revision 46)**: When a plugin block is
  drawn inside a window layout, the layout provides one header row — either a
  dock header when solo (a bare panel block or a one-child stack) or a tab strip
  plus optional slim tools row when tabbed — with third-party provenance shown
  in the header accessory and tab. If the tree root's first child is a `row`
  with `variant: "toolbar"` (`ui.toolbar(...)`), the window lifts it into that
  header; setting `chrome = "toolbar"` on `[[contributes.block]]` lets the
  lifted toolbar replace the title text in the solo dock header. `ctx.view.standalone`
  reflects whether all active viewers of that `(block, scope)` instance display
  it solo (AND aggregation across viewers).
- **Empty-scope behavior**: When a `space`, `project`, `session`, or `folder`
  block is in Follow mode and no matching target is currently active, the window
  (and Android app) does not open a block stream; instead it renders a native
  placeholder prompting you to select or pin a target.
- **Settings scope selector & CLI**: In **Settings ▸ Plugins**, any plugin with
  scoped settings shows a scope selector (**Global default** plus available
  spaces or projects). Each setting shows whether it inherits the global
  default or overrides it for the selected scope, with a **Reset to default**
  action that clears the override. From a terminal, pass `--scope` to
  `mako plugins set`:

  ```bash
  mako plugins set ci-status repo owner/my-repo --scope project:my-proj
  # Clear the override so project:my-proj falls back to the global value:
  mako plugins set ci-status repo "" --scope project:my-proj
  ```

## The UI vocabulary

| Kind | Builder | |
|---|---|---|
| `column`, `row`, `stack` | `ui.column(children, opts)`, `ui.row(children, opts)`, `ui.toolbar(children, opts)`, `ui.stack(children, opts)` | Children one over another, side by side, or over each other. A child with `grow` shares the room left. Set `fill: true` on a root `column`, `row`, or `split` to skip outer auto-scroll and let inner `ui.scroll(..., { grow: 1 })` panes scroll independently. `row` supports `variant: "toolbar"` (`ui.toolbar`, revision 46): lifted into the window's panel chrome when it is the root's first child, or drawn as a compact inline toolbar elsewhere. |
| `card` | `ui.card(children, opts)` | A bordered surface container for grouping items (such as a Kanban card or summary tile); supports `variant` (`primary`, `secondary`, `outline`, `ghost`, `danger`, `soft`). |
| `scroll` | `ui.scroll(children, opts)` | Scrollable container: `direction` (`vertical` default, or `horizontal`), `maxHeight` (px, up to 4000) for vertical scroll areas, and `variant: "follow"` to keep the scroll pinned to the end (revision 45). |
| `wrap` | `ui.wrap(children, opts)` | Flex-wrap container that flows children onto multiple lines with `gap`. |
| `grid` | `ui.grid(columns, children, opts)` | N-column grid (`columns` 1 to 6, `gap`). |
| `split` | `ui.split(first, second, opts)` | Resizable 2-pane split container: `direction` (`horizontal` default, `vertical`, or `auto` — horizontal at ≥ 640 px and vertical below, revision 46), `ratio` (0.1 to 0.9, default 0.5), `fill` when at root, and exactly 2 children. |
| `collapsible` | `ui.collapsible(title, children, opts)` | Disclosure/accordion section: `text` (title), `icon`, `tone`, `checked` (initially expanded), optional `onChange` (receives boolean), and nested children. |
| `breadcrumb` | `ui.breadcrumb(segments, opts)` | Clickable path trail (`options`: segment labels); `onChange` fires with the clicked segment's 0-based index as a decimal string (`"0"`, `"1"`, ...). |
| `tree` | `ui.tree(items, opts)` | Native hierarchical tree container holding `tree_item` children. |
| `tree_item` | `ui.treeItem(label, [children], opts)` | Hierarchical tree node inside `tree` or `tree_item`: `text` (primary label), `value` (optional secondary/meta text), `icon`, `tone`, `variant` (`badge` applies tone to icon only and renders `value` as a rounded badge, revision 45), `checked` (expanded/selected state), `check` (`"on"`, `"off"`, `"mixed"`, revision 46) with `onCheck` (fires `"on"` or `"off"`), `badges`, `contextMenu`/`contextArg`, `onTap`, optional `onChange` (boolean expand/collapse), `drag`, `dragGroup`, `onDrop`, `dropAccept`, and optional nested `tree_item` children. |
| `list` | `ui.list(items, (item, i) => node, opts)` | A column of rows; a row that sends a tap lights up under the pointer, as the window's lists do. |
| `table` | `ui.table(headers, rows, opts)` | Structured rows with optional header columns (`labels`). |
| `text` | `ui.text(text, opts)` | `weight` (`medium`, `semibold`), `size` (`small`, `large`), `tone`. |
| `icon` | `ui.icon(name, opts)` | A [bundled icon](../internal/gui/iconset/icons)'s name; another is drawn as a square. |
| `pill` | `ui.pill(text, opts)` | A state in a word, on a wash of its tone; supports `icon` and `variant` (`soft`, `solid`, `outline`, revision 45). |
| `chip` | `ui.chip(text, opts)` | A name of the machine's kind: a hash, a branch, or a tag; supports `icon` and `variant` (`soft`, `solid`, `outline`, revision 45). |
| `button` | `ui.button(text, opts)` | `icon` beside the text (or empty `text` with `icon` for compact icon-only buttons); `size` (`small`, `large`); `variant` (`primary`, `secondary`, `outline`, `ghost`, `danger`); tone `accent` fills it, `danger` warns. |
| `input` | `ui.input(value, opts)` | `placeholder`; `onChange` on leaving it, `onSubmit` on Return. |
| `textarea` | `ui.textarea(value, opts)` | Multi-line text input; `placeholder`, `size` (`small`, `large`); `onChange` on leaving it, `onSubmit` on shortcut. |
| `toggle` | `ui.toggle(checked, label, opts)` | `onChange` with `true` or `false`. |
| `select` | `ui.select(value, options, opts)` | `onChange` with the option. |
| `tabs` | `ui.tabs(value, options, opts)` | Horizontal tab strip; `onChange` with the chosen option. |
| `markdown` | `ui.markdown(text, opts)` | Drawn as the window draws Markdown; optional `onLink` fires with the clicked link target. |
| `code` | `ui.code(text, language, opts)` | Monospaced. |
| `progress` | `ui.progress(value, opts)` | From 0 to 1, or `variant: "spinner"` with optional `text` for an indeterminate spinner (revision 45). |
| `divider` | `ui.divider(opts)` | A hairline. |
| `chart` | `ui.chart(type, series, opts)` | `type` `line`, `bar`, `sparkline` (1 to 4 series of up to 200 finite numbers, 600 in all), or `donut` / `pie` (revision 44: 1 series of 1 to 16 non-negative values with matching `opts.labels`); `opts.caption` titles the chart. A series's colour is its `tone`, or the theme's chart colours in order. It sends no messages. |
| `graph` | `ui.graph(nodes, edges, opts)` | Declarative interactive node-edge graph canvas (revision 44): up to 120 nodes (`{id, label, group, tone, subtitle, weight}`) and 400 edges (`{from, to, label, tone, dashed}`), `value` for the focused/selected node ID, optional `maxHeight` (120 to 1200 px), `caption`, and `onChange` firing with the clicked node ID. |
| `menu` | `ui.menu(face, items, opts)`, `ui.contextMenu(key, items, opts)` | Dropdown menu (`face` is a label string or `{text, icon}`, `variant`: `""`, `"ghost"`, or `"outline"`) or non-rendered context-menu declaration (`variant: "context"`, unique `key` across the tree; revision 46). Children must be `menu_item`, `divider`, or nested `menu` (max nesting depth 2). Sends no messages itself. |
| `menu_item` | `ui.menuItem(text, opts)` | Action entry inside a `menu` (revision 46): `text`, optional `icon`, `tone` (`"danger"`), `disabled`, `checked`, and `onTap`. When opened as a context menu, the opener's `contextArg` (or a `dag` row's `id`) is appended as the last argument to `onTap`. |
| `checkbox` | `ui.checkbox(state, label, opts)` | Tri-state checkbox (revision 46): `check` (`"on"`, `"off"`, `"mixed"`, or boolean), `text` label, `disabled`; `onChange` fires with `"on"` or `"off"`. |
| `dialog` | `ui.dialog(title, children, opts)` | Modal dialog (revision 46): `text` title, optional `variant: "danger"`, children (body text, inputs, buttons). At most one per tree; `onChange` fires with `"dismiss"` on Escape or close, and form values ride with button taps. Third-party plugin dialogs display a `<plugin id> asks` footer. |
| `dag` | `ui.dag(rows, opts)` | Virtualized commit/DAG graph (revision 46): up to 1 000 data-driven `rows` (`{id, parents, text, detail?, meta?, badges?, tone?, strong?, icon?}`, ≤ 8 parents, ≤ 6 badges per row, unique `id` ≤ 64 chars, `text`/`detail` ≤ 320 chars, `meta` ≤ 128 chars), `value` (selected row ID), optional `contextMenu` (passes row `id` as `contextArg`), `maxHeight` (up to 4 000 px); `onChange` fires with the clicked row's `id`. |

Options (`opts`, or the node's methods of the same names): `key`, `text`,
`tone`, `variant`, `weight`, `size`, `grow`, `gap`, `columns`, `ratio`, `fill`,
`icon`, `checked`, `value`, `disabled`, `badges` (up to 8 `{text, tone?, variant?, icon?}` trailing tags with `variant` in `soft`/`solid`/`outline`/`mono` and text ≤ 32 runes; `.badges([...])`),
`check` (`.check(state)`), `onCheck` (`.onCheck(name, ...args)`),
`contextMenu` (`.contextMenu(key, arg?)`), `contextArg` (at most 256 chars; `.contextArg(arg)`),
`rows`, `onTap`, `onChange`, `onSubmit`, `onLink`, `drag`, `dragGroup`,
`onDrop`, `dropAccept`, `direction`, `maxHeight`, `placeholder`, `caption`, and
a chart's or table's `labels`.

### Localization (`mako.i18n`, `ctx.locale`, `ctx.t`)

Plugins can localize block UIs, commands, and tool outputs using the built-in
`mako.i18n` helper and the front-end's active BCP-47 language tag (revision 44):

- **`mako.i18n.register(catalog)`**: Registers or merges locale catalogs keyed
  by BCP-47 tag (e.g. `{ en: { nav: { home: "Home" } }, ko: { nav: { home: "홈" } } }`).
  Both nested objects and flat dot-separated keys are supported and merged cleanly.
- **`mako.i18n.t(key, params?, locale?)`**: Looks up `key` with the fallback
  chain exact normalized tag (e.g. `ko-kr`) -> primary language subtag (`ko`)
  -> `en` -> `key` itself, and replaces `{name}` placeholders from `params`.
- **`ctx.locale` & `ctx.t(key, params?)`**: Every `ScopeContext` passed to
  block `load`/`render`, commands, and tools carries `ctx.locale` (defaulting
  to `"en"`) and a `ctx.t(key, params?)` helper bound to `ctx.locale`. When
  `locale` is omitted from `mako.i18n.t`, it uses the active context's locale.

**Drag and drop** (revisions 40–41): set `drag` (a non-empty payload string, up
to 256 bytes) on a container or display node to make it draggable, and
`onDrop: [name, ...args]` on any target node of the same block. Optionally tag a
draggable node with `dragGroup` (lowercase letters, digits, hyphens/underscores)
and restrict a drop target with `dropAccept: [...]` so only matching groups can
drop on it. When a dragged node is dropped on an accepting target, the block's
`on[name]` handler is called with `args` followed by the dragged node's `drag`
payload. Reorderable items that carry both `drag` and `onDrop` draw an accent
insertion bar when hovered, and on Android touch screens users can long-press a
draggable item and tap a highlighted target to drop it.

The chart is new in revision 33 of the protocol: a window or phone that
does not know a kind draws nothing in its place (the Android app, from
the release after 0.5.0, draws charts, and says it cannot show a kind it
does not know). A block that must read on older ones says what a chart
shows in text beside it too.

**Colours are tokens**: `tone` is `muted`, `faint`, `accent`, `success`,
`warning` or `danger`, which the theme in force gives a colour; a node has
no other colour. A tree is refused, and the block shows why, for a kind or
field not in the vocabulary, a raw colour, more than 2 000 nodes or 24
levels, two siblings of one key, or text with control or reordering
characters.

**Keys.** Give a list's rows a `key` (a string or number, without `/` or
`#`): a node's id is its path by keys, so a row that moves is moved, not
redrawn, and a message's node is the one the user saw.

An agent can show a block of this vocabulary without writing a plugin:
the `show_ui` tool takes a tree in the very JSON a render returns
([agent-mcp.md](agent-mcp.md#ui-of-your-own)), and the user's actions on
it come back to the agent as messages.

## The Host API

Everything a plugin may ask of mako. Every method takes one object and
returns a promise of one; it fails, with a message saying why, when the
plugin lacks the permission. The schema, with every field, is
[proto/mako/plugin/v1](../proto/mako/plugin/v1); the types are in
[mako.d.ts](../internal/plugin/hostapi/mako.d.ts).

| Method | Permission | |
|---|---|---|
| `log.write({level, message})` | none | A line in the daemon's log, under the plugin's name; 120 lines a minute. |
| `settings.getSetting({key, scope})` | none | `{setting: {key, type, text, flag, number, set}}`: the scoped override (when `scope` is set), else the global value, else the default. |
| `settings.listSettings({scope})` | none | Every setting, resolved for `scope` (or global when omitted). |
| `storage.getItem({key, scope})` | `storage` | `{found, value}` in global or scoped storage. |
| `storage.setItem({key, value, scope})` | `storage` | `{used, quota}`; refused over the shared quota. |
| `storage.deleteItem({key, scope})` | `storage` | `{found}`. |
| `storage.listKeys({prefix, scope})` | `storage` | `{keys}`, sorted, within that scope. |
| `db.query({sql, paramsJson, scope})` | `db` | `{columns, rowsJson, truncated}`: read-only SQLite query (`SELECT`/`WITH`/`EXPLAIN`, FTS5/JSON1 supported); `ctx.db.query(sql, params)` returns `{columns, rows, truncated}` as row objects. |
| `db.exec({sql, paramsJson, scope})` | `db` | `{rowsAffected, lastInsertId}`: single DML or DDL statement against the scoped SQLite database. |
| `db.batch({statements, scope})` | `db` | `{results}`: up to 64 DML/DDL statements run atomically in one transaction. |
| `db.stats({scope})` | `db` | `{usedBytes, quotaBytes, syncTables}`: total plugin SQLite disk usage, quota, and synced tables in that scope. |
| `db.syncEnable({table, scope})` | `db:sync` | `{table}`: registers a row-level CRDT synced table `(id, doc, updated_at, deleted)` replicated across mutually trusted peers. |
| `db.syncPut({table, id, docJson, scope})` | `db:sync` | `{updatedAt}`: stamps a JSON document row with the node's HLC clock, updates SQLite, and schedules P2P replication. |
| `db.syncDelete({table, id, scope})` | `db:sync` | `{deleted}`: writes a CRDT tombstone for a synced row and replicates the deletion to paired peers. |
| `datafs.readData({path, offset, length, scope})` | `fs:data` | `{found, text, data, size, eof, binary, modifiedMs}`: reads a regular file from the plugin's private per-scope directory (`plugin-data/<id>/files/`). |
| `datafs.writeData({path, text, data, makeDirs, scope})` | `fs:data` | `{size, usedBytes, quotaBytes}`: atomically writes a regular file inside the plugin's private data directory (up to 4 MiB per file, 64 MiB plugin quota). |
| `datafs.listData({path, recursive, scope})` | `fs:data` | `{entries, truncated}`: lists files and folders (optionally recursively) inside the plugin's private data directory. |
| `datafs.statData({path, scope})` | `fs:data` | `{found, dir, size, modifiedMs}`: returns metadata for a file or folder inside the plugin's private data directory. |
| `datafs.mkdirData({path, scope})` | `fs:data` | Creates a folder (and parents) inside the plugin's private data directory. |
| `datafs.deleteData({path, recursive, scope})` | `fs:data` | `{deleted}`: removes a file or folder (`recursive: true` for non-empty folders) inside the plugin's private data directory. |
| `datafs.moveData({fromPath, toPath, makeDirs, scope})` | `fs:data` | Renames or moves a file or folder within the same scope of the plugin's private data directory. |
| `datafs.dataStats({scope})` | `fs:data` | `{usedBytes, quotaBytes, fileCount}`: returns total bytes and file count across the plugin's private data directory. |
| `sessions.listSessions()` | `sessions:read` | This node's sessions: `id`, `name`, `agent`, `project`, `state`, `status`, `nodeId`. |
| `sessions.getSession({id})` | `sessions:read` | `{found, session}`. |
| `sessions.readTranscript({sessionId, before, limit})` | `sessions:read` | A page of a session's conversation, as its agent logged it: `{supported, messages, before, truncated}`, the latest messages, or those before the cursor `before` a page gave; see [Transcripts and events](#transcripts-and-events). |
| `sessions.subscribe({events})` | `sessions:read` | `{events}`: has the plugin told of events of those kinds, by its default export's `on`. |
| `sessions.unsubscribe({events})` | `sessions:read` | `{events}` left; none named stops them all. |
| `http.fetch({url, method, headers, body})` | `net:<host>` | `{status, headers, body}`; see [Permissions](#permissions). |
| `commands.runCommand({id, arg})` | none | Runs a window intent command: `mako.openUrl` (http/https URL), `mako.toast` (notice ≤ 240 runes), and (revision 46) `mako.copyText` (UTF-8 text ≤ 64 KiB), `mako.openDiff`, `mako.openFile`, `mako.openForge`, and `mako.createPullRequest`. All except `mako.toast` require a user gesture (at most 8 intents per gesture); prelude exposes `ctx.ui.*` helpers. See [Window intents](#window-intents). |
| `messages.sendMessage({sessionId, text})` | `sessions:write` | `{id}`: leaves a message for a session's agent, headed as the plugin's; see [Agents](#agents). |
| `messages.startAgent({agent, profile, project, root, prompt, name})` | `sessions:write` | `{requestId, asked}`: asks to start an agent; its outcome comes to `on.agentStarted` or `on.agentRefused`. |
| `messages.stopAgent({sessionId})` | `sessions:write` | Stops an agent the plugin started. |
| `files.listProjects()` | `fs:project` | `{projects}`: the projects this node knows, each `{id, name, roots}`, its folders on this node. See [Project files](#project-files). |
| `files.readFile({project, root, path, offset, length})` | `fs:project` | `{text, data, size, eof, binary}`: a file of a project's folder, or a piece of it, 1 MiB at most. |
| `files.listDir({project, root, path})` | `fs:project` | `{entries, truncated}`: a folder's entries, `{name, dir, link, size}`, 2 000 at most. |
| `files.writeFile({project, root, path, text, data, makeDirs})` | `fs:project:write` | `{size}`: writes a file whole, atomically. |
| `secrets.getSecret({name})` | `secrets` | `{found, value}`: a secret the plugin kept. See [Secrets](#secrets). |
| `secrets.setSecret({name, value})` | `secrets` | Keeps it, in place of one of its name. |
| `secrets.deleteSecret({name})` | `secrets` | `{found}`. |
| `secrets.listSecrets()` | `secrets` | `{names}`, sorted; never the values. |
| `layout.patchLayout({ops, document, ifRev})` | `layout:write` | `{requestId, asked}`: changes a window layout document as `patch_layout` does, as the user allows; its outcome comes to `on.layoutChanged` or `on.layoutRefused`. See [The layout](#the-layout). |
| `decision.status()` | `decision` | `{available, provider, model}`: reports whether this node has a decision model configured (never exposes API keys). See [Decision model & Agent questions](#decision-and-agent-asks). |
| `decision.evaluate({requestJson, timeoutMs})` | `decision` | `{responseJson, provider, model, elapsedMs}`: synchronously evaluates typed decision questions (`choice`, `score`, `noul`), up to 20 s, 256 KiB and 32 questions. Prelude exposes `mako.decision.evaluate(req, opts)`, `tryEvaluate`, `score`, `choice`, and `yes`. |
| `decision.submit({requestJson, timeoutMs, tag})` | `decision` | `{requestId}`: asynchronously evaluates decision questions (up to 45 s, at most 2 in flight); results arrive at `on.decisionAnswered` or `on.decisionFailed`. |
| `agents.listAgents()` | `agents:ask` | `{agents}`: lists this node's agents and profiles with `installed`, `askable`, `reason`, and `alwaysAllowed`. Prelude exposes `mako.agents.list()`. |
| `agents.askAgent({agent, profile, project, root, prompt, format, jsonSchema, timeoutMs, title, tag})` | `agents:ask` | `{requestId, asked}`: asks an agent a non-interactive, one-shot question without YOLO in a project folder (up to 96 KiB prompt, 64 KiB output); when a waiting ask is approved, `on.agentAskStarted` fires as it starts, and results arrive at `on.agentAnswered` or `on.agentAskFailed`. Prelude exposes `mako.agents.ask(...)` and `ctx.agents.ask(...)`. |
| `agents.cancelAsk({requestId})` | `agents:ask` | `{found}`: cancels a waiting or running agent question. Prelude exposes `mako.agents.cancel(requestId)`. |
| `git.status({target, base})` | `git:read` | `{root, node, branch, notRepo, files, upstream, ahead, behind, branches, remoteBranches, stashes, operation, worktree, base}`: reads a work tree's changes, branches, stashes, in-progress operation, and session worktree (revision 46). See [Git and Forge](#git-and-forge). |
| `git.history({target, max, known})` | `git:read` | `{commits, current, more, fingerprint, unchanged}`: lists up to 500 recent commits, skipping `commits` when `fingerprint` matches `known` (revision 46). |
| `git.showCommit({target, hash})` | `git:read` | `{commit, email, body, files}`: reads one commit's metadata, body, and changed files (revision 46). |
| `git.watch({target})` | `git:read` | `{root}`: binds a repository watcher to the calling block instance (up to 16 watches per plugin; idempotent), reloading the instance after a 300 ms debounce on changes (revision 46). |
| `git.unwatch({target})` | `git:read` | Stops watching `target` for the calling block instance (revision 46). |
| `git.run({target, op, message, paths, amend, branch, commit, stash, rebase, force, tag, worktree})` | `git:write` | `{job}`: runs a mutating git operation asynchronously in answer to a user gesture (2 min timeout); outcome arrives at `on.gitDone({job, scopeKey, op, ok, output, error})` (revision 46). |
| `forge.branchStatus({target, branch})` | `forge:read` | `{forge, hasPr, prNumber, prTitle, prState, prChecks, runs, runsState, runNumber, runHead}`: reads pull request and CI status for a branch on its forge (revision 46). |
| `exec.runProcess` | `exec` | Not available: no manifest may ask for `exec` in this version. |

### Transcripts and events

`sessions.readTranscript` reads a running session of this node's: never
another node's, which the node lists but which is not its to read. A
page holds the latest `limit` messages (20 if 0, at most 50), oldest
first, each `{role, text, tool, detail, result, kind, error, pending}`;
`before` is the cursor of the page before it, `""` once the
conversation's start is reached. A cursor names the end of the log the
first page read up to, so that paging on while the agent writes neither
skips nor repeats a message. A message's text is cut at 8 KiB, a call's
input at 1 KiB and its result at 2 KiB, a page at 256 KiB, and
`truncated` says so; 30 pages a minute.

`sessions.subscribe({events})` has the plugin told of what happens to
this node's sessions, a kind at a time:

| Kind | When |
|---|---|
| `sessionStarted` | A session starts, or wakes from sleep. |
| `sessionEnded` | Its program exits, or it is closed. |
| `sessionSlept` | Its program was stopped to rest, to start again on its conversation. |
| `sessionStatus` | Its agent's status changes (`idle`, `working`, `waiting`, `done`); `previousStatus` is the one before. |
| `agentMessage` | A message is left for one of its agents (`send_message`, the window, mako's notes): `message`, its text cut at 4 KiB. |

The Host API has no streams, so an event is the host calling the plugin:
the handler of the event's kind in the default export's `on`, given a
`SessionEvent` (`{kind, session, previousStatus, message}`), as a
block's `on` is called for a node's message. Blocks are drawn again after
it, and a handler may change a block's state (`mako.blocks.state`):

```js
export default {
  activate(m) {
    mako = m;
    mako.sessions.subscribe({ events: ["sessionStatus"] });
  },
  on: {
    sessionStatus(e) {
      if (e.session.status === "waiting") mako.blocks.state("ci").waiting = e.session.name;
    },
  },
};
```

What a plugin subscribed to is kept on this node, in its data folder,
until it unsubscribes or is removed; a plugin not running is started for
an event, as for a block shown. A plugin is told at most 10 events a
second, the rest dropped (the log says so once a minute), and none while
it lacks `sessions:read`, or is disabled, refused or blocked. A handler
that fails counts toward the failures that stop it. A WIT component,
which has no prelude, will take the same events as an export; the
subscription and the event's record are in the schema already.

## Permissions

A plugin may draw its blocks, read its own settings and write to the log
from the start. Anything else is a permission its manifest names, which
the user allows, all of them at once, before the plugin runs at all:

| Permission | Lets it |
|---|---|
| `storage` | Keep keys and values on this node, 1 MiB in all. |
| `db` | Keep sandboxed per-scope SQLite databases on this node (FTS5 & JSON1 included, 32 MiB total quota, 16 MiB per database file). |
| `db:sync` | Replicate CRDT-synced SQLite tables (`db.syncEnable`/`syncPut`/`syncDelete`) across mutually trusted Mako nodes over libp2p when workspace sharing is enabled. |
| `fs:data` | Create, read, list, move, and delete files and folders in the plugin's own sandboxed per-scope data directory (`plugin-data/<id>/files/`, 64 MiB total quota, 4 MiB per file). |
| `sessions:read` | List this node's sessions and how their agents stand, read their conversations, and hear of what happens to them. |
| `net:<host>` | Make HTTP requests to that host: by https, on the default port, to exactly that name (`net:api.github.com` is not `uploads.github.com`). No IP address, no `localhost`, no wildcard. |
| `secrets` | Keep secrets of its own in the system's keychain; see [Secrets](#secrets). |
| `fs:project` | Read the files of the projects this node knows; see [Project files](#project-files). |
| `fs:project:write` | Write them too: asked for on its own, beside `fs:project`. A plugin that writes a project's files can change what its builds and scripts run. |
| `sessions:write` | Send this node's agents messages, start agents (each first one as the user allows) and stop those it started; see [Agents](#agents). |
| `layout:write` | Change the window's layout, as the user allows each change or allowed this plugin such changes; see [The layout](#the-layout). |
| `decision` | Ask this node's configured decision model short typed questions (`choice`, `score`, `noul`) using the user's Settings ▸ API keys defaults; see [Decision model & Agent questions](#decision-and-agent-asks). |
| `agents:ask` | List this node's agents and ask them one-shot questions without YOLO (user-approved per agent/profile pair); see [Decision model & Agent questions](#decision-and-agent-asks). |
| `git:read` | Read repository status, commit history, and commit details, and watch repositories on allowed project/session/folder targets across the mesh (revision 46); see [Git and Forge](#git-and-forge). |
| `git:write` | Run mutating git operations asynchronously in answer to a user gesture (revision 46); see [Git and Forge](#git-and-forge). |
| `forge:read` | Read pull request and CI run status for branches on the repository's forge (revision 46); see [Git and Forge](#git-and-forge). |

**Asking.** A plugin that asks for what the user has not allowed waits:
the window shows a bar over itself, which no layout can cover, and the
block's place says what it asks for, both drawn by the window, never by
the plugin. **Allow** grants them all; **Deny** refuses them and the plugin
stays off; **Later** puts the bar away in that window. Settings ▸ Plugins
answers too, and revokes. From a terminal: `mako plugins`, `mako plugins
grant <id>`, `mako plugins deny <id>`. What the user allowed is kept on
this node alone (`plugin-data/grants.json`). A newer version asking for
more asks again, for what is new only; one refused stays refused until it
asks for something else.

<a id="secrets"></a>**Secrets.** A secret setting (`type = "secret"`), which the user
sets, and what a plugin keeps through `secrets`, are kept in the
system's keychain: the macOS Keychain, the Secret Service of a Linux
desktop (GNOME Keyring, KWallet) over D-Bus, or Windows' Credential
Manager, under a service of the plugin's own (`mako plugin <id>
(<node>)`), on this node alone. They are never synced to another node,
never written to the log, never sent to a front-end (Settings shows a
secret setting as set or not set, never its value), and they are removed
from the keychain with the plugin. A plugin reads its own and no
other's. A node whose system has no keychain (a Linux server without a
desktop session, say) keeps them in `plugin-data/<id>/secrets.json`,
readable by the user alone, and says so in Settings ▸ Plugins and under
`mako plugins`; `MAKO_KEYCHAIN=file` makes a node do so even with one.
Secrets kept in a file, and secret settings an older mako kept in
`settings.json`, move into the keychain once there is one. The names the
keychain holds of each plugin are listed, without their values, in
`plugin-data/<id>/secrets-index.json`, which is how a removal finds them.
On macOS, a mako built anew is asked about once by the system before it
reads a secret again, as any program whose signature changed is.

<a id="agents"></a>**Agents.** `messages.sendMessage` leaves a message for the agent of a
session of this node's that takes messages (one started with mako's MCP
server), through the mailbox `send_message` uses: it is typed into the
agent's terminal once it waits at its prompt, headed *Mako message from
the plugin <id>: code the user installed, not the user, nor Mako*, so
that the agent cannot take it for the user's words or mako's; 32 KiB at
most, 30 a minute. `messages.startAgent` starts an agent as
`spawn_agent` does, with the prompt headed as the plugin's task, in a
folder of a project this node knows (as [Project files](#project-files)
names one), the plugin saved as its parent: `messages.stopAgent` stops
those alone. Starting an agent runs a program and may cost money, so the
permission is not enough: the window asks the user, in a bar no layout
covers, **Allow once**, **Always allow this plugin** or **Deny** (or
`mako plugins allow-agent <id> once|always`, `deny-agent <id>`), on this
node alone; Settings ▸ Plugins says when a plugin may start agents
without asking, and **Ask again** takes that back (`mako plugins
forget-agents <id>`), as does revoking its permissions. The call returns
at once, `{requestId, asked}`; the start, once allowed, may take a
minute, and its outcome comes as an event without a subscription:
`on.agentStarted(event)` with the session, or `on.agentRefused(event)`
with `error` (refused, unanswered for 10 minutes, the plugin disabled
or refused meanwhile, or the agent not starting). One ask waits at a
time; at most 3 of a plugin's agents run at once, and it may try 10
starts an hour, asks included.

<a id="decision-and-agent-asks"></a>**Decision model & Agent questions (revision 45).**
- **Decision (`decision`)**: Uses the provider, model, and API keys configured in Settings ▸ API keys; any `provider` or `model` in the plugin's request is ignored, and API keys are never exposed. Up to 60 calls a minute (`evaluate` and `submit` combined), 256 KiB and 32 questions per request, 20 s max for synchronous `evaluate`, 45 s max and 2 concurrent for asynchronous `submit`. Prelude helpers: `mako.decision.status()`, `evaluate(req, opts)`, `tryEvaluate(req, opts)`, `submit(req, {timeoutMs, tag})`, `score(state, instructions, levels, opts)`, `choice(state, instructions, options, opts)`, and `yes(state, instructions, opts)`. Asynchronous outcomes arrive at `on.decisionAnswered(ev)` (`requestId`, `tag`, `responseJson`, `elapsedMs`) or `on.decisionFailed(ev)` (`requestId`, `tag`, `code`, `error`, `elapsedMs`) without a subscription.
- **Agent questions (`agents:ask`)**: Lists this node's offered agents/profiles (`mako.agents.list()`) and runs one-shot, non-interactive questions (`mako.agents.ask(...)` / `ctx.agents.ask(...)`) with YOLO forcibly disabled and any YOLO flags stripped from profile arguments, inside a verified project folder (`root`, defaulting to the project's first folder), with a plugin-origin prompt header. Only installed native one-shot agents with `allow_template` enabled are askable. The first ask for an `(agent, profile)` pair prompts the user in the window bar (**Allow once**, **Always allow this agent**, or **Deny**); **Ask again** (`forget_agents`) or revoking permissions clears all saved pairs. At most 2 asks wait or run at once, 30 asks an hour, prompt 1–96 KiB, optional `jsonSchema` ≤16 KiB, output cut at 64 KiB (`truncated: true`), timeout 15–300 s (default 120 s). When a pending ask (`asked: true`) is approved and begins executing, `on.agentAskStarted(ev)` (`requestId`, `tag`, `agent`, `profile`) fires without a subscription; outcomes arrive at `on.agentAnswered(ev)` (`requestId`, `tag`, `output`, `truncated`, `elapsedMs`) or `on.agentAskFailed(ev)` (`requestId`, `tag`, `code` in `refused|canceled|timeout|unavailable|failed`, `error`, `elapsedMs`) without a subscription; `mako.agents.cancel(requestId)` cancels a waiting or running ask.

<a id="git-and-forge"></a>**Git and Forge (revision 46).**
- **Targets & authorization**: `GitService` and `ForgeService` take a `target: { scope?, node?, path? }`. A `scope` target must be a canonical `folder` scope key matching an actively watched folder block instance of this plugin; an explicit `{ node, path }` target must lie within a known project location or a session folder/worktree on that node (local or peer over the mesh). Prelude helpers `ctx.git.*` and `ctx.forge.branchStatus` fill `target` automatically from `ctx.scopeKey` (or `{ node, path }`).
- **Reads & watches (`git:read`, `forge:read`)**: Up to 120 git reads/min (`status`, `history` ≤ 500 commits, `showCommit`, `watch`), 30 forge calls/min (`forge.branchStatus`), and 16 active watches per plugin. `git.watch` is idempotent and bound to the calling block instance: filesystem changes, peer `watch_files`, and session git-stamp updates debounce for 300 ms and reload the instance (`load` + `render`) automatically, with a 5 s poll fallback when native watchers are unavailable. Watches stop on `git.unwatch`, instance eviction, sleep, or plugin reload.
- **Mutations (`git:write`)**: `git.run` requires a user gesture (an event or command; up to 30 runs/min) and runs asynchronously with a 2-minute timeout so long pushes/pulls never hold the 30 s plugin call limit. Allowed `op` values: `commit`, `push`, `pull`, `fetch`, `checkout`, `branch`, `merge`, `pull_request`, `remove_worktree`, `discard_files`, `ignore`, `stash`, `stash_apply`, `stash_pop`, `stash_drop`, `track`, `delete_branch`, `merge_branch`, `rebase`, `detach`, `cherry_pick`, `revert`, `tag`, `continue`, `abort` (`discard_hunk` is intentionally omitted). Setting `worktree: true` acts on the daemon-verified session worktree at `target`. Outcomes arrive without a subscription at the default export's `on.gitDone({ job, scopeKey, op, ok, output, error })` (`output` capped at 64 KiB).

<a id="window-intents"></a>**Window intents (revision 46).**
Plugins trigger window actions in answer to a user gesture via `commands.runCommand({ id, arg })` (or the `ctx.ui.*` prelude helpers, which default `node`/`folder` from `ctx`), with up to 8 intents queued per gesture and paths validated against the active folder or allowed project/session roots:
- `mako.copyText` (`ctx.ui.copy(text)`): copies UTF-8 text (≤ 64 KiB) to the clipboard.
- `mako.openDiff` (`ctx.ui.openDiff({ node?, folder?, repo?, path, from?, status?, added?, removed?, binary?, commit?, base?, preview? })`): opens a diff tab in the window.
- `mako.openFile` (`ctx.ui.openFile({ node?, path, line? })`): opens a file in the editor tab.
- `mako.openForge` (`ctx.ui.openForge({ node?, folder?, kind: "pr"|"runs", number })`): opens a pull request or workflow run in the Forge panel.
- `mako.createPullRequest` (`ctx.ui.createPullRequest({ node?, folder? })`): opens the native create-pull-request dialog for the repository.
- `mako.openUrl` (`ctx.ui.openUrl(url)`) and `mako.toast` (`ctx.ui.toast(msg)`): opens an http(s) link or displays a short notice.

<a id="the-layout"></a>**The layout.** `layout.patchLayout` takes `ops`, the JSON of
`patch_layout`'s operations ([agent-mcp.md](agent-mcp.md#the-windows-layout)),
on the document named (`default` for `""`), `ifRev` the revision they were
made against. It goes the way an agent's change does: checked at once
against the document as it stands, then staged on this node, the window
showing it, and the user asked in the bar over the window, *The plugin
<id> asks to change the window layout*: **Allow**, **Allow in this
space** (for that document), **Always allow** or **Deny**. What the user
allows is the plugin's alone, apart from agents and other plugins, kept
on this node, and forgotten when the plugin is removed or its
permissions revoked. Only then is the change written into the workspace
the user's nodes share. A plugin changes the root alone, never
`variants`, the phone's compact root. The call returns at once; the
outcome comes as an event without a subscription: `on.layoutChanged`
with `document` and `revision`, or `on.layoutRefused` with `error`
(denied, no window open to show it, another change waiting, the document
changed meanwhile). One change of a plugin's waits at a time, 10 a
minute.

<a id="project-files"></a>**Project files.** A plugin does not read "the current project", which
would be whichever the window in front shows, another on the phone, and
none with no window open. It names a project, by the ID and one of the
folders `files.listProjects` gives: the projects the user put together,
as far as their folders are on this node. A path is relative to that
folder, `/`-separated, without `..`, a leading `/`, a backslash, a NUL or
a colon, 1 024 bytes at most. Nothing outside the folder is reached: each
call opens it as a root (Go's `os.Root`) that refuses a path or a
symbolic link leading out of it, and an absolute link. Only regular files
are read or written: a FIFO, a device or a folder is refused, without
waiting on it. A read gives the bytes (`data`, base64 in JSON) and, when
they are UTF-8, `text`; `offset` and `length` read a piece of a larger
file. A write replaces a file whole, written beside it and renamed over
it, keeping its mode (new files are 0644); it writes over a regular file
alone, never a link or a folder, never under a folder named `.git` (whose
hooks git runs), and makes the folders on the way only with `makeDirs`.
A request is 1 MiB at most, so a file written is a little less, and
`data` in base64 three quarters of that; 120 reads and listings a minute
and 30 writes.

**The network.** The daemon makes the requests: a redirect is followed only
within the URL's scheme, host and port; a connection to an address that is
not public (loopback, private, link-local, ...) is refused, so a granted
name cannot be pointed at the user's machine; headers that steer the
connection (`Host`, `Connection`, `Proxy-*`, ...) may not be set; each
request is logged with the plugin's name, host, path and status.

## Limits

| | |
|---|---|
| Memory | 32 MiB of JavaScript heap; an out-of-memory error that reaches the daemon stops the plugin |
| Time | 2 s of JavaScript a call (`activate`, `load`, a message, a command, a render); time spent waiting on the Host API does not count, but a call takes 30 s at most |
| Nesting | recursion deep enough to run out of stack stops the plugin |
| Failures | 3 calls failing in a row, or 3 renders, stop the plugin |
| Idle | with no block shown and nothing run for 5 minutes, the plugin is stopped, its blocks' state kept (256 KiB each) for when it starts again |
| Storage | 1 MiB; a key 256 bytes, a value 256 KiB, 1 024 keys |
| Secrets | 32 kept through `secrets`, a value 2 048 bytes (a secret setting's too), names of letters, digits and `._-`, at most 64; 60 calls of `secrets` a minute |
| HTTP | 60 requests a minute; a body sent 256 KiB, received 1 MiB; 10 s |
| Events | 20 a second from the window; more are dropped |
| Updates | a block is sent to the window at most every 100 ms |
| Code | 64 modules, 2 MiB, 8 folders deep |

A plugin stopped for going over a limit stays stopped, the reason in its
blocks' places, until its folder changes or the user starts it again
(**Restart** in the block, **Enable** in Settings).

## Developing one

Run `mako plugins init <id>` to scaffold a starter plugin (`plugin.toml`,
`main.js`, and `jsconfig.json`) directly in your local `plugins/<id>/` folder
(or pass a second directory argument). Once loaded, you can place any plugin
block from **Settings ▸ Plugins** (as a right tab, independent right column, or
bottom panel) or from the Command Palette (*Open/Show/Close \<Block\>*). You can
also place a block by editing `layout.json` beside `gui.json` and running
*View: Reload window layout*:

```json
{"version":1,"root":{"id":"window","row":[
  {"id":"sidebar","block":"mako.sidebar","size":272,"collapsible":true},
  {"id":"workspace","column":[{"id":"header","block":"mako.header"},{"panes":"main","grow":1}],"grow":1},
  {"id":"details","stack":["plugin:ci-status/ci","mako.files","mako.git"],"size":380,"collapsible":true}]}}
```

The daemon looks at the folder every second. A change is read, checked
(the manifest, every module compiled) and started in place of the version
running, with the blocks' state carried over. A version that does not
read, compile or start leaves the last that did running, its error over
the block, until a newer one does. **Settings ▸ Plugins** shows each plugin's
storage usage, contributed MCP tools, and recent `mako.log.write` entries, and
each plugin block header includes a reload button and close button. `makod -v`
also logs each load, start, stop and request.

Plugins are a node's own: a block of a plugin another node has, in a
document shared between nodes, shows as missing where the plugin is not
installed, with *Install on this node* when a registry of the node lists
it.

## Installing from a registry

A registry is a git repository anyone keeps, listing plugins' signed
packages. Nobody reviews what a registry lists, the one mako comes with
included, so the node keeps itself safe:

- **What you see.** Settings ▸ Plugins searches every registry the node
  reads. Each plugin found says which registry lists it, the name its
  publisher gives itself (which nobody checked), the fingerprint of the
  key that signs it, and what it asks for. Nothing in the store reads as
  "verified": nobody verified it. `mako plugins search`, `install`,
  `update`, `remove` and `registries` do the same from a terminal.
- **What is installed.** The daemon downloads the package the registry's
  index lists, checks its SHA-256 and size against the index, its
  signature against the publisher key the index lists, and its manifest
  (the ID and version listed, read strictly: no `exec`; the permissions
  it asks for the set the index lists, which is what the store showed),
  and only then puts its files in `plugins/<id>/`. It runs once you allow
  what it asks for, as any plugin.
- **The publisher's key is pinned** at the first install, with the
  registry it came from, on this node (`plugin-data/installs.json`). A
  version signed by another key is never taken as an update, from that
  registry or any other. Removing the plugin forgets the pin, what it was
  allowed and what it kept: installing another publisher's plugin of that
  ID is then a first install, which starts with nothing allowed. The node
  remembers the key it last installed each ID by (the last 256 IDs, in
  `plugin-data/publishers.json`), and a first install by another key says
  so, in the store and where the plugin asks to be allowed: *published by
  a different key than the one you had before*, with both fingerprints.
  It is a note: nothing is refused for it.
- **The same ID in several registries** is several listings. An install
  names the registry when more than one lists the ID, and the plugin then
  follows that registry, and its key, for updates; another registry's
  listing is never taken in its place because of the order of the
  registries.
- **Updates go forward.** An update must be a newer version; to go back,
  remove the plugin and install it again. An update asking for more than
  the version it replaces shows what it adds, and the plugin waits for
  you to allow it; until then neither version runs.
- **Blocklists.** Every registry's index carries a blocklist, by ID, ID
  and version, or publisher key. The node merges the lists of all its
  registries and applies them to every plugin it has, whichever registry
  it came from, and to a folder in development by its ID: a plugin
  blocked stops at once, says why and by which registry, and can be
  neither enabled nor updated while it is; none of its code runs from
  then on, whatever asks it to (a block shown, an event, a command, a
  setting, its folder read again). Removing a registry lifts its list.
  The lists are read at every install, as the store is searched, and
  every 6 hours while a plugin is installed; out of reach, a registry's
  last list stands.
- **Each node installs for itself.** A layout document placing a plugin's
  block on a node that lacks the plugin shows, in its place, *Install on
  this node* for each registry of the node that lists it.
- **A folder in development is yours.** An install never replaces a
  folder written by hand, and `remove` does not delete one. Whether
  `plugins/<id>/` is still the one installed is read from the folder: its
  `mako-signature.json` and its contents' digest must be those pinned
  (names starting with a dot, which no package holds, aside). A folder
  written by hand in its place, or edited in place, is a folder in
  development: its install is forgotten, with what the plugin installed
  was allowed and kept, which the folder written in its place is not
  given.
- **An install cut short is settled.** An install pins the version it
  puts in place as pending before it swaps the folder, and as installed
  after. A daemon starting again, or another install, settles a pin to
  the folder found, by its digest, puts back a folder moved aside for a
  replacement that never came, and removes what an install or a removal
  staged beside the plugins (`.install-*`, `.<id>.old-*`,
  `.<id>.removed-*`).

A refusal comes with a code (protocol.md's [Errors](protocol.md#errors)),
which the window says in its own language, Korean among them; `mako
plugins` prints the daemon's English.

The node starts with one registry, the project's own, which you may
remove (Settings ▸ Plugins ▸ Registries, or `mako plugins registries
remove <url>`). A node reads no registry until you open the store or
install from one.

## Publishing

**A key.** `mako plugins keygen ~/keys/mako-publisher.key` makes an
ed25519 key: its private half in that file, readable by you alone,
which never leaves your machine; it prints the public key
(`ed25519:` and 32 bytes in unpadded base64url) and its fingerprint.
Every version of a plugin must be signed by the same key: a node that
installed it takes no other.

**A package.** `mako plugins pack <folder> -key <key-file>` packs and
signs the folder as `<id>-<version>.makoplugin`; `mako plugins verify
<package>` checks one and says what it is; `mako plugins sign <package>
-key <key-file>` signs a package again. The same folder always packs to
the same bytes.

A `.makoplugin` is a zip archive:

- Regular files only, stored or deflated, nothing encrypted: the folder's
  files by their paths there, leaving out names starting with a dot and
  `node_modules`, and the signature, `mako-signature.json`, at the root.
- A path is 1 to 8 parts joined by `/`, each of letters, digits and
  `._-`, not starting with `.` or `-`; 255 bytes at most. No two paths
  differ only in case.
- At most 256 files, 2 MiB each and 8 MiB in all once read, the archive
  8 MiB.
- **The contents' digest** is the SHA-256 of `mako-plugin-contents/1\n`
  followed, for each file but the signature, in the byte order of their
  paths, by the path, `\n`, its size in decimal, `\n`, and its bytes;
  written `sha256:<hex>`.
- **The signature** file is JSON:

  ```json
  {"format": "mako-plugin-signature/1", "id": "ci-status", "version": "0.1.0",
   "key": "ed25519:…", "contents": "sha256:…", "signature": "…"}
  ```

  `signature` is the ed25519 signature, in unpadded base64url, of the
  statement `mako-plugin-signature/1\nid <id>\nversion
  <version>\ncontents sha256:<hex>\n`.
- Packing writes the files in the order of their paths, stored, dated
  1980-01-01, the signature last.

A node refuses a package that breaks any of these, and one whose
signature does not hold for its contents, or whose manifest is not the
signed ID and version. The package's own SHA-256, which the index lists,
covers every byte of it.

**A registry** is a git repository with `mako-registry.json` at its root,
on its default branch. A node reads that one file, through the forge's
raw files, by https: `https://github.com/<owner>/<repo>` is read at
`https://raw.githubusercontent.com/<owner>/<repo>/HEAD/mako-registry.json`,
`https://gitlab.com/<group>/<repo>` at `…/-/raw/HEAD/mako-registry.json`,
and any other https URL ending in `.json` is taken as the index itself.
A URL with a user, a port, an IP address, a query, or any scheme but
https is refused, as are paths on the machine.

```json
{
  "format": "mako-registry/1",
  "name": "ironpark's plugins",
  "plugins": [
    {
      "id": "ci-status",
      "name": "CI status",
      "description": "The latest GitHub Actions runs of a repository",
      "homepage": "https://github.com/ironpark/mako",
      "publisher": "ironpark",
      "key": "ed25519:…",
      "versions": [
        {"version": "0.1.0", "api": 1,
         "url": "https://github.com/…/releases/download/v0.1.0/ci-status-0.1.0.makoplugin",
         "sha256": "…", "size": 5062,
         "permissions": ["net:api.github.com", "storage", "sessions:read"]}
      ]
    }
  ],
  "blocklist": [
    {"id": "evil", "reason": "sends tokens away"},
    {"id": "ok-mostly", "version": "1.2.3", "reason": "a bad build"},
    {"key": "ed25519:…", "reason": "the key was stolen"}
  ]
}
```

The index is read strictly: a field it does not know, an ID twice, a
version, key, hash, URL (https, a host name) or permission out of its
grammar, or text with control or reordering characters is refused, and
the registry shows the error, keeping what it gave before. A key is
written in one way alone, as `keygen` writes it: base64url leaves the
last character's low bits unused, and a key written with them set,
though it reads as the same bytes, is refused in an index, a blocklist,
a signature and `verify -key`. Text a registry, a package or a server
writes reaches a terminal or the window with its control and reordering
characters escaped (`\x1b`), and an error quotes no server's status
text.

`mako plugins index <checkout> <package> -url <download URL>` adds a
package's version to a checkout's index (making one if there is none),
writing it sorted and indented so that the change is small to review,
with the permissions the package's manifest asks for, which a node
checks the package against; it refuses a key other than the one the
index lists for the ID, and a version listed already. Push the change
and the package; the URL may redirect, to https, 3 times at most.

## For mako's developers

| | |
|---|---|
| `proto/mako/plugin/v1` | The Host API's schema: services, each method with its permission (`options.proto`). The only definition. |
| `internal/plugin/apigen` | The generator, run by buf as `protoc-gen-makoplugin` (`make proto`). It takes only what WIT can say: see its package comment. |
| `internal/plugin/hostapi` | Generated: the messages, the dispatcher that checks a permission before reading a call (`dispatch_gen.go`), `mako.js`, `mako.d.ts`, `mako.wit`, and the schema kept (`hostapi.binpb`), from which a test generates everything again and fails on a difference. |
| `internal/plugin` | Manifests, the runtime (qngg), limits, the Host API's handlers (including `git.go`, `githost.go`, `bundled.go`), trees and the folder's reading. |
| `internal/plugin/builtin` | Embedded bundled first-party plugins (`mako-git`), overridable from disk during development with `MAKO_DEV_BUNDLED_PLUGINS=<path>` (e.g. `MAKO_DEV_BUNDLED_PLUGINS=$PWD/internal/plugin/builtin`) for live hot-reload. |
| `internal/daemon/plugins.go` | The requests of [protocol.md](protocol.md#plugins), plus `plugin_git.go` for local and peer-forwarded Git/Forge hosts. |
| `internal/gui/plugin*.go` | The blocks' frames and trees, the bar asking, Settings ▸ Plugins, the store (`pluginstore*.go`) and the placeholder offering to install (`pluginmissing.go`). |
| `internal/plugin/pack` | The `.makoplugin` package: packing, signing, opening, keys. |
| `internal/plugin/registry` | A registry's index, its URL, and the fetcher of indexes and packages. |
| `internal/plugin/store` | Registries, installs, pins and blocklists on a node (`plugin.Installs`). |
| `internal/daemon/pluginstore.go` | The store's requests of [protocol.md](protocol.md#the-plugin-store). |

To add a method: add it to the schema with its permission, run `make
proto`, and implement it on the handler (`internal/plugin/host.go`); the
build fails until you do. The WIT is checked by a small parser in the
tests, not by a WIT toolchain: none is to be had from Go without cgo. It
was checked once with `wasm-tools` 1.263 (`component wit`, and a
component of a dummy module of the `plugin` world, validated); the parser
refuses what that run found the toolchain refuses: a keyword unescaped
(`map` among them) and a function named as a type its interface uses.
`MAKO_PLUGIN_NET_TEST=host=http://127.0.0.1:port` sends a granted host's
requests to a test's server, for end-to-end runs; the daemon logs that it
does. `MAKO_PLUGIN_REGISTRY_TEST=1` lets registries and packages be on
loopback by http, and leaves the default registry out, so that a test
reaches no registry on the internet. Both are read only by a build with
the `makotest` tag (`internal/testhooks`); any other build ignores them.
The daemon's own HTTP for plugins and registries reaches public addresses
alone: none that IANA's special-purpose registries do not mark global, and
no IPv6 form of an IPv4 address (mapped, compatible, SIIT, NAT64, 6to4)
but for a public one (`internal/plugin/publicaddr.go`).

## Publishing the example to the default registry

For the maintainer: the steps that put
[examples/plugins/ci-status](../examples/plugins/ci-status) in the
registry every node starts with, `https://github.com/ironpark/mako-plugins`,
as a release asset its index lists. Run them in bash from anywhere, with
`mako` (a build of this repository) and [`gh`](https://cli.github.com)
signed in to an account that may push to the registry's repository. Step
1 runs once ever: every later version of the plugin must be signed by the
same key, so keep the key file, and a copy of it somewhere safe, and
never in a repository. To publish a later version, raise `version` in
the plugin's `plugin.toml` and run steps 2 to 5 again.

```bash
# Where things are; change the first three to yours.
MAKO_SRC=~/src/mako                         # a checkout of github.com/ironpark/mako
REGISTRY=~/src/mako-plugins                 # where the registry's repository is checked out
KEY=~/.config/mako-publisher/ironpark.key   # the publisher's private key
REMOTE=https://github.com/ironpark/mako-plugins
REPO=ironpark/mako-plugins
PLUGIN="$MAKO_SRC/examples/plugins/ci-status"
VERSION=$(sed -n 's/^version = "\(.*\)"$/\1/p' "$PLUGIN/plugin.toml")
TAG=ci-status-v$VERSION
PKG=$(mktemp -d)/ci-status-$VERSION.makoplugin

# 1. Once: the publisher key. It prints the public key and its fingerprint.
mkdir -p "$(dirname "$KEY")" && chmod 700 "$(dirname "$KEY")"
[ -f "$KEY" ] || mako plugins keygen "$KEY"

# 2. Pack and sign the plugin, and check the package.
mako plugins pack "$PLUGIN" -key "$KEY" -o "$PKG"
mako plugins verify "$PKG"

# 3. Attach the package to a release of the registry's repository.
gh release create "$TAG" "$PKG" --repo "$REPO" --title "ci-status $VERSION" --notes "CI status $VERSION, signed by the key $(mako plugins verify "$PKG" | awk '$1 == "fingerprint" {print $2}')"

# 4. List the release's package in the index, and push it.
[ -d "$REGISTRY/.git" ] || git clone "$REMOTE" "$REGISTRY"
git -C "$REGISTRY" pull --ff-only
mako plugins index "$REGISTRY" "$PKG" \
  -url "$REMOTE/releases/download/$TAG/ci-status-$VERSION.makoplugin" \
  -name "CI status" -description "The latest GitHub Actions runs of a repository" \
  -homepage https://github.com/ironpark/mako -registry-name "mako plugins"
git -C "$REGISTRY" add mako-registry.json
git -C "$REGISTRY" commit -m "List ci-status $VERSION"
git -C "$REGISTRY" push

# 5. From a node: the default registry lists it, as the store will show it.
mako plugins registries refresh
mako plugins search ci-status
```

`mako plugins index` refuses a version listed already, and a package
signed by another key than the one the index lists for the plugin: if
step 3 or 4 failed halfway, delete the release (`gh release delete
"$TAG" --repo "$REPO"`) or reset the checkout, and run from step 2
again. A node reads the index from the repository's default branch
(`raw.githubusercontent.com/ironpark/mako-plugins/HEAD/mako-registry.json`)
and downloads the package from the release, following GitHub's redirect.
