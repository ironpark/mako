package prompttmpl

import (
	"strings"
	"testing"
)

// A raw block keeps what it holds as it is written: names defined
// nowhere, tags, a match that covers nothing, a script's fences, and a
// raw block of its own, which pairs with its own end. Its tags, alone on
// their lines, take those lines.
func TestRawKeepsItsText(t *testing.T) {
	inner := "{{nope}} {#if}{/each} {#match x}{/match}\n" +
		"```js\nthrow new Error(\"never\")\n```\n" +
		"`{#raw}` … `{/raw}` and {:else}\n"
	src := "---\nargs: [{name: who}]\n---\nHi {{who}}.\n{#raw}\n" + inner + "{/raw}\nBye {{who}}."
	tm, err := Parse("raw", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if tm.HasScripts() {
		t.Fatal("a script in a raw block counts as one")
	}
	got, err := tm.Expand(t.Context(), "Ada", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Hi Ada.\n" + inner + "Bye Ada."; got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	body := tm.Body
	if hs := Placeholders(body); len(hs) != 2 {
		t.Fatalf("placeholders %v", hs)
	}
	if ss := Scripts(body); len(ss) != 0 {
		t.Fatalf("scripts %v", ss)
	}
	tags := Tags(body)
	if len(tags) != 2 || tags[0].Kind != TagRaw || tags[1].Kind != TagEnd || tags[1].Block != TagRaw {
		t.Fatalf("tags %v", tags)
	}
	// Inline, it is the text between its tags.
	tm, err = Parse("inline", []byte("a {#raw}{{b}}{/raw} c"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := tm.Expand(t.Context(), "", nil); got != "a {{b}} c" {
		t.Fatalf("inline: %q", got)
	}
}

// A raw block not closed, one given something after raw, and an end
// with none open are refused; a script before a raw block still runs.
func TestRawThatIsWrong(t *testing.T) {
	for body, want := range map[string]string{
		"{#raw}\n{{x}}":       "{#raw} has no {/raw}",
		"{#raw x}a{/raw}":     "a raw block takes nothing",
		"a{/raw}":             "{/raw} with no block open",
		"{#raw}{#raw}a{/raw}": "{#raw} has no {/raw}",
	} {
		if _, err := Parse("t", []byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
	tm, err := Parse("t", []byte("```js\n1 + 1\n```\n{#raw}{{x}}{/raw}"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tm.Expand(t.Context(), "", nil); err != nil || got != "2\n{{x}}" {
		t.Fatalf("%q %v", got, err)
	}
}

// Within a raw block, an editor completes nothing but its end.
func TestCompleteInRaw(t *testing.T) {
	if _, ok := Complete("{#raw}\n{{na"); ok {
		t.Fatal("a name completed in a raw block")
	}
	c, ok := Complete("{#raw}\nx {/")
	if !ok || c.Kind != CompleteTag || c.Open != TagRaw {
		t.Fatalf("its end: %+v %v", c, ok)
	}
	if c, ok := Complete("{#raw}x{/raw} {{na"); !ok || c.Kind != CompleteName {
		t.Fatalf("after it: %+v %v", c, ok)
	}
}
