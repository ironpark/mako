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

// A prompt runs JavaScript in fenced blocks, on QuickJS in a sandbox of
// its own (qngg): no files, no network, nothing printed. A ```js block
// gives way to the value it ends on, as a script's last expression or
// what it returns; a ```js run block runs and leaves nothing. The blocks
// of a prompt run in one runtime, in order, after the match blocks have
// picked their arms and before the names are filled in the text around
// them, so a block defines with var, or on globalThis, what later blocks
// use. A block sees the arguments as args, a number, a boolean or a text
// as their types say, and the built-in variables as vars; it may await.
//
//	```js run
//	var lines = n => n === 1 ? "one line" : n + " lines";
//	```
//	Review {{file}} in ```js
//	lines(args.depth * 10)
//	```.
//
// A value that is text is put in as it is, one that is undefined or null
// as nothing, and any other as JSON.

// scriptFence is a fenced block of JavaScript, on lines of its own: its
// info string, js or javascript, with run for one run only, its code, and
// its closing fence.
var scriptFence = regexp.MustCompile("(?m)^[ \t]*```[ \t]*(?:js|javascript)([ \t]+run)?[ \t]*\n((?s:.*?))^[ \t]*```[ \t]*(?:\n|$)")

// scriptTimeout bounds how long a prompt's blocks run, together.
const scriptTimeout = 5 * time.Second

// scriptMemory bounds the heap a prompt's blocks share, in bytes.
const scriptMemory = 64 << 20

// Script is a fenced block of JavaScript in a prompt: its bytes [Start,
// End), fences and all, its code, and whether it runs only.
type Script struct {
	Start, End int
	Code       string
	Run        bool
}

// Scripts are the fenced blocks of JavaScript in body, in order.
func Scripts(body string) []Script {
	var out []Script
	for _, m := range scriptFence.FindAllStringSubmatchIndex(body, -1) {
		out = append(out, Script{Start: m[0], End: m[1], Code: body[m[4]:m[5]], Run: m[2] >= 0})
	}
	return out
}

