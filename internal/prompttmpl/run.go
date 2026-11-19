package prompttmpl

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/qngg"
)

// A prompt's JavaScript runs on QuickJS in a sandbox of its own (qngg):
// no files, no network, nothing printed, bounded in time and memory. A
// prompt of text and names alone needs none, and is filled in Go.
//
// A ```js block gives way to the value it ends on, as a script's last
// expression, or what it returns when it returns or awaits; a ```js run
// block runs and leaves nothing. A block run in place, by eval, defines
// with var what later blocks and expressions use. A value that is text is
// put in as it is, one that is undefined or null as nothing, and any
// other as JSON.

// scriptFence is a fenced block of JavaScript, on lines of its own: its
// info string, js or javascript, with run for one run only, its code, and
// its closing fence.
var scriptFence = regexp.MustCompile("(?m)^[ \t]*```[ \t]*(?:js|javascript)([ \t]+run)?[ \t]*\n((?s:.*?))^[ \t]*```[ \t]*(?:\n|$)")

// scriptTimeout bounds how long a prompt's JavaScript runs, together.
const scriptTimeout = 5 * time.Second

// scriptMemory bounds the heap a prompt's JavaScript has, in bytes.
const scriptMemory = 64 << 20

// Script is a fenced block of JavaScript in a prompt: its bytes [Start,
// End), fences and all, its code, and whether it runs only.
type Script struct {
	Start, End int
	Code       string
	Run        bool
}

// fences are the fenced blocks of JavaScript in body, in order, raw
// blocks or not.
func fences(body string) []Script {
	var out []Script
	for _, m := range scriptFence.FindAllStringSubmatchIndex(body, -1) {
		out = append(out, Script{Start: m[0], End: m[1], Code: body[m[4]:m[5]], Run: m[2] >= 0})
	}
	return out
}

// scriptAt is the script that i, a byte of the prompt, is in.
func scriptAt(scripts []Script, i int) (Script, bool) {
	for _, s := range scripts {
		if i >= s.Start && i < s.End {
			return s, true
		}
	}
	return Script{}, false
}

// HasScripts reports whether the template's prompt runs JavaScript, which
// takes a while to fill.
func (t *Template) HasScripts() bool {
	nodes, err := t.read()
	return err == nil && !plain(nodes, t)
}

// read is the prompt read, as check read it, or now for a template not
// read, as one made to save is.
func (t *Template) read() ([]*node, error) {
	if t.nodes != nil {
		return t.nodes, nil
	}
	nodes, _, err := t.prepare()
	return nodes, err
}

// prepare reads the prompt, and compiles it and checks its JavaScript
// parses. A {{…}} that holds no expression JavaScript reads, as another
// template language's {{.Field}} in a prompt, is text.
func (t *Template) prepare() ([]*node, *compiled, error) {
	nodes, err := parseBody(t.Body)
	if err != nil {
		return nil, nil, err
	}
	if exprHoles(nodes) {
		rt, err := qngg.New(qngg.Config{MemoryLimit: scriptMemory})
		if err != nil {
			return nil, nil, fmt.Errorf("JavaScript: %w", err)
		}
		demote(rt, nodes)
		rt.Close()
	}
	comp, err := t.compile(nodes)
	if err != nil {
		return nil, nil, err
	}
	if !plain(nodes, t) {
		if err := checkJS(comp); err != nil {
			return nil, nil, err
		}
	}
	return nodes, comp, nil
}

// exprHoles reports whether nodes hold a {{…}} of an expression.
func exprHoles(nodes []*node) bool {
	found := false
	walkNodes(nodes, func(n *node) {
		found = found || (n.kind == "hole" && !n.name)
	})
	return found
}

// demote makes text of each {{…}} that holds no expression JavaScript
// reads.
func demote(rt *qngg.Runtime, nodes []*node) {
	walkNodes(nodes, func(n *node) {
		if n.kind == "hole" && !n.name {
			if _, err := rt.Compile("hole", "("+n.text+")", qngg.TopLevelAwait()); err != nil {
				n.kind, n.text = "text", n.raw
			}
		}
	})
}

// walkNodes calls fn for each of nodes and what they hold.
func walkNodes(nodes []*node, fn func(*node)) {
	for _, n := range nodes {
		fn(n)
		for _, b := range n.branches {
			walkNodes(b.body, fn)
		}
		for _, a := range n.arms {
			walkNodes(a.body, fn)
		}
		walkNodes(n.body, fn)
		walkNodes(n.empty, fn)
	}
}

// Fill is the template's prompt filled in, an argument's value from
// values and a built-in variable's from builtins; its JavaScript runs for
// as long as ctx and scriptTimeout allow.
func (t *Template) Fill(ctx context.Context, values, builtins map[string]string) (string, error) {
	return t.FillWith(ctx, values, builtins, nil)
}

