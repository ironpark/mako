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
