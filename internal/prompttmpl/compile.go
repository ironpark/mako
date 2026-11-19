package prompttmpl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A prompt read is filled in by JavaScript it compiles to: one async
// function that adds the prompt's text to __o piece by piece, its blocks
// JavaScript's own if, for and functions, so that what an each, a const or
// a snippet defines is in scope where JavaScript has it. What can be
// checked before it runs is checked here: the names a prompt uses, its
// match blocks' patterns and the values they cover.

// prelude is what the compiled function uses.
const prelude = `const __s = v => v === undefined || v === null ? "" : typeof v === "string" ? v : typeof v === "object" ? JSON.stringify(v) : String(v);
const __num = v => v.trim() === "" ? NaN : Number(v);
const __list = v => v == null ? [] : typeof v === "number" ? Array.from({length: v}, (_, i) => i) : Array.from(v);
const __val = (v, nl) => { const s = __s(v); return s !== "" && nl ? s + "\n" : s; };
`

// compiled is a prompt as the program that fills it in, and its pieces
// of JavaScript, each with what to call it should it not parse.
type compiled struct {
	program string
	checks  []jsCheck
}

type jsCheck struct {
	what, code string
	await      bool // parsed where it may await
}

// compiler writes a prompt's program, keeping the names each scope of it
// defines.
type compiler struct {
	t       *Template
	b       strings.Builder
	scopes  []map[string]bool
	checks  []jsCheck
	scripts map[string]bool // what scripts define with var or function
}

// compile writes the program nodes, read from the template's prompt, fill
// it in with, or finds what is wrong with them.
func (t *Template) compile(nodes []*node) (*compiled, error) {
	c := &compiler{t: t, scopes: []map[string]bool{{}}, scripts: scriptNames(nodes)}
	c.line(`let __o = "";`)
	// An argument and a built-in variable are names an expression reads.
	for _, a := range t.Args {
		if jsName(a.Name) {
			c.line(fmt.Sprintf("let %s = args[%s];", a.Name, js(a.Name)))
		}
	}
	for _, b := range Builtins {
		if t.arg(b) == nil && jsName(b) {
			c.line(fmt.Sprintf("let %s = vars[%s];", b, js(b)))
		}
	}
	if err := c.nodes(nodes); err != nil {
		return nil, err
	}
	c.line("return __o;")
	return &compiled{program: prelude + "await (async () => {\n" + c.b.String() + "})()", checks: c.checks}, nil
}

func (c *compiler) line(s string) { c.b.WriteString(s + "\n") }

func (c *compiler) check(what, code string, await bool) {
	c.checks = append(c.checks, jsCheck{what: what, code: code, await: await})
}

// declare has names defined in the scope written now.
func (c *compiler) declare(names ...string) {
	for _, n := range names {
		c.scopes[len(c.scopes)-1][n] = true
	}
}

// local reports whether a scope written now, or a script, defines name.
func (c *compiler) local(name string) bool {
	for _, s := range c.scopes {
		if s[name] {
			return true
		}
	}
	return c.scripts[name]
}

// nodes writes nodes in a scope of their own, the snippets among them
// known throughout it, as JavaScript hoists functions.
func (c *compiler) nodes(nodes []*node) error {
	c.scopes = append(c.scopes, map[string]bool{})
	defer func() { c.scopes = c.scopes[:len(c.scopes)-1] }()
	for _, n := range nodes {
		if n.kind == TagSnippet {
			c.declare(n.expr)
		}
	}
	for _, n := range nodes {
		if err := c.node(n); err != nil {
			return err
		}
	}
	return nil
}

// block writes nodes as a JavaScript block.
func (c *compiler) block(nodes []*node) error {
	c.line("{")
	err := c.nodes(nodes)
	c.line("}")
	return err
}