// inScript reports whether [start, end) lies within one of scripts.
func inScript(scripts []Script, start, end int) bool {
	for _, s := range scripts {
		if start >= s.Start && end <= s.End {
			return true
		}
	}
	return false
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

// HasScripts reports whether the template's prompt runs JavaScript, in a
// script or a block's subject, which takes a while to fill.
func (t *Template) HasScripts() bool {
	return scriptFence.MatchString(t.Body) || len(t.expressions(t.parsed())) > 0
}

// filler fills a template's prompt in the order it reads: text with its
// names filled and its scripts run, and blocks with the arm their
// subject's value picks. It starts a JavaScript runtime only once
// something needs one, which the scripts and subjects then share.
type filler struct {
	ctx              context.Context
	t                *Template
	values, builtins map[string]string
	rt               *qngg.Runtime
	scripts          int // the scripts run, to name the next
}

func (f *filler) value(name string) string {
	if v, ok := f.values[name]; ok {
		return v
	}
	return f.builtins[name]
}

// runtime is the prompt's JavaScript runtime, started the first time.
func (f *filler) runtime() (*qngg.Runtime, error) {
	if f.rt != nil {
		return f.rt, nil
	}
	rt, err := qngg.New(qngg.Config{MemoryLimit: scriptMemory})
	if err != nil {
		return nil, fmt.Errorf("JavaScript: %w", err)
	}
	if err := rt.Set("args", f.t.typedArgs(f.values)); err != nil {
		rt.Close()
		return nil, err
	}
	if err := rt.Set("vars", f.builtins); err != nil {
		rt.Close()
		return nil, err
	}
	f.rt = rt
	return rt, nil
}

func (f *filler) close() {
	if f.rt != nil {
		f.rt.Close()
	}
}

// segments is segs filled in.
func (f *filler) segments(segs []segment) (string, error) {
	var b strings.Builder
	for _, s := range segs {
		if s.block == nil {
			text, err := f.text(s.text)
			if err != nil {
				return "", err
			}
			b.WriteString(text)
			continue
		}
		v, err := f.subject(s.block.subject)
		if err != nil {
			return "", err
		}
		for _, arm := range s.block.arms {
			if slices.ContainsFunc(arm.patterns, func(p string) bool { return matches(p, v) }) {
				text, err := f.segments(arm.body)
				if err != nil {
					return "", err
				}
				b.WriteString(text)
				break
			}
		}
	}
	return b.String(), nil
}

// subject is the value of a block's subject: an argument's or a built-in
// variable's, or what its JavaScript evaluates to.
func (f *filler) subject(subject string) (string, error) {
	if !f.t.isExpression(subject) {
		return f.value(subject), nil
	}
	rt, err := f.runtime()
	if err != nil {
		return "", err
	}
	v, err := evalScript(f.ctx, rt, "{#match}", "("+subject+")")
	if err != nil {
		return "", fmt.Errorf("{#match %s}: %w", subject, scriptError(err))
	}
	return v, nil
}

// text is text with its scripts run, a value's in its place and a run
// only's gone, and the names filled between them, which a value is not.
func (f *filler) text(text string) (string, error) {
	var b strings.Builder
	at := 0
	for _, s := range Scripts(text) {
		b.WriteString(f.fill(text[at:s.Start]))
		at = s.End
		f.scripts++
		rt, err := f.runtime()
		if err != nil {
			return "", err
		}
		v, err := evalScript(f.ctx, rt, fmt.Sprintf("%s.md:block %d", f.t.Name, f.scripts), s.Code)
		if err != nil {
			return "", fmt.Errorf("block %d: %w", f.scripts, scriptError(err))
		}
		// A value takes the lines its block took; a block run only, or
		// with no value, leaves none.
		if !s.Run && v != "" {
			b.WriteString(v)
			if strings.HasSuffix(text[s.Start:s.End], "\n") {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString(f.fill(text[at:]))
	return b.String(), nil
}

// fill is text with its {{name}}s given their values.
func (f *filler) fill(text string) string {
	return placeholder.ReplaceAllStringFunc(text, func(s string) string {
		return f.value(strings.TrimSpace(s[2 : len(s)-2]))
	})
}

// evalScript runs code, giving the value it ends on as text. Code that
// returns, as a function's body would, runs as one.
func evalScript(ctx context.Context, rt *qngg.Runtime, name, code string) (string, error) {
	v, err := rt.Eval(ctx, name, code, qngg.TopLevelAwait())
	var jsErr *qngg.Error
	if errors.As(err, &jsErr) && jsErr.Name == "SyntaxError" && strings.Contains(jsErr.Message, "return not in a function") {
		v, err = rt.Eval(ctx, name, "await (async () => {\n"+code+"\n})()", qngg.TopLevelAwait())
	}
	if err != nil {
		return "", err
	}
	defer v.Free()
	switch {
	case v.IsUndefined(), v.IsNull():
		return "", nil
	case v.IsString():
		return v.String(), nil
	}
	out, err := v.MarshalJSON()
	if err != nil {
		return v.String(), nil
	}
	return string(out), nil
}

// scriptError says what went wrong in a block, its JavaScript error's
// name and message without the stack.
func scriptError(err error) error {
	var jsErr *qngg.Error
	switch {
	case errors.Is(err, qngg.ErrInterrupted):
		return fmt.Errorf("ran past %s", scriptTimeout)
	case errors.As(err, &jsErr):
		return fmt.Errorf("%s: %s", jsErr.Name, jsErr.Message)
	}
	return err
}

// typedArgs are values as a block sees them: a number, a boolean or text
// as each argument's type says.
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

// checkScripts finds a script, or a block's subject of JavaScript, that
// does not parse.
func (t *Template) checkScripts(segs []segment) error {
	scripts, exprs := Scripts(t.Body), t.expressions(segs)
	if len(scripts) == 0 && len(exprs) == 0 {
		return nil
	}
	rt, err := qngg.New(qngg.Config{MemoryLimit: scriptMemory})
	if err != nil {
		return fmt.Errorf("JavaScript: %w", err)
	}
	defer rt.Close()
	for _, e := range exprs {
		if _, err := rt.Compile("{#match}", "("+e+")"); err != nil {
			return fmt.Errorf("{#match %s}: %w", e, scriptError(err))
		}
	}
	for i, s := range scripts {
		if _, err := rt.Compile(fmt.Sprintf("block %d", i+1), s.Code, qngg.TopLevelAwait()); err != nil {
			var jsErr *qngg.Error
			if errors.As(err, &jsErr) && strings.Contains(jsErr.Message, "return not in a function") {
				if _, err = rt.Compile(fmt.Sprintf("block %d", i+1), "(async () => {\n"+s.Code+"\n})()"); err == nil {
					continue
				}
			}
			return fmt.Errorf("block %d: %w", i+1, scriptError(err))
		}
	}
	return nil
}
