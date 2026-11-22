package apigen

import (
	"fmt"
	"strings"
)

// genJS writes the bindings: hostApi(call) builds mako.<group>.<method>,
// each calling call(name, request), which the runtime's prelude makes a
// call into the daemon (internal/plugin/prelude.js). Nothing is checked
// here: the daemon checks every call, so the bindings are a convenience
// and no boundary.
func genJS(a *api) []byte {
	var b strings.Builder
	b.WriteString("// " + header + "\n\n")
	b.WriteString("// hostApi builds the Host API's bindings over call(method, request), which\n")
	b.WriteString("// returns a promise of the method's response.\n")
	b.WriteString("export function hostApi(call) {\n  return Object.freeze({\n")
	for _, s := range a.services {
		fmt.Fprintf(&b, "    %s: Object.freeze({\n", s.js)
		for _, m := range s.methods {
			fmt.Fprintf(&b, "      %s: (req) => call(%q, req),\n", m.js, m.key())
		}
		b.WriteString("    }),\n")
	}
	b.WriteString("  });\n}\n")
	return []byte(b.String())
}

// genDTS writes the types of the bindings, after what a plugin module,
// its blocks and the UI vocabulary are (dtsPreamble).
func genDTS(a *api) []byte {
	var b strings.Builder
	b.WriteString("// " + header + "\n")
	b.WriteString(dtsPreamble)
	b.WriteString("\n// The Host API, from proto/mako/plugin/v1. A method that needs a permission\n")
	b.WriteString("// fails unless the plugin's manifest asks for it and the user allowed it.\n\n")
	b.WriteString("export interface HostApi {\n")
	for _, s := range a.services {
		prefixed(&b, "  ", "/** ", oneLine(s.doc))
		fmt.Fprintf(&b, "  readonly %s: %sApi;\n", s.js, s.short)
	}
	b.WriteString("}\n")
	for _, s := range a.services {
		b.WriteString("\n")
		jsdoc(&b, "", s.doc)
		fmt.Fprintf(&b, "export interface %sApi {\n", s.short)
		for _, m := range s.methods {
			doc := m.doc
			need := "Needs no permission."
			switch {
			case m.perm == "net":
				need = "Needs net:<host> for the URL's host."
			case m.perm != PermNone:
				need = "Needs " + m.perm + "."
			}
			if m.unavailable {
				need += " Not available in this version of mako: it fails."
			}
			if doc != "" {
				doc += "\n\n"
			}
			jsdoc(&b, "  ", doc+need)
			fmt.Fprintf(&b, "  %s(req?: %s): Promise<%s>;\n", m.js, m.in.name, m.out.name)
		}
		b.WriteString("}\n")
	}
	for _, e := range a.enums {
		b.WriteString("\n")
		jsdoc(&b, "", e.doc)
		quoted := make([]string, len(e.values))
		for i, v := range e.values {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		fmt.Fprintf(&b, "export type %s = %s;\n", e.name, strings.Join(quoted, " | "))
	}
	for _, m := range a.messages {
		b.WriteString("\n")
		jsdoc(&b, "", m.doc)
		fmt.Fprintf(&b, "export interface %s {\n", m.name)
		for _, f := range m.fields {
			jsdoc(&b, "  ", f.doc)
			fmt.Fprintf(&b, "  %s?: %s;\n", f.json, tsType(f))
		}
		b.WriteString("}\n")
	}
	return []byte(b.String())
}

// tsType is a field's TypeScript type, as protojson writes it.
func tsType(f *field) string {
	if f.mapVal != nil {
		return "Record<string, " + tsType(f.mapVal) + ">"
	}
	var t string
	switch {
	case f.msg != nil:
		t = f.msg.name
	case f.enum != nil:
		t = f.enum.name
	case f.scalar == "string":
		t = "string"
	case f.scalar == "bytes":
		t = "string /* base64 */"
	case f.scalar == "bool":
		t = "boolean"
	default:
		t = "number"
	}
	if f.repeated {
		return t + "[]"
	}
	if f.msg != nil {
		return t + " | null"
	}
	return t
}

func jsdoc(b *strings.Builder, indent, doc string) {
	if doc == "" {
		return
	}
	lines := docLines(doc)
	if len(lines) == 1 {
		b.WriteString(indent + "/** " + lines[0] + " */\n")
		return
	}
	b.WriteString(indent + "/**\n")
	prefixed(b, indent, " * ", doc)
	b.WriteString(indent + " */\n")
}

// oneLine is a doc's first sentence or line, for a property's comment.
func oneLine(doc string) string {
	lines := docLines(doc)
	if len(lines) == 0 {
		return ""
	}
	return lines[0] + " */"
}

// dtsPreamble is what the schema does not say: a plugin's module, its
// blocks and commands, and the UI vocabulary, which the runtime's prelude
// carries out and internal/plugin/uitree checks.
const dtsPreamble = `//
// The types of a mako plugin's JavaScript: its module, what it is given,
// and the Host API. See docs/plugins.md.

/** A plugin's entry module exports a plugin as its default. */
export interface Plugin {
  /** Runs when the plugin starts: register the blocks and commands here. */
  activate(mako: Mako): void | Promise<void>;
  /**
   * The handlers of the events the plugin subscribed to with
   * mako.sessions.subscribe, by kind: the daemon calls the one of an
   * event's kind, starting the plugin for it if it is not running.
   */
  on?: PluginEvents;
}

/** What a plugin does on each kind of event it subscribed to. */
export interface PluginEvents {
  sessionStarted?(event: SessionEvent): void | Promise<void>;
  sessionEnded?(event: SessionEvent): void | Promise<void>;
  sessionSlept?(event: SessionEvent): void | Promise<void>;
  sessionStatus?(event: SessionEvent): void | Promise<void>;
  agentMessage?(event: SessionEvent): void | Promise<void>;
  /** Fired when CRDT-synced SQLite rows arrive from a trusted peer node. */
  dbSync?(event: DbSyncEvent): void | Promise<void>;
}

/** Event delivered to on.dbSync when remote peers merge synced SQLite rows. */
export interface DbSyncEvent {
  readonly scope: PluginScope;
  readonly scopeKey: string;
  readonly tables: readonly string[];
  readonly rows: number;
}

/** Nested or flat map of translation keys to template strings. */
export type I18nMessages = { readonly [key: string]: string | I18nMessages };

/** Plugin localization helper exposed on mako.i18n. */
export interface I18nApi {
  /** Registers or merges locale catalogs keyed by BCP-47 tag (e.g. "en", "ko", "ko-KR"). */
  register(catalog: Record<string, I18nMessages>): void;
  /** Looks up key with fallback (exact tag -> primary subtag -> "en" -> key) and replaces {name} placeholders. */
  t(key: string, params?: Record<string, string | number | boolean>, locale?: string): string;
}

/** What activate is given: the Host API, and the plugin's blocks, commands, tools, and i18n. */
export interface Mako extends HostApi {
  readonly blocks: Blocks;
  readonly commands: CommandsApi & Commands;
  readonly tools: Tools;
  readonly i18n: I18nApi;
}

/** Declared scope kind of a plugin block, tool, or setting. */
export type PluginScope = "global" | "space" | "project" | "session";

/** Scoped storage helper bound to a ScopeContext's scopeKey. */
export interface ScopedStorageApi {
  getItem(key: string): Promise<GetItemResponse>;
  setItem(key: string, value: string): Promise<SetItemResponse>;
  deleteItem(key: string): Promise<DeleteItemResponse>;
  listKeys(prefix?: string): Promise<ListKeysResponse>;
}

/** Scoped settings helper bound to a ScopeContext's scopeKey. */
export interface ScopedSettingsApi {
  getSetting(key: string): Promise<GetSettingResponse>;
  listSettings(): Promise<ListSettingsResponse>;
}

/** Query result row object mapped from columns and JSON cell values. */
export interface ScopedDbQueryResult {
  readonly columns: readonly string[];
  readonly rows: readonly Record<string, any>[];
  readonly truncated: boolean;
}

/** Scoped SQLite and CRDT sync helper bound to a ScopeContext's scopeKey. */
export interface ScopedDbApi {
  query(sql: string, params?: readonly any[]): Promise<ScopedDbQueryResult>;
  exec(sql: string, params?: readonly any[]): Promise<ExecResponse>;
  batch(statements: readonly { sql: string; params?: readonly any[] }[]): Promise<BatchResponse>;
  stats(): Promise<StatsResponse>;
  syncEnable(table: string): Promise<SyncEnableResponse>;
  syncPut(table: string, id: string, doc: Record<string, any> | string): Promise<SyncPutResponse>;
  syncDelete(table: string, id: string): Promise<SyncDeleteResponse>;
}

/** Scoped private data-directory file helper bound to a ScopeContext's scopeKey. */
export interface ScopedDatafsApi {
  read(path: string, opts?: { offset?: number; length?: number }): Promise<ReadDataResponse>;
  write(path: string, content: string | { text?: string; data?: string; makeDirs?: boolean }, opts?: { makeDirs?: boolean }): Promise<WriteDataResponse>;
  list(path?: string, opts?: { recursive?: boolean }): Promise<ListDataResponse>;
  stat(path: string): Promise<StatDataResponse>;
  mkdir(path: string): Promise<MkdirDataResponse>;
  delete(path: string, opts?: { recursive?: boolean }): Promise<DeleteDataResponse>;
  move(fromPath: string, toPath: string, opts?: { makeDirs?: boolean }): Promise<MoveDataResponse>;
  stats(): Promise<DataStatsResponse>;
}

/** Active workspace scope context passed to block lifecycle hooks and scoped tools. */
export interface ScopeContext {
  readonly scope: PluginScope;
  readonly scopeKey: string;
  readonly spaceId: string;
  readonly spaceName: string;
  readonly projectId: string;
  readonly projectName: string;
  readonly sessionId: string;
  readonly roots: readonly string[];
  /** Active BCP-47 language tag from the front-end (defaults to "en"). */
  readonly locale: string;
  /** Translates a key using the context's locale with {name} placeholder interpolation. */
  t(key: string, params?: Record<string, string | number | boolean>): string;
  readonly storage: ScopedStorageApi;
  readonly settings: ScopedSettingsApi;
  readonly db: ScopedDbApi;
  readonly datafs: ScopedDatafsApi;
}

export interface Blocks {
  /** Registers a block the manifest declares under contributes.block. */
  register<S extends object>(id: string, block: Block<S>): void;
  /** The state of a block registered, optionally for a specific scopeKey. */
  state<S extends object = any>(id: string, scopeKey?: string): S;
  /** Latest form field values carried by the current block event, keyed by node id and key. */
  values(id: string, scopeKey?: string): Record<string, string>;
  /** Has a block's load run again once the current call ends, optionally for a specific scopeKey. */
  reload(id: string, scopeKey?: string): void;
  /** Returns the active ScopeContext for the block currently being invoked. */
  context(id: string): ScopeContext;
}

export interface Commands {
  /** Registers what a command the manifest declares does. */
  register(id: string, run: (ctx?: ScopeContext) => void | Promise<void>): void;
}

export interface Tools {
  /** Registers an MCP tool declared under contributes.tool for running agents to call. */
  register(id: string, run: (args: Record<string, any>, ctx: ScopeContext) => any | Promise<any>): void;
}

/** A block: state, loaded and drawn as render says, changed by on's messages. */
export interface Block<S extends object> {
  /** The state it starts with; JSON. */
  state: S;
  /** Fills the state: when the block is first shown, and every refresh seconds while it shows. */
  load?(state: S, ctx: ScopeContext): void | Promise<void>;
  /** Seconds between loads while the block shows; at least 15. */
  refresh?: number;
  /** Draws the state, as a tree of the UI vocabulary. It may not call the Host API. */
  render(state: S, ui: Ui, ctx: ScopeContext): UiNode;
  /** What each message a node sends does to the state. */
  on?: Record<string, (state: S, ...args: any[]) => void | Promise<void>>;
}

/** A colour, as a token of the theme; never a value. */
export type Tone = "" | "muted" | "faint" | "accent" | "success" | "warning" | "danger";

/** Visual style variant for button or card nodes. */
export type Variant = "" | "primary" | "secondary" | "outline" | "ghost" | "danger";

/** A message a node sends: its name in on, then arguments, JSON. */
export type Message = [string, ...any[]];

export interface NodeOptions {
  /** Tells siblings apart across renders; a node's id is its keys' path. */
  key?: string | number;
  tone?: Tone;
  /** Style variant for button or card nodes. */
  variant?: Variant;
  weight?: "medium" | "semibold";
  size?: "small" | "large";
  /** Shares out a row's or column's room left. */
  grow?: number;
  /** Room between a container's children, in px. */
  gap?: number;
  /** Column count (1..6) for a grid container. */
  columns?: number;
  /** Initial primary pane ratio (0.1..0.9) for a split container. */
  ratio?: number;
  /** On a root column, row, or split, skips outer auto-scroll so inner scroll panes can fill. */
  fill?: boolean;
  /** A bundled icon for an icon, button, collapsible, or tree_item node. */
  icon?: string;
  /** Initial expanded/selected state for toggle, collapsible, or tree_item. */
  checked?: boolean;
  /** Secondary/meta text on tree_item, or focused node ID on graph. */
  value?: string;
  disabled?: boolean;
  onTap?: Message;
  /** Sent with the new value last: toggle/collapsible/tree_item (bool), select/tabs/input/textarea/breadcrumb index/graph node ID (string). */
  onChange?: Message;
  /** Sent with an input's or textarea's text last when Return is pressed. */
  onSubmit?: Message;
  /** On markdown, sent with the clicked link target last. */
  onLink?: Message;
  /** Payload carried when this node is dragged onto a drop target. */
  drag?: string;
  /** Optional drag group tag matched against a target's dropAccept. */
  dragGroup?: string;
  /** Sent with the dragged node's drag payload last when dropped on this node. */
  onDrop?: Message;
  /** Restricts this drop target to drags whose dragGroup is in the list. */
  dropAccept?: string[];
  /** Scroll or split container direction: "vertical" or "horizontal". */
  direction?: "vertical" | "horizontal";
  /** Maximum height in px for scroll (0..4000) or graph canvas (120..1200). */
  maxHeight?: number;
  placeholder?: string;
  /** A chart's or graph's caption, said above it and read out with it. */
  caption?: string;
  /** A chart's names of its points/slices, or a table's column headers. */
  labels?: (string | number)[];
}

/** A series of a chart: at most 200 finite numbers (1..16 non-negative for donut/pie), its colour a token. */
export interface ChartSeries {
  name?: string;
  values: number[];
  tone?: Tone;
}

/** A node in a declarative interactive graph canvas. */
export interface GraphNode {
  id: string;
  label: string;
  group?: string;
  tone?: Tone;
  subtitle?: string;
  /** Positive finite weight (> 0); defaults to 1. */
  weight?: number;
}

/** An edge connecting two GraphNodes by id. */
export interface GraphEdge {
  from: string;
  to: string;
  label?: string;
  tone?: Tone;
  dashed?: boolean;
}

/** A node of a block's tree; its methods set what NodeOptions do. */
export interface UiNode {
  key(k: string | number): this;
  tone(t: Tone): this;
  variant(v: Variant): this;
  weight(w: "medium" | "semibold"): this;
  size(s: "small" | "large"): this;
  grow(n?: number): this;
  gap(px: number): this;
  columns(n: number): this;
  ratio(r: number): this;
  fill(on?: boolean): this;
  icon(name: string): this;
  checked(on?: boolean): this;
  value(v: string): this;
  disabled(on?: boolean): this;
  onTap(name: string, ...args: any[]): this;
  onChange(name: string, ...args: any[]): this;
  onSubmit(name: string, ...args: any[]): this;
  onLink(name: string, ...args: any[]): this;
  drag(payload: string, group?: string): this;
  onDrop(name: string, ...args: any[]): this;
  dropAccept(...groups: string[]): this;
}

/** The UI vocabulary: the only nodes a block draws. */
export interface Ui {
  column(children: UiNode[], opts?: NodeOptions): UiNode;
  row(children: UiNode[], opts?: NodeOptions): UiNode;
  stack(children: UiNode[], opts?: NodeOptions): UiNode;
  card(children: UiNode[], opts?: NodeOptions): UiNode;
  scroll(children: UiNode[], opts?: NodeOptions): UiNode;
  wrap(children: UiNode[], opts?: NodeOptions): UiNode;
  grid(columns: number, children: UiNode[], opts?: NodeOptions): UiNode;
  grid(children: UiNode[], opts?: NodeOptions): UiNode;
  split(first: UiNode, second: UiNode, opts?: NodeOptions): UiNode;
  split(children: [UiNode, UiNode], opts?: NodeOptions): UiNode;
  collapsible(title: string, children: UiNode[], opts?: NodeOptions): UiNode;
  breadcrumb(segments: string[], opts?: NodeOptions): UiNode;
  tree(items: UiNode[], opts?: NodeOptions): UiNode;
  treeItem(label: string, opts?: NodeOptions): UiNode;
  treeItem(label: string, children: UiNode[], opts?: NodeOptions): UiNode;
  text(text: string, opts?: NodeOptions): UiNode;
  icon(name: string, opts?: NodeOptions): UiNode;
  pill(text: string, opts?: NodeOptions): UiNode;
  chip(text: string, opts?: NodeOptions): UiNode;
  button(text: string, opts?: NodeOptions): UiNode;
  list<T>(items: T[], row: (item: T, index: number) => UiNode, opts?: NodeOptions): UiNode;
  input(value: string, opts?: NodeOptions): UiNode;
  textarea(value: string, opts?: NodeOptions): UiNode;
  toggle(checked: boolean, label: string, opts?: NodeOptions): UiNode;
  select(value: string, options: string[], opts?: NodeOptions): UiNode;
  tabs(value: string, options: string[], opts?: NodeOptions): UiNode;
  table(headers: string[], rows: UiNode[], opts?: NodeOptions): UiNode;
  markdown(text: string, opts?: NodeOptions): UiNode;
  code(text: string, language?: string, opts?: NodeOptions): UiNode;
  progress(value: number, opts?: NodeOptions): UiNode;
  divider(opts?: NodeOptions): UiNode;
  /** A line, bar, sparkline, donut or pie chart; up to 4 series (600 points), or 1 series of 1..16 non-negative slices with matching labels for donut/pie. */
  chart(type: "line" | "bar" | "sparkline" | "donut" | "pie", series: ChartSeries[], opts?: NodeOptions): UiNode;
  /** A declarative interactive node-edge graph canvas (up to 120 nodes, 400 edges); onChange fires clicked node ID. */
  graph(nodes: GraphNode[], edges: GraphEdge[], opts?: NodeOptions): UiNode;
}
`