func (c *compiler) node(n *node) error {
	switch n.kind {
	case "text":
		c.line("__o += " + js(n.text) + ";")
	case "hole":
		if !n.name {
			c.check("{{"+n.text+"}}", "("+n.text+")", true)
			c.guard("{{"+n.text+"}}", "__o += __s(await ("+n.text+"));")
			return nil
		}
		v, err := c.name(n.text, "{{"+n.text+"}}")
		if err != nil {
			return err
		}
		c.line("__o += " + v + ";")
	case "script":
		c.script(n)
	case TagConst:
		c.check("{@const "+n.text+"}", "{ const "+n.text+"; }", true)
		c.line("const " + n.text + ";")
		lhs := n.text
		if eq := firstTopLevel(lhs, "="); eq >= 0 {
			lhs = lhs[:eq]
		}
		c.declare(identifiers(lhs)...)
	case TagRender:
		c.check("{@render "+n.text+"}", "("+n.text+")", true)
		c.guard("{@render "+n.text+"}", "__o += __s(await ("+n.text+"));")
	case TagIf:
		for i, br := range n.branches {
			switch {
			case i == 0:
				c.check("{#if "+br.cond+"}", "("+br.cond+")", true)
				c.b.WriteString("if (" + br.cond + ") ")
			case br.cond != "":
				c.check("{:else if "+br.cond+"}", "("+br.cond+")", true)
				c.b.WriteString("else if (" + br.cond + ") ")
			default:
				c.b.WriteString("else ")
			}
			if err := c.block(br.body); err != nil {
				return err
			}
		}
	case TagEach:
		what := "{#each " + n.expr + " as " + n.as + "}"
		c.check(what, "("+n.expr+")", true)
		c.check(what, "{ const "+n.as+" = 0; }", false)
		c.line("{ const __a = __list(" + n.expr + ");")
		c.line("if (__a.length > 0) for (let __i = 0; __i < __a.length; __i++) {")
		c.scopes = append(c.scopes, map[string]bool{})
		c.line("const " + n.as + " = __a[__i];")
		c.declare(identifiers(n.as)...)
		if n.at != "" {
			c.line("const " + n.at + " = __i;")
			c.declare(n.at)
		}
		err := c.nodes(n.body)
		c.scopes = c.scopes[:len(c.scopes)-1]
		if err != nil {
			return err
		}
		c.line("} else")
		if err := c.block(n.empty); err != nil {
			return err
		}
		c.line("}")
	case TagMatch:
		return c.match(n)
	case TagSnippet:
		c.check("{#snippet "+n.expr+"("+n.as+")}", "(async function "+n.expr+"("+n.as+") {})", false)
		c.line("async function " + n.expr + "(" + n.as + ") {")
		c.line(`let __o = "";`)
		c.scopes = append(c.scopes, map[string]bool{})
		for _, p := range topLevelParts(n.as, ",") {
			if eq := firstTopLevel(p, "="); eq >= 0 {
				p = p[:eq]
			}
			c.declare(identifiers(p)...)
		}
		err := c.nodes(n.body)
		c.scopes = c.scopes[:len(c.scopes)-1]
		if err != nil {
			return err
		}
		c.line("return __o;")
		c.line("}")
	}
	return nil
}

// name is the value a {{name}}, or a block's subject, gives, as text: an
// argument's as the call gave it, a built-in variable's, or what a scope
// or a script defines by the name.
func (c *compiler) name(name, what string) (string, error) {
	switch {
	case c.local(name):
		return "__s(" + name + ")", nil
	case c.t.arg(name) != nil:
		return "__s(__raw[" + js(name) + "])", nil
	case IsBuiltin(name):
		return "__s(vars[" + js(name) + "])", nil
	}
	return "", fmt.Errorf("%s: no argument, built-in variable or name defined before it is called %s", what, name)
}

// script writes a script. One run only runs in the global scope, so that
// what it defines with var or function every later piece reads; one with a
// value runs where it is, reading the names of the blocks it is in, its
// value the last expression's, or, when it returns or awaits, as a
// function's body, what it returns.
func (c *compiler) script(n *node) {
	nl := strconv.FormatBool(n.nl)
	what := fmt.Sprintf("block %d", len(c.scriptChecks())+1)
	fn := needsFunction(n.text)
	if fn {
		c.check(what, "(async () => {\n"+n.text+"\n})", false)
	} else {
		c.check(what, n.text, false)
	}
	switch {
	case n.run && fn:
		c.guard(what, "await (0, eval)("+js("(async () => {\n"+n.text+"\n})()")+");")
	case n.run:
		c.guard(what, "(0, eval)("+js(n.text)+");")
	case fn:
		c.guard(what, "__o += __val(await (async () => {\n"+n.text+"\n})(), "+nl+");")
	default:
		c.guard(what, "__o += __val(eval("+js(n.text)+"), "+nl+");")
	}
}

