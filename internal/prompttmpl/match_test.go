package prompttmpl

import (
	"strings"
	"testing"
)

const matching = `---
args:
  - name: tone
    options: [kind, blunt, plain]
    default: plain
  - name: depth
    type: integer
    default: 1
  - name: tests
    type: boolean
    default: "no"
---
Review it.
{#match tone}
{:kind | "plain"}
Be encouraging.
{:blunt}
Be direct, {{agent}}.
{/match}
{#match depth}
{:1..=2}
Read it.
{:3..}
Read every line,
  and every caller.
{:else}
Skim it.
{/match}
{#match tests}{:true}Run the tests {#match agent}{:claude}with /test{:_}as usual{/match}.
{:false}{/match}
Read it {#match tone}{:blunt}bluntly{:else}gently{/match}.`

// A block gives the text of its first arm whose pattern matches, words,
// quoted texts, ranges, _ and else; a tag alone on its line takes the
// line, and one within a line leaves the text around it.
func TestMatchPicksTheArm(t *testing.T) {
	tm, err := Parse("m", []byte(matching))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ args, want string }{
		{"", "Review it.\nBe encouraging.\nRead it.\nRead it gently."},
		{"blunt 5 yes", "Review it.\nBe direct, claude.\nRead every line,\n  and every caller.\nRun the tests with /test.\nRead it bluntly."},
		{"kind 0", "Review it.\nBe encouraging.\nSkim it.\nRead it gently."},
	} {
		got, err := tm.Expand(t.Context(), c.args, map[string]string{"agent": "claude"})
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%q gives\n%q, want\n%q", c.args, got, c.want)
		}
	}
	got, _ := tm.Expand(t.Context(), "blunt 2 yes", map[string]string{"agent": "codex"})
	if !strings.Contains(got, "Run the tests as usual.") {
		t.Errorf("a nested block's _ not taken: %q", got)
	}
}

// What a block may not be: its values not all covered, a pattern its
// subject cannot match, a subject not known, text before its first arm,
// a tag out of place, unclosed.
func TestMatchRefusesWhatIsWrong(t *testing.T) {
	front := "---\nargs:\n  - name: tone\n    options: [kind, blunt]\n    required: true\n  - name: n\n    type: integer\n---\n"
	for body, want := range map[string]string{
		"{#match tone}{:kind}a{/match}":         `no arm matches "blunt"`,
		"{#match tone}{:kind}a{:nice}b{/match}": `"nice" is not one of kind, blunt`,
		"{#match n}{:a}b{:_}c{/match}":          `"a" is not a valid integer`,
		"{#match n}{:1..3}b{/match}":            "add {:else} for the rest",
		"{#match who}{:_}a{/match}":             "no argument",
		"{#match tone}hello{:_}a{/match}":       "text before its first",
		"{#match tone}{:_}a":                    "has no {/match}",
		"{#match tone}{/match}":                 "has no arms",
		"a {/match}":                            "{/match} with no {#match}",
		"{#match tone}{:kind |}a{:_}b{/match}":  "is not a pattern",
	} {
		_, err := Parse("bad", []byte(front+body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}

// A block's tags are found to colour, their lines with those alone on
// one; a {{name}} in an arm is a placeholder still.
func TestTags(t *testing.T) {
	body := "a\n{#match tone}\n{: kind } {{x}}\n{/match}"
	tags := Tags(body)
	if len(tags) != 3 || tags[0].Kind != TagMatch || tags[0].Arg != "tone" || tags[1].Kind != TagArm || tags[1].Arg != "kind" || tags[2].Kind != TagEnd {
		t.Fatalf("tags %+v", tags)
	}
	if body[tags[0].From:tags[0].To] != "{#match tone}\n" || body[tags[1].From:tags[1].To] != "{: kind } " {
		t.Fatalf("what the tags take: %q %q", body[tags[0].From:tags[0].To], body[tags[1].From:tags[1].To])
	}
	if p := Placeholders(body); len(p) != 1 || p[0].Name != "x" {
		t.Fatalf("placeholders %+v", p)
	}
}

// A block's subject is JavaScript when it names no argument or built-in
// variable: it reads args and vars, and what the scripts before it
// defined, braces and quotes in it and all; it covers its values with
// true and false, or {:else}. A brace that begins no tag is text.
func TestMatchOnAnExpression(t *testing.T) {
	src := "---\nargs: [{name: depth, type: integer, default: 1}]\n---\n" +
		"```js run\nvar deep = n => n > 2;\n```\n" +
		"{#match deep(args.depth)}{:true}closely{:false}quickly{/match}, " +
		"{#match ({\"a\": \"}\"}).a + vars.agent}{:\"}claude\"}braced{:else}not{/match}; " +
		`{"json": {"stays": true}} {:as text}`
	tm, err := Parse("e", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if !tm.HasScripts() {
		t.Fatal("an expression is no JavaScript")
	}
	for args, want := range map[string]string{
		"":  `quickly, braced; {"json": {"stays": true}} {:as text}`,
		"5": `closely, braced; {"json": {"stays": true}} {:as text}`,
	} {
		got, err := tm.Expand(t.Context(), args, map[string]string{"agent": "claude"})
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%q gives %q, want %q", args, got, want)
		}
	}
	for body, want := range map[string]string{
		"{#match nope}{:_}a{/match}":              "no argument",
		"{#match args.depth +}{:_}a{/match}":      "SyntaxError",
		"{#match args.depth * 2}{:1..3}a{/match}": "add {:else}",
		"{#match args.depth > 1}{:true}a{/match}": "add {:else}",
	} {
		if _, err := Parse("bad", []byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}
