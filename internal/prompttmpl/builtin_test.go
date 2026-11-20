package prompttmpl

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateReference = flag.Bool("update", false, "write the built-in //template's language reference into docs/usage.md")

// Every built-in template reads, and is listed, marked built in.
func TestBuiltinsRead(t *testing.T) {
	got := BuiltinTemplates()
	if len(got) != len(builtinSources) {
		t.Fatalf("%d of %d built-in templates read", len(got), len(builtinSources))
	}
	for _, b := range got {
		if !b.Builtin || b.Path != "" || BuiltinTemplate(b.Name) != b {
			t.Errorf("%s: %+v", b.Name, b)
		}
		if _, err := Parse(b.Name, BuiltinSource(b.Name)); err != nil {
			t.Errorf("%s: %v", b.Name, err)
		}
	}
	m := BuiltinTemplate(MakerName)
	if m == nil || len(m.Args) != 1 || m.Args[0].Name != "text" || m.Args[0].Type != Text || !m.Args[0].Required || !m.Args[0].Rest {
		t.Fatalf("//%s takes %+v", MakerName, m)
	}
}

// //template, given a description as Chat gives it, unquoted, makes a
// prompt with the description and the language reference whole, its
// syntax as text.
func TestMakerExpands(t *testing.T) {
	m := BuiltinTemplate(MakerName)
	desc := `review a Go file for races; don't nitpick, "strict" as a choice`
	got, err := m.Expand(t.Context(), desc, map[string]string{"agent": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"save_template", "\n" + desc + "\n", strings.TrimRight(reference(t), "\n"), "{{file}}", "{#match tone}", "```js run", "`{#raw}` … `{/raw}`"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt has no %.60q", want)
		}
	}
	if strings.Contains(got, "{{text}}") || strings.HasSuffix(got, "{/raw}") {
		t.Errorf("the prompt was not filled in:\n%s", got[len(got)-200:])
	}
	if _, err := m.Bind(""); err == nil {
		t.Fatal("//template took no description")
	}
}

// reference is the language reference the built-in //template gives the
// agent: what its raw block holds.
func reference(t *testing.T) string {
	ref := Reference()
	if ref == "" {
		t.Fatal("the built-in //template has no raw block of the reference")
	}
	return ref
}

// docs/usage.md has the reference the built-in //template gives the agent,
// word for word, between its markers; -update writes it there.
func TestReferenceInDocs(t *testing.T) {
	const path = "../../docs/usage.md"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	open := strings.Index(doc, "<!-- prompttmpl:reference")
	end := strings.Index(doc, "<!-- /prompttmpl:reference -->")
	if open < 0 || end < open {
		t.Fatal("docs/usage.md has no prompttmpl:reference markers")
	}
	start := open + strings.Index(doc[open:], "\n") + 1
	want := reference(t)
	if doc[start:end] == want {
		return
	}
	if *updateReference {
		if err := os.WriteFile(path, []byte(doc[:start]+want+doc[end:]), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("docs/usage.md's template reference differs from internal/prompttmpl/builtin/maker.md's; edit the latter and run go test ./internal/prompttmpl -run TestReferenceInDocs -update")
}

// A file of the user's stands in for a built-in of its name, even one
// that does not read; with none, Find and LoadAll give the built-in.
func TestUserFileOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	names := func(ts []*Template) (out []string) {
		for _, x := range ts {
			out = append(out, x.Name+map[bool]string{true: "*"}[x.Builtin])
		}
		return out
	}
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("A"), 0o600)
	got, _ := LoadAll(dir)
	if strings.Join(names(got), " ") != "a "+MakerName+"*" {
		t.Fatalf("listed %v", names(got))
	}
	if f, err := Find(dir, MakerName); err != nil || !f.Builtin {
		t.Fatalf("found %+v %v", f, err)
	}
	if _, err := Find(dir, "none"); err == nil {
		t.Fatal("found a template no one has")
	}

	_ = os.WriteFile(filepath.Join(dir, MakerName+".md"), []byte("Mine: {{nope}}"), 0o600)
	got, broken := LoadAll(dir)
	if strings.Join(names(got), " ") != "a" || len(broken) != 1 {
		t.Fatalf("with a broken copy: %v %v", names(got), broken)
	}
	if _, err := Find(dir, MakerName); err == nil {
		t.Fatal("a broken copy fell back to the built-in")
	}

	_ = os.WriteFile(filepath.Join(dir, MakerName+".md"), []byte("Mine"), 0o600)
	got, _ = LoadAll(dir)
	if strings.Join(names(got), " ") != "a "+MakerName {
		t.Fatalf("with a copy: %v", names(got))
	}
	if f, err := Find(dir, MakerName); err != nil || f.Builtin || f.Body != "Mine" {
		t.Fatalf("found %+v %v", f, err)
	}
	if BuiltinTemplate(MakerName).Body == "Mine" {
		t.Fatal("the copy changed the built-in")
	}
}