// guard writes stmt so that what it throws says it is what's.
func (c *compiler) guard(what, stmt string) {
	c.line("try { " + stmt + " } catch (__e) { if (__e instanceof Error) __e.message = " + js(what+": ") + " + __e.message; throw __e; }")
}

func (c *compiler) scriptChecks() []jsCheck {
	var out []jsCheck
	for _, ch := range c.checks {
		if strings.HasPrefix(ch.what, "block ") {
			out = append(out, ch)
		}
	}
	return out
}

var functionWords = regexp.MustCompile(`\b(return|await)\b`)

// needsFunction reports whether a script returns or awaits, which eval
// cannot.
func needsFunction(code string) bool { return functionWords.MatchString(code) }

// match writes a match block: its subject's value as text, and an if for
// each arm; and checks its patterns and that its arms cover its values.
func (c *compiler) match(n *node) error {
	what := "{#match " + n.expr + "}"
	var value string
	var arg *Arg
	if IsName(n.expr) {
		var err error
		if value, err = c.name(n.expr, what); err != nil {
			return err
		}
		if !c.local(n.expr) {
			arg = c.t.arg(n.expr)
		}
	} else {
		c.check(what, "("+n.expr+")", true)
		value = "__s(await (" + n.expr + "))"
	}
	var covered []string
	for _, a := range n.arms {
		for _, p := range a.patterns {
			if err := checkPattern(arg, p); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
		covered = append(covered, a.patterns...)
	}
	if err := covers(what, arg, covered); err != nil {
		return err
	}
	c.line("{ const __v = " + value + ", __n = __num(__v);")
	for i, a := range n.arms {
		preds := make([]string, len(a.patterns))
		for j, p := range a.patterns {
			preds[j] = patternJS(p)
		}
		if i > 0 {
			c.b.WriteString("else ")
		}
		c.b.WriteString("if (" + strings.Join(preds, " || ") + ") ")
		if err := c.block(a.body); err != nil {
			return err
		}
	}
	c.line("}")
	return nil
}

// covers finds a value of arg's that no pattern of covered matches; for a
// subject not an argument's, or one of values not few, it wants _, or
// true and false.
func covers(what string, arg *Arg, covered []string) error {
	if slices.Contains(covered, "_") {
		return nil
	}
	values := arg.values()
	if values == nil {
		if slices.Contains(covered, "true") && slices.Contains(covered, "false") {
			return nil
		}
		return fmt.Errorf("%s: not every value is matched; add {:else} for the rest", what)
	}
	for _, v := range values {
		if !slices.ContainsFunc(covered, func(p string) bool { return matches(p, v) }) {
			return fmt.Errorf("%s: no arm matches %q; add one, or {:else} for the rest", what, v)
		}
	}
	return nil
}

// patternJS is pattern p as a test of __v, the subject's value as text,
// and __n, the number it reads as.
func patternJS(p string) string {
	switch {
	case p == "_":
		return "true"
	case len(p) >= 2 && p[0] == '"':
		return "__v === " + js(p[1:len(p)-1])
	}
	if m := rangeWord.FindStringSubmatch(p); m != nil {
		test := []string{"!isNaN(__n)"}
		if m[1] != "" {
			test = append(test, "__n >= "+m[1])
		}
		if m[3] != "" {
			test = append(test, "__n "+map[bool]string{true: "<=", false: "<"}[m[2] == "="]+" "+m[3])
		}
		return "(" + strings.Join(test, " && ") + ")"
	}
	return "__v === " + js(p)
}

// js is s as a JavaScript string literal.
func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var (
	identifierRe = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	jsNameRe     = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
	scriptVarRe  = regexp.MustCompile(`\b(?:var|function)\s+([A-Za-z_$][\w$]*)|\bglobalThis\.([A-Za-z_$][\w$]*)\s*=`)
)

// jsName reports whether name is an identifier JavaScript lets a variable
// have.
func jsName(name string) bool { return jsNameRe.MatchString(name) && !reserved[name] }

var reserved = map[string]bool{}

func init() {
	for w := range strings.FieldsSeq("break case catch class const continue debugger default delete do else enum export extends false finally for function if import in instanceof new null return super switch this throw true try typeof var void while with yield let static implements interface package private protected public await async arguments eval undefined NaN Infinity args vars mako") {
		reserved[w] = true
	}
}

// identifiers are the names a binding, as an each's item or a const's
// left side, defines: those not a property's key.
func identifiers(pattern string) []string {
	var out []string
	for _, m := range identifierRe.FindAllStringIndex(pattern, -1) {
		rest := strings.TrimLeft(pattern[m[1]:], " \t")
		if strings.HasPrefix(rest, ":") {
			continue // a key, renamed
		}
		out = append(out, pattern[m[0]:m[1]])
	}
	return out
}

// scriptNames are what the scripts of nodes run only define with var or
// function, or on globalThis, which every later piece reads.
func scriptNames(nodes []*node) map[string]bool {
	out := map[string]bool{}
	var walk func([]*node)
	walk = func(ns []*node) {
		for _, n := range ns {
			if n.kind == "script" && n.run {
				for _, m := range scriptVarRe.FindAllStringSubmatch(n.text, -1) {
					out[m[1]+m[2]] = true
				}
			}
			for _, b := range n.branches {
				walk(b.body)
			}
			for _, a := range n.arms {
				walk(a.body)
			}
			walk(n.body)
			walk(n.empty)
		}
	}
	walk(nodes)
	return out
}

// firstTopLevel is the index of the first sep in s outside brackets and
// quotes, -1 for none; one = of ==, =>, <= or >= is no sep.
func firstTopLevel(s, sep string) int {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] != sep || !topLevel(s, i) {
			continue
		}
		if sep == "=" && (i+1 < len(s) && (s[i+1] == '=' || s[i+1] == '>') || i > 0 && strings.IndexByte("=!<>", s[i-1]) >= 0) {
			continue
		}
		return i
	}
	return -1
}

