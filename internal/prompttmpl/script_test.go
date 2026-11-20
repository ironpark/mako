package prompttmpl

import (
	"context"
	"strings"
	"testing"
	"time"
)

const scripted = "---\nargs:\n  - name: depth\n    type: integer\n    default: 2\n  - name: tone\n    options: [kind, blunt]\n    default: kind\n---\n" +
	"```js run\nvar lines = n => n === 1 ? \"one line\" : n + \" lines\";\n```\n" +
	"Read {{tone}}ly,\n```js\nlines(args.depth * 10)\n```\n" +
	"as {{agent}}.\n```js\nreturn { agent: vars.agent, kind: args.tone === \"kind\" };\n```\n" +
	"```js\nreturn await Promise.resolve(\"{{tone}} stays\")\n```\n" +
	"{#match tone}\n{:kind}\n```js\nconst f = a => a + 1\nf(1)\n```\n{:_}\nnone\n{/match}\n" +
	"```go\nfmt.Println(\"{{tone}}\")\n```"

// A block of JavaScript gives way to its value, as its last expression or
// what it returns, awaited; one run only leaves nothing but what it
// defines; the names are filled around them, not in them; and a block in
// an arm runs when its arm is picked. Other code blocks stay as they are.
func TestScriptsRunInThePrompt(t *testing.T) {
	tm, err := Parse("s", []byte(scripted))
	if err != nil {
		t.Fatal(err)
	}
	if !tm.HasScripts() {
		t.Fatal("no scripts found")
	}
	got, err := tm.Expand(t.Context(), "3", map[string]string{"agent": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	want := "Read kindly,\n30 lines\n" +
		"as claude.\n{\"agent\":\"claude\",\"kind\":true}\n" +
		"{{tone}} stays\n" +
		"2\n" +
		"```go\nfmt.Println(\"kind\")\n```"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// A block that does not parse is refused as the template is read; one that
// throws, or runs too long, fails the call, saying which block.
func TestScriptsThatFail(t *testing.T) {
	if _, err := Parse("bad", []byte("```js\nlet = ;\n```\n")); err == nil || !strings.Contains(err.Error(), "SyntaxError") {
		t.Fatalf("a block that does not parse: %v", err)
	}
	tm, err := Parse("throws", []byte("a\n```js\nthrow new Error(\"no\")\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.Expand(t.Context(), "", nil); err == nil || !strings.Contains(err.Error(), "block 1: no") {
		t.Fatalf("a block that throws: %v", err)
	}
	loops, err := Parse("loops", []byte("```js run\nfor (;;) {}\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := loops.Expand(ctx, "", nil); err == nil || !strings.Contains(err.Error(), "ran too long") {
		t.Fatalf("a block that never ends: %v", err)
	}
}

// A name a ```js run block defines where no later piece sees it, as let
// and const do, or as anything one that awaits does, is refused where a
// later piece reads it, saying how to share it; var, function and
// globalThis share as before, and a name read only in the block, in a
// string, as a property or as a key, or one a block or an argument has,
// is no matter.
func TestRunBlocksShareWhatTheyCan(t *testing.T) {
	refused := map[string]string{
		"```js run\nconst lines = 2;\n```\n{{lines}}":                        "{{lines}}: block 1 defines lines with const, which only that block sees; define it with var, or set globalThis.lines = …",
		"```js run\nlet n = 2;\n```\n{{n * 2}}":                              "{{n * 2}}: block 1 defines n with let",
		"```js run\nvar x = await Promise.resolve(1);\n```\n{{x}}":           "{{x}}: block 1 awaits or returns, so it runs as a function and its var x is its own; set globalThis.x = … in it to share it",
		"```js run\nfunction f() {}\nawait 0;\n```\n{#if f()}y{/if}":         "{#if f()}: block 1 awaits",
		"```js run\nconst {a, b: c} = {a: 1, b: 2};\n```\n```js\nc + 1\n```": "block 2: block 1 defines c with const",
		"```js run\nconst k = 1;\n```\n{#each [k] as v}{{v}}{/each}":         "{#each [k] as v}: block 1",
		"```js run\nconst k = 1;\n```\n{#match `${k}`}{:_}y{/match}":         "{#match `${k}`}: block 1",
	}
	for body, want := range refused {
		if _, err := Parse("t", []byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", body, err, want)
		}
	}
	fine := map[string]string{
		"```js run\nvar x = 1;\nfunction f() { return 2 }\n```\n{{x + f()}}":                       "3",
		"```js run\nglobalThis.x = await Promise.resolve(4);\n```\n{{x}}":                          "4",
		"```js run\nconst y = await Promise.resolve(5);\nglobalThis.x = y;\n```\n{{x}}":            "5",
		"```js run\nconst n = 1;\n```\n{{ \"n\" + `n` + ({n: 2}).n }}":                             "nn2",
		"```js run\nconst i = 9;\n```\n{#each [1, 2] as i}{{i}}{/each}":                            "12",
		"---\nargs:\n  - name: file\n---\n```js run\nconst file = 0;\n```\n{{file}}":               "",
		"```js run\nconst n = 1;\n```\n```js run\nvar n = 2;\n```\n{{n}}":                          "2",
		"```js run\nconst tmp = 3;\nvar out = tmp * 2;\n```\n{{out}}":                              "6",
		"```js run\nfor (const t of [1]) {}\n```\n{{ [1, 2].map(t => t * 2).join(\",\") }}":        "2,4",
		"```js run\nconst x = 1;\n```\n{{ [3].map(function (x) { return x }).concat([]) }}":        "[3]",
		"```js run\nconst e = 1;\n```\n{{ (() => { try { throw 4 } catch (e) { return e } })() }}": "4",
	}
	for body, want := range fine {
		tm, err := Parse("t", []byte(body))
		if err != nil {
			t.Errorf("%q: %v", body, err)
			continue
		}
		if got, err := tm.Expand(t.Context(), "", nil); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", body, got, err, want)
		}
	}
}

// A ```js run block, in the global scope, reads the arguments and the
// built-in variables by name, as every other piece does, whether it
// awaits or not.
func TestRunBlocksReadTheArgumentsByName(t *testing.T) {
	tm, err := Parse("t", []byte("---\nargs:\n  - name: file\n---\n```js run\nglobalThis.up = file.toUpperCase() + date;\n```\n```js run\nglobalThis.who = await Promise.resolve(agent + \"!\");\n```\n{{up}} {{who}}"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tm.Expand(t.Context(), "a.go", map[string]string{"date": "2026", "agent": "claude"})
	if err != nil || got != "A.GO2026 claude!" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A block returns or awaits only at its top: a return or await in a
// function it defines, a string or a comment does not make it run as a
// function, so a run block of helpers shares them, and a block of a value
// gives its last expression's.
func TestReturnInAFunctionIsTheFunctions(t *testing.T) {
	for code, want := range map[string]bool{
		"function f() { return 1 }\nf()":           false,
		"const g = async () => { await 0 }":        false,
		"const o = { m() { return 1 } }":           false,
		"class C { m(x) { return x } }":            false,
		"// return\n'await'":                       false,
		"`${'return'}`":                            false,
		"if (x) { return 1 }":                      true,
		"for (const x of xs) { await x }":          true,
		"for await (const x of xs) {}":             true,
		"try { await f() } catch (e) { return e }": true,
		"return `${await f()}`":                    true,
		"x.return":                                 false,
	} {
		if got := needsFunction(code); got != want {
			t.Errorf("needsFunction(%q) = %v, want %v", code, got, want)
		}
	}
	tm, err := Parse("t", []byte("```js\nfunction f() { return 7 }\nf()\n```"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tm.Expand(t.Context(), "", nil); err != nil || got != "7" {
		t.Fatalf("a block of a value with a function: %q, %v", got, err)
	}
}
