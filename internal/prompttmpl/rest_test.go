package prompttmpl

import (
	"maps"
	"strings"
	"testing"
)

// The last argument, given rest, takes what follows the arguments before
// it as it is typed, quotes and apostrophes and all; those before it are
// given in order or by name first.
func TestRestTakesTheRest(t *testing.T) {
	tm, err := Parse("explain", []byte("---\nargs:\n  - name: file\n    required: true\n  - name: depth\n    type: integer\n    default: 2\n  - name: ask\n    type: text\n    rest: true\n---\n{{file}} {{depth}}: {{ask}}"))
	if err != nil {
		t.Fatal(err)
	}
	for args, want := range map[string]map[string]string{
		`main.go 3 why doesn't it "close"?`:   {"file": "main.go", "depth": "3", "ask": `why doesn't it "close"?`},
		`depth=4 "my file.go" a=b  c`:         {"file": "my file.go", "depth": "4", "ask": "a=b  c"},
		"main.go 1":                           {"file": "main.go", "depth": "1", "ask": ""},
		"main.go 1 two\nlines ":               {"file": "main.go", "depth": "1", "ask": "two\nlines"},
		`main.go 1 ask=given by name is text`: {"file": "main.go", "depth": "1", "ask": "ask=given by name is text"},
	} {
		got, err := tm.Bind(args)
		if err != nil || !maps.Equal(got, want) {
			t.Errorf("%q: %v %v, want %v", args, got, err, want)
		}
	}
	if _, err := tm.Bind("main.go many"); err == nil || !strings.Contains(err.Error(), "not a valid integer") {
		t.Fatalf("a word for an integer before the rest: %v", err)
	}
	if u := tm.Usage(); u != "<file:string> [depth:integer=2] [ask:text…]" {
		t.Fatalf("usage %s", u)
	}
	back, err := Parse("explain", Format(tm))
	if err != nil || !back.Args[2].Rest {
		t.Fatalf("formatted and read back: %v %+v", err, back)
	}

	// Alone, it is the whole call; a string takes no second line.
	only, err := Parse("ask", []byte("---\nargs: [{name: q, required: true, rest: true}]\n---\n{{q}}"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := only.Expand(t.Context(), `  it's "fine" `, nil); err != nil || got != `it's "fine"` {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := only.Bind(""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("nothing given: %v", err)
	}
	if _, err := only.Bind("a\nb"); err == nil {
		t.Fatal("a string took two lines")
	}
}

// Only the last argument, a string or text, takes the rest.
func TestRestThatIsWrong(t *testing.T) {
	for front, want := range map[string]string{
		"args: [{name: a, rest: true}, {name: b}]":     "only the last argument takes the rest",
		"args: [{name: a, type: integer, rest: true}]": "only a string or text takes the rest",
	} {
		if _, err := Parse("t", []byte("---\n"+front+"\n---\nx")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", front, err, want)
		}
	}
}
