# Mako Plugin UI Component Catalog & Validation Rules (Revision 46)

All trees are validated strictly by `internal/uiblock`. Raw hex colors are forbidden;
use tone tokens: `""`, `"muted"`, `"faint"`, `"accent"`, `"success"`, `"warning"`, `"danger"`.

## 1. Layout & Containers

- `ui.column(children, { gap, fill, ... })` / `ui.row(children, { gap, fill, variant, ... })` / `ui.stack(children)`
  - **`fill: true`** (root `column`/`row`/`split` ONLY): skips the outer frame scroll so you can pin a sticky top toolbar and scroll an inner `ui.scroll([...], { grow: 1 })`!
- `ui.toolbar(children, opts)` — shorthand for `ui.row(children, { variant: "toolbar", ... })` (Revision 46). Lifted into the window's chrome when it is the root's first child; drawn inline elsewhere.
- `ui.wrap(children, { gap })` — flex-wrap container (use for tags, chips, and link pills so they never clip horizontally).
- `ui.grid(columns, children, { gap })` — 1..6 equal columns per row (great for KPI stat cards).
- `ui.split(first, second, { direction: "horizontal"|"vertical"|"auto", ratio: 0.35 })` — resizable 2-pane split (`ratio` 0.1..0.9; `direction: "auto"` switches between horizontal at ≥ 640 px and vertical below, Revision 46).
- `ui.card(children, { gap, tone, variant, onTap, drag, dragGroup, onDrop, dropAccept })` — `variant`: `"" | "primary" | "secondary" | "outline" | "ghost" | "danger" | "soft"`.
- `ui.scroll(children, { direction: "vertical"|"horizontal", maxHeight, grow, gap, variant })` — `variant: "follow"` pins scroll to the end (Revision 45).
- `ui.list(items, (item, i) => node, { gap })` / `ui.table(headers, rowNodes)` (every table child must be `ui.row`).

## 2. Navigation, Menus, Dialogs & Disclosure

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

## 3. Controls, Text & Markdown

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

## 4. Visualization

- `ui.chart(type, series, { key, caption, labels })`:
  - `type`: `"line"`, `"bar"`, `"sparkline"`, `"donut"`, `"pie"` (`donut`/`pie` added in Revision 44).
  - Each series: `{ name, tone, values: [...] }`. For `"donut"` / `"pie"`: 1 series, 1..16 non-negative finite values matching `labels.length` (labels ≤ 32 UTF-8 bytes!).
- `ui.graph(nodes, edges, { key, caption, value, maxHeight, onChange })` (Revision 44):
  - Interactive force-directed canvas (pan/zoom + click hit-test firing `onChange` with `node.id`!).
  - `nodes`: `[{ id, label, group?, tone?, subtitle?, weight? }]` (≤ 120 unique IDs, positive finite weight).
  - `edges`: `[{ from, to, label?, tone?, dashed? }]` (≤ 400 edges; `from`/`to` must exist in `nodes`).
  - `maxHeight`: 120..1200 px.
- `ui.dag(rows, { key, value, contextMenu, maxHeight, onChange })` (Revision 46):
  - Virtualized commit/DAG graph with topological lanes and Bezier merges (up to 1 000 rows, keeping trees well under the 2 000-node limit!).
  - `rows`: `[{ id, parents, text, detail?, meta?, badges?, tone?, strong?, icon? }]` (unique `id` ≤ 64 chars, ≤ 8 parents, `text`/`detail` ≤ 320 chars, `meta` ≤ 128 chars, ≤ 6 badges per row).
  - `value`: selected row `id`; `onChange` fires with clicked row `id`; `contextMenu` opens with `row.id` as the context argument; `maxHeight` up to 4 000 px.

## 5. Common Pitfalls & Validation Rules

- **Keys**: must NOT contain `/` or `#`, and must be ≤ 128 bytes. Always sanitize paths with a `safeKey(prefix, raw)` helper.
- **Form values**: inside `on` submit handlers (including dialog confirm buttons), read live values via `mako.blocks.values("<blockId>")` (keyed by node key) because users may click Save without blurring an input first.
- **Context menus**: declare `ui.contextMenu(key, items)` once in the tree (unique `key`, never nested inside another menu) and reference it by `key` from rows/items instead of repeating menus per row; unknown `contextMenu` references are rejected.
- **Dialogs & DAGs**: at most one `ui.dialog` per tree; use `ui.dag` (data-driven rows) instead of building hundreds of row nodes to stay within the 2 000-node / 512 KiB tree JSON limits.
- **Gesture-only calls**: `ctx.git.run(...)` and `ctx.ui.*` (except `toast`) only succeed inside user-triggered event or command handlers, never inside `load()` or `render()`.
- **Chart/Graph byte limits**: truncate labels by UTF-8 byte length (`<= 32` bytes for chart labels; `<= 32` runes for badges), not just JS string `.length` (Korean characters are 3 bytes each in UTF-8!).
- **Root `fill: true`**: always pair `fill: true` with an inner `ui.scroll([...], { grow: 1 })` or `ui.split(..., { grow: 1 })` so tall content scrolls cleanly below your sticky header.
- **No external images**: `ui.markdown` blocks remote images for security; never depend on external image URLs.
<!-- mako-managed-skill -->
