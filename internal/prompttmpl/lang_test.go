package prompttmpl

import (
	"strings"
	"testing"
)

// The blocks of Svelte's markup, its values in double braces: each over a
// list, with a const and an index, and an else for an empty list; if,
// else if and else; snippets rendered with what they are given.
func TestIfEachConstSnippet(t *testing.T) {
	src := "---\nargs:\n  - name: n\n    type: integer\n    default: 2\n  - name: strict\n    type: boolean\n    default: \"no\"\n---\n" +
		"```js run\nvar boxes = [{width: 2, height: 3}, {width: 4, height: 5}];\n```\n" +
		"{#each boxes as box, i}\n" +
		"    {@const area = box.width * box.height}\n" +
		"    {{i + 1}}. {{box.width}} * {{box.height}} = {{area}}\n" +
		"{/each}\n" +
		"{#each [] as x}\n{{x}}\n{:else}\nnone\n{/each}\n" +
		"{#each n as k}{{k}}{/each}\n" +
		"{#if n > 2}\nmany\n{:else if n === 2}\ntwo\n{:else}\nfew\n{/if}\n" +
		"{#snippet rules(strict)}\n- Keep functions short.\n{#if strict}\n- No TODOs.\n{/if}\n{/snippet}\n" +
		"{@render rules(true)}\n" +
		"Then:\n" +
		"{@render rules(strict)}\n" +
		"Literal {{.Left}} and {\"json\": {\"stays\": true}}."
	tm, err := Parse("lang", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tm.Expand(t.Context(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "    1. 2 * 3 = 6\n" +
		"    2. 4 * 5 = 20\n" +
		"none\n" +
		"01\n" +
		"two\n" +
		"- Keep functions short.\n- No TODOs.\n" +
		"Then:\n" +
		"- Keep functions short.\n" +
		"Literal {{.Left}} and {\"json\": {\"stays\": true}}."
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// A block's mistakes are found as the template is read: one not closed,
// closed by another's end, an else where it has none, a name defined
// nowhere, an expression that does not parse; a name the block defines is
// known within it and not after.
func TestBlocksThatAreWrong(t *testing.T) {
	for body, want := range map[string]string{
		"{#if x}a":                             "has no {/if}",
		"{#if true}a{/each}":                   "{/each} closes {#if true}",
		"{#snippet s()}{:else}{/snippet}":      "{:else} in {#snippet",
		"{#each [1] as a}{{a}}{/each}{{a}}":    "{{a}}: no argument",
		"{#if 1 +}a{/if}":                      "{#if 1 +}: SyntaxError",
		"{#each [1]}a{/each}":                  "an each is list as item",
		"{@const}":                             "needs what to const",
		"{#if true}{:else}a{:else if x}b{/if}": "after {:else}",
	} {
		_, err := Parse("bad", []byte(body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}

// What a prompt defines is listed for an editor to colour.
func TestLocals(t *testing.T) {
	got := Locals("{#each xs as {a, b: c}, i}{@const d = 1}{/each}{#snippet s(p, q = 2)}{/snippet}\n```js run\nvar v = 1\n```\n")
	if strings.Join(got, ",") != "a,c,d,i,p,q,s,v" {
		t.Fatalf("locals %v", got)
	}
}

// The README's examples read and fill in as it says.
func TestTheReadmesExamples(t *testing.T) {
	src := "---\nargs:\n  - name: depth\n    type: integer\n    default: 2\n  - name: tone\n    options: [kind, blunt, plain]\n    default: kind\n  - name: file\n---\n" +
		"```js run\nvar files = [\"a/b.go\", \"c.go\"];\nvar lines = n => n === 1 ? \"one line\" : n + \" lines\";\n```\n" +
		"{#if args.depth > 2}\nRead every line.\n{:else if args.depth === 2}\nRead it.\n{:else}\nSkim it.\n{/if}\n" +
		"{#each files as file, i}\n  {@const name = file.split(\"/\").pop()}\n  {{i + 1}}. {{name}}\n{:else}\nNo files.\n{/each}\n" +
		"{#match tone}\n{:kind | \"plain\"}\nBe encouraging.\n{:blunt}\nBe direct.\n{/match}\n" +
		"Read it {#match args.depth}{:1..=2}quickly{:else}closely{/match}.\n" +
		"Review {{file}}, about {{lines(depth * 40)}}."
	tm, err := Parse("readme", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tm.Expand(t.Context(), "file=x.go", map[string]string{"agent": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	want := "Read it.\n  1. b.go\n  2. c.go\nBe encouraging.\nRead it quickly.\nReview x.go, about 80 lines."
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}
