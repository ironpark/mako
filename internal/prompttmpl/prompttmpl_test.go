package prompttmpl

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const review = `---
description: Review a file
agents: [claude, codex:fast]
args:
  - name: file
    type: string
    required: true
  - name: depth
    type: integer
    default: 2
  - name: tone
    options: [kind, blunt]
    default: kind
  - name: strict
    type: boolean
    default: "no"
  - name: notes
    type: text
---
Review {{file}}, {{ depth }} deep, {{tone}} (strict: {{strict}}).
{{notes}}
As {{agent}} on {{model}}. {{ .Left }} {{not closed
`

func parse(t *testing.T, src string) *Template {
	t.Helper()
	tm, err := Parse("review", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestParseReadsFrontMatterAndFillsTypes(t *testing.T) {
	tm := parse(t, review)
	if tm.Description != "Review a file" || len(tm.Args) != 5 {
		t.Fatalf("read %+v", tm)
	}
	if tm.Args[2].Type != Choice {
		t.Errorf("an argument with options is a choice, got %q", tm.Args[2].Type)
	}
	if *tm.Args[3].Default != "false" {
		t.Errorf("a boolean default is spelled as the prompt has it, got %q", *tm.Args[3].Default)
	}
	if got := tm.Usage(); got != "<file:string> [depth:integer=2] [tone:kind|blunt=kind] [strict:boolean=false] [notes:text]" {
		t.Errorf("usage %q", got)
	}
}

func TestExpandFillsArgumentsAndBuiltins(t *testing.T) {
	tm := parse(t, review)
	got, err := tm.Expand(`main.go tone=BLUNT notes="line one
line two" 3`, map[string]string{"agent": "claude", "model": "opus"})
	if err != nil {
		t.Fatal(err)
	}
	want := "Review main.go, 3 deep, blunt (strict: false).\nline one\nline two\nAs claude on opus. {{ .Left }} {{not closed"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestBindRefusesWhatTheTypesDoNotTake(t *testing.T) {
	tm := parse(t, review)
	for args, kind := range map[string]ErrorKind{
		``:                   Missing,
		`a.go depth=two`:     BadValue,
		`a.go tone=rude`:     BadValue,
		`a.go strict=maybe`:  BadValue,
		`a.go colour=red`:    BadValue, // no argument has the name, so it is depth's value
		`a.go 1 kind no n x`: Extra,
		`a.go file=b.go`:     Twice,
		`"a.go`:              Unquoted,
		`file="a
b"`: BadValue, // a string is one line
	} {
		_, err := tm.Bind(args)
		var ae *ArgError
		if !errors.As(err, &ae) || ae.Kind != kind {
			t.Errorf("%q: got %v, want kind %d", args, err, kind)
		}
	}
	// A quoted = is a value, not a name.
	values, err := tm.Bind(`"x=y.go"`)
	if err != nil || values["file"] != "x=y.go" {
		t.Errorf("got %v, %v", values, err)
	}
}

func TestParseRefusesWhatIsWrong(t *testing.T) {
	for name, src := range map[string]string{
		"unknown variable": "Hi {{who}}",
		"builtin shadowed": "---\nargs: [{name: agent}]\n---\n",
		"choice no option": "---\nargs: [{name: a, type: choice}]\n---\n",
		"no such type":     "---\nargs: [{name: a, type: date}]\n---\n",
		"bad default":      "---\nargs: [{name: a, type: integer, default: x}]\n---\n",
		"unknown field":    "---\nloops: true\n---\n",
		"not closed":       "---\ndescription: x\n",
		"twice":            "---\nargs: [{name: a}, {name: a}]\n---\n",
	} {
		if _, err := Parse("t", []byte(src)); err == nil {
			t.Errorf("%s: read without an error", name)
		}
	}
	if tm, err := Parse("t", []byte("Just {{project}}\n")); err != nil || tm.Body != "Just {{project}}\n" {
		t.Errorf("a template without front matter: %v, %v", tm, err)
	}
	if tm, err := Parse("t", []byte("---\n---\nBody")); err != nil || tm.Body != "Body" {
		t.Errorf("empty front matter: %v, %v", tm, err)
	}
}

func TestForLimitsAgents(t *testing.T) {
	tm := parse(t, review)
	for _, c := range []struct {
		agent, profile string
		want           bool
	}{
		{"claude", "", true}, {"claude", "opus", true},
		{"codex", "fast", true}, {"codex", "", false}, {"shell", "", false},
	} {
		if got := tm.For(c.agent, c.profile); got != c.want {
			t.Errorf("%s:%s: got %v", c.agent, c.profile, got)
		}
	}
	if !(&Template{}).For("anything", "") {
		t.Error("a template naming no agents is for every agent")
	}
}

func TestParseCall(t *testing.T) {
	for draft, want := range map[string]Call{
		"//":               {},
		"//rev":            {Name: "rev"},
		"//rev a b":        {Name: "rev", Args: "a b"},
		"//rev\nmore text": {Name: "rev", Args: "more text"},
	} {
		got, ok := ParseCall(draft)
		if !ok || got != want {
			t.Errorf("%q: got %+v, %v", draft, got, ok)
		}
	}
	for _, draft := range []string{"/rev", "hi", "// rev"} {
		if _, ok := ParseCall(draft); ok {
			t.Errorf("%q read as a call", draft)
		}
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.md", "B")
	write("a.md", "A {{project}}")
	write("bad.md", "{{nope}}")
	write("skip.txt", "not a template")
	got, broken := LoadDir(dir)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("templates %v", got)
	}
	if len(broken) != 1 || broken[0].Name != "bad" || !strings.Contains(broken[0].Err.Error(), "nope") {
		t.Fatalf("broken %v", broken)
	}
	if got, broken := LoadDir(filepath.Join(dir, "missing")); len(got)+len(broken) != 0 {
		t.Fatal("a missing folder has templates")
	}
}

func TestFormatReadsBackAsItWas(t *testing.T) {
	no := "no"
	for _, tm := range []*Template{
		parse(t, review),
		{Name: "plain", Body: "Just {{project}}\n"},
		{Name: "dashes", Body: "---\nnot front matter\n"},
		{Name: "tricky", Description: "a: b # c", Agents: []string{"claude:opus"},
			Args: []Arg{{Name: "x", Type: String, Default: &no}}, Body: "{{x}}\n"},
	} {
		got, err := Parse(tm.Name, Format(tm))
		if err != nil {
			t.Fatalf("%s: %v\n%s", tm.Name, err, Format(tm))
		}
		if string(Format(got)) != string(Format(tm)) || got.Body != strings.TrimRight(tm.Body, "\n")+"\n" {
			t.Errorf("%s: read back\n%s\nfrom\n%s", tm.Name, Format(got), Format(tm))
		}
	}
}

// A backslash is itself unless it escapes a space or a quote, as in a
// Windows path.
func TestBindKeepsAWindowsPath(t *testing.T) {
	tm := parse(t, review)
	values, err := tm.Bind(`C:\src\a.go`)
	if err != nil || values["file"] != `C:\src\a.go` {
		t.Fatalf("got %v, %v", values, err)
	}
}

// The placeholders of a body are found where they are, spaces in their
// braces and all, and a brace that opens none is not one.
func TestPlaceholders(t *testing.T) {
	body := "Review {{ file }} as {{agent}}, {{ not closed"
	got := Placeholders(body)
	want := []Placeholder{{Start: 7, End: 17, Name: "file"}, {Start: 21, End: 30, Name: "agent"}}
	if !slices.Equal(got, want) {
		t.Fatalf("placeholders %v, want %v", got, want)
	}
	if !IsBuiltin("agent") || IsBuiltin("file") {
		t.Fatal("built-ins")
	}
}

// The {{ a text ends in is found with what is typed of its name, spaces
// and tabs before the name allowed; and the }} after it, with spaces.
func TestOpenPlaceholderAndClosing(t *testing.T) {
	for _, c := range []struct {
		text  string
		ok    bool
		start int
		typed string
	}{
		{"Review {{fi", true, 9, "fi"},
		{"Review {{ \t", true, 11, ""},
		{"Review {{file}} ", false, 0, ""},
		{"Review {{fi le", false, 0, ""},
		{"Review", false, 0, ""},
	} {
		start, typed, subject, ok := OpenPlaceholder(c.text)
		if ok != c.ok || ok && (start != c.start || typed != c.typed) || subject {
			t.Errorf("%q: %d %q %v %v", c.text, start, typed, subject, ok)
		}
	}
	if start, typed, subject, ok := OpenPlaceholder("{{match to"); !ok || !subject || start != 8 || typed != "to" {
		t.Errorf("a match's name: %d %q %v %v", start, typed, subject, ok)
	}
	if Closing(" }} b") != 3 || Closing("}}") != 2 || Closing(" b}}") != 0 {
		t.Fatal("closing")
	}
}
