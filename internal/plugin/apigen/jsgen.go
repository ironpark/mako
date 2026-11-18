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
}

/** What activate is given: the Host API, and the plugin's blocks and commands. */
export interface Mako extends HostApi {
  readonly blocks: Blocks;
  readonly commands: CommandsApi & Commands;
}

export interface Blocks {
  /** Registers a block the manifest declares under contributes.block. */
  register<S extends object>(id: string, block: Block<S>): void;
  /** The state of a block registered, which a command may change. */
  state<S extends object = any>(id: string): S;
  /** Has a block's load run again once the current call ends. */
  reload(id: string): void;
}

export interface Commands {
  /** Registers what a command the manifest declares does. */
  register(id: string, run: () => void | Promise<void>): void;
}

/** A block: state, loaded and drawn as render says, changed by on's messages. */
export interface Block<S extends object> {
  /** The state it starts with; JSON. */
  state: S;
  /** Fills the state: when the block is first shown, and every refresh seconds while it shows. */
  load?(state: S): void | Promise<void>;
  /** Seconds between loads while the block shows; at least 15. */
  refresh?: number;
  /** Draws the state, as a tree of the UI vocabulary. It may not call the Host API. */
  render(state: S, ui: Ui): UiNode;
  /** What each message a node sends does to the state. */
  on?: Record<string, (state: S, ...args: any[]) => void | Promise<void>>;
}

/** A colour, as a token of the theme; never a value. */
export type Tone = "" | "muted" | "faint" | "accent" | "success" | "warning" | "danger";

/** A message a node sends: its name in on, then arguments, JSON. */
export type Message = [string, ...any[]];

export interface NodeOptions {
  /** Tells siblings apart across renders; a node's id is its keys' path. */
  key?: string | number;
  tone?: Tone;
  weight?: "medium" | "semibold";
  size?: "small" | "large";
  /** Shares out a row's or column's room left. */
  grow?: number;
  /** Room between a row's or column's children, in px. */
  gap?: number;
  /** A bundled icon beside a button's text. */
  icon?: string;
  disabled?: boolean;
  onTap?: Message;
  /** Sent with the new value last: a toggle's, select's or input's. */
  onChange?: Message;
  /** Sent with an input's text last when Return is pressed. */
  onSubmit?: Message;
  placeholder?: string;
}

/** A node of a block's tree; its methods set what NodeOptions do. */
export interface UiNode {
  key(k: string | number): this;
  tone(t: Tone): this;
  weight(w: "medium" | "semibold"): this;
  size(s: "small" | "large"): this;
  grow(n?: number): this;
  gap(px: number): this;
  icon(name: string): this;
  disabled(on?: boolean): this;
  onTap(name: string, ...args: any[]): this;
  onChange(name: string, ...args: any[]): this;
}

/** The UI vocabulary: the only nodes a block draws. */
export interface Ui {
  column(children: UiNode[], opts?: NodeOptions): UiNode;
  row(children: UiNode[], opts?: NodeOptions): UiNode;
  stack(children: UiNode[], opts?: NodeOptions): UiNode;
  text(text: string, opts?: NodeOptions): UiNode;
  icon(name: string, opts?: NodeOptions): UiNode;
  pill(text: string, opts?: NodeOptions): UiNode;
  chip(text: string, opts?: NodeOptions): UiNode;
  button(text: string, opts?: NodeOptions): UiNode;
  list<T>(items: T[], row: (item: T, index: number) => UiNode, opts?: NodeOptions): UiNode;
  input(value: string, opts?: NodeOptions): UiNode;
  toggle(checked: boolean, label: string, opts?: NodeOptions): UiNode;
  select(value: string, options: string[], opts?: NodeOptions): UiNode;
  markdown(text: string, opts?: NodeOptions): UiNode;
  code(text: string, language?: string, opts?: NodeOptions): UiNode;
  progress(value: number, opts?: NodeOptions): UiNode;
  divider(opts?: NodeOptions): UiNode;
}
`