// FillWith fills the template with an optional host bridge for mako.* calls.
func (t *Template) FillWith(ctx context.Context, values, builtins map[string]string, host Host) (string, error) {
	nodes, err := t.read()
	if err != nil {
		return "", err
	}
	if plain(nodes, t) {
		var b strings.Builder
		for _, n := range nodes {
			if n.kind == "text" {
				b.WriteString(n.text)
			} else if v, ok := values[n.text]; ok && t.arg(n.text) != nil {
				b.WriteString(v)
			} else {
				b.WriteString(builtins[n.text])
			}
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	comp := t.comp
	if comp == nil {
		if _, comp, err = t.prepare(); err != nil {
			return "", err
		}
	}
	wallTimeout := scriptTimeout
	if host != nil {
		wallTimeout = hostWallTimeout
	}
	budget := newBudget(ctx, scriptTimeout, wallTimeout)
	defer budget.release()
	return evalPromptJS(budget, t, comp.program, values, builtins, host)
}

// scriptError says what went wrong in a prompt's JavaScript, its error's
// name and message without the stack.
func scriptError(err error) error {
	var jsErr *qngg.Error
	switch {
	case errors.Is(err, qngg.ErrInterrupted):
		return fmt.Errorf("its JavaScript ran too long")
	case errors.As(err, &jsErr):
		return fmt.Errorf("%s: %s", jsErr.Name, jsErr.Message)
	}
	return err
}

// typedArgs are values as JavaScript reads them: a number, a boolean or
// text as each argument's type says.
func (t *Template) typedArgs(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for name, v := range values {
		out[name] = v
		a := t.arg(name)
		if a == nil || v == "" {
			continue
		}
		switch a.Type {
		case Integer, Number:
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				out[name] = n
			}
		case Boolean:
			out[name] = v == "true"
		}
	}
	return out
}

// checkJS finds a piece of the prompt's JavaScript that does not parse,
// saying which, and then the program.
func checkJS(comp *compiled) error {
	if len(comp.checks) == 0 {
		return nil
	}
	rt, err := qngg.New(qngg.Config{MemoryLimit: scriptMemory})
	if err != nil {
		return fmt.Errorf("JavaScript: %w", err)
	}
	defer rt.Close()
	for _, ch := range comp.checks {
		var opts []qngg.EvalOption
		if ch.await {
			opts = append(opts, qngg.TopLevelAwait())
		}
		if _, err := rt.Compile(ch.what, ch.code, opts...); err != nil {
			return fmt.Errorf("%s: %w", ch.what, scriptError(err))
		}
	}
	if _, err := rt.Compile("prompt", comp.program, qngg.TopLevelAwait()); err != nil {
		return fmt.Errorf("the prompt: %w", scriptError(err))
	}
	return nil
}

// patternWord is one pattern of a match's arm, and patternList an arm's
// patterns, joined by |.
var (
	patternWord = `"[^"\n]*"|(?:-?\d+(?:\.\d+)?)?\.\.=?(?:-?\d+(?:\.\d+)?)?|[^\s|"]+`
	patternList = regexp.MustCompile(`^\s*(?:` + patternWord + `)(?:\s*\|\s*(?:` + patternWord + `))*\s*$`)
	patternOne  = regexp.MustCompile(patternWord)
	rangeWord   = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)?\.\.(=)?(-?\d+(?:\.\d+)?)?$`)
)

// matches reports whether value matches pattern, as the compiled test of
// it does: what the arms of a match cover is worked out by it.
func matches(pattern, value string) bool {
	switch {
	case pattern == "_":
		return true
	case len(pattern) >= 2 && pattern[0] == '"':
		return pattern[1:len(pattern)-1] == value
	}
	if m := rangeWord.FindStringSubmatch(pattern); m != nil {
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return false
		}
		if m[1] != "" {
			if lo, _ := strconv.ParseFloat(m[1], 64); v < lo {
				return false
			}
		}
		if m[3] != "" {
			hi, _ := strconv.ParseFloat(m[3], 64)
			return v < hi || (m[2] == "=" && v == hi)
		}
		return true
	}
	return pattern == value
}

// values are every value a, as a call gives it, can have, nil when they
// are not few: a choice's options and a boolean's true and false, with ""
// for one a call may leave out with no default. A built-in variable's,
// nil a, are not few.
func (a *Arg) values() []string {
	if a == nil {
		return nil
	}
	var out []string
	switch a.Type {
	case Choice, Select:
		out = slices.Clone(a.Options)
	case Boolean:
		out = []string{"true", "false"}
	default:
		return nil
	}
	if !a.Required && a.Default == nil {
		out = append(out, "")
	}
	return out
}

// checkPattern finds what is wrong with pattern p for arg, nil for any
// other subject: a range of what is not a number, or a value the
// argument does not take, or spells another way, which no call matches.
func checkPattern(arg *Arg, p string) error {
	if p == "_" || p == `""` || arg == nil {
		return nil
	}
	if rangeWord.MatchString(p) && p != ".." {
		if arg.Type != Integer && arg.Type != Number {
			return fmt.Errorf("%s: a range matches only numbers", p)
		}
		return nil
	}
	v := strings.Trim(p, `"`)
	as, err := arg.convert(v)
	if err != nil {
		return err
	}
	if as != v {
		return fmt.Errorf("%s: a call gives it as %s", p, as)
	}
	return nil
}