// topLevelParts are s split at each sep outside brackets and quotes.
func topLevelParts(s, sep string) []string {
	var out []string
	for {
		i := firstTopLevel(s, sep)
		if i < 0 {
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
			return out
		}
		out = append(out, strings.TrimSpace(s[:i]))
		s = s[i+len(sep):]
	}
}

// Locals are the names body defines: each's items and indexes, consts,
// snippets and their parameters, and what its scripts define; for an
// editor to colour.
func Locals(body string) []string {
	nodes, err := parseBody(body)
	if err != nil {
		return nil
	}
	set := scriptNames(nodes)
	var walk func([]*node)
	walk = func(ns []*node) {
		for _, n := range ns {
			switch n.kind {
			case TagEach:
				for _, id := range identifiers(n.as) {
					set[id] = true
				}
				if n.at != "" {
					set[n.at] = true
				}
			case TagConst:
				lhs := n.text
				if eq := firstTopLevel(lhs, "="); eq >= 0 {
					lhs = lhs[:eq]
				}
				for _, id := range identifiers(lhs) {
					set[id] = true
				}
			case TagSnippet:
				set[n.expr] = true
				for _, p := range topLevelParts(n.as, ",") {
					if eq := firstTopLevel(p, "="); eq >= 0 {
						p = p[:eq]
					}
					for _, id := range identifiers(p) {
						set[id] = true
					}
				}
			}
			for _, b := range n.branches {
				walk(b.body)
			}
			for _, a := range n.arms {
				walk(a.body)
			}
			walk(n.body)
			walk(n.empty)
		}
	}
	walk(nodes)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// plain reports whether nodes are text and names alone, which need no
// JavaScript to fill in.
func plain(nodes []*node, t *Template) bool {
	for _, n := range nodes {
		if n.kind != "text" && (n.kind != "hole" || !n.name || (t.arg(n.text) == nil && !IsBuiltin(n.text))) {
			return false
		}
	}
	return true
}
