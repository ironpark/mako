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
{{match tone}}
kind | "plain" => Be encouraging.
blunt => "Be direct, {{agent}}.",
{{end}}
{{match depth}}
  1..=2 => Read it.
  3.. =>
    Read every line,
      and every caller.
  _ => Skim it.
{{end}}
{{match tests}}
true => Run the tests {{match agent}}
  claude => with /test.
  _ => as usual.
{{end}}
false =>
{{end}}
Done.`

// A block gives the text of its first arm whose pattern matches, words,
// quoted texts, ranges and _, its other arms and its lines leaving nothing.
func TestMatchPicksTheArm(t *testing.T) {
	tm, err := Parse("m", []byte(matching))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ args, want string }{
		{"", "Review it.\nBe encouraging.\nRead it.\nDone."},
		{"blunt 5 yes", "Review it.\nBe direct, claude.\nRead every line,\n  and every caller.\nRun the tests with /test.\nDone."},
		{"kind 0", "Review it.\nBe encouraging.\nSkim it.\nDone."},
	} {
		got, err := tm.Expand(c.args, map[string]string{"agent": "claude"})
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%q gives\n%q, want\n%q", c.args, got, c.want)
		}
	}
	got, _ := tm.Expand("blunt 2 yes", map[string]string{"agent": "codex"})
	if !strings.Contains(got, "Run the tests as usual.") {
		t.Errorf("a nested block's _ not taken: %q", got)
	}
}

// What a block may not be: its values not all covered, a pattern its
// subject cannot match, a subject not known, an arm not an arm, unclosed.
func TestMatchRefusesWhatIsWrong(t *testing.T) {
	front := "---\nargs:\n  - name: tone\n    options: [kind, blunt]\n    required: true\n  - name: n\n    type: integer\n---\n"
	for body, want := range map[string]string{
		"{{match tone}}\nkind => a\n{{end}}":            `no arm matches "blunt"`,
		"{{match tone}}\nkind => a\nnice => b\n{{end}}": `"nice" is not one of kind, blunt`,
		"{{match n}}\na => b\n_ => c\n{{end}}":          `"a" is not a valid integer`,
		"{{match n}}\n1..3 => b\n{{end}}":               "add an arm _ =>",
		"{{match who}}\n_ => a\n{{end}}":                "no argument",
		"{{match tone}}\nhello\n_ => a\n{{end}}":        "is not an arm",
		"{{match tone}}\n_ => a\n":                      "has no {{end}}",
		"a {{end}}":                                     "{{end}} with no {{match}}",
	} {
		_, err := Parse("bad", []byte(front+body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
	if _, err := Parse("bad", []byte("---\nargs: [{name: end}]\n---\n")); err == nil {
		t.Error("an argument named end")
	}
}

// A block's tags are found to colour; {{end}} is no placeholder.
func TestTags(t *testing.T) {
	body := "{{match tone}}\n_ => {{x}}\n{{ end }}"
	tags := Tags(body)
	if len(tags) != 2 || tags[0].Subject != "tone" || body[tags[1].Start:tags[1].End] != "{{ end }}" {
		t.Fatalf("tags %+v", tags)
	}
	if p := Placeholders(body); len(p) != 1 || p[0].Name != "x" {
		t.Fatalf("placeholders %+v", p)
	}
}
