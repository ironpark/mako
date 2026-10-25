package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/ironpark/sanbo/internal/syntax"
)

// samples is a small, valid program per grammar, with one capture name
// its highlights must produce.
var samples = map[string]struct{ src, want string }{
	"go":              {"package main\n\nimport \"fmt\"\n\n// main says hi.\nfunc main() {\n\tx := []int{1, 2}\n\tfmt.Println(\"hi\", x[0], true)\n}\n", "function"},
	"rust":            {"use std::fmt;\n\n/// Doc.\nfn main() {\n    let v: Vec<u8> = vec![1, 2];\n    println!(\"{}\", v.len());\n}\n", "function"},
	"python":          {"import os\n\n\nclass A:\n    def f(self, x: int) -> str:\n        \"\"\"Doc.\"\"\"\n        return f\"{x} {os.sep}\"  # c\n", "function"},
	"javascript":      {"import x from \"y\";\n// c\nconst f = async (a, b = 2) => {\n  const el = <div className=\"a\">{a}</div>;\n  return `t${b}`;\n};\nclass A extends B { m() { return this.x; } }\n", "function"},
	"typescript":      {"interface P { name: string; age?: number }\nexport function f<T>(p: P, t: T): T {\n  const n: number = 1 as number;\n  return t;\n}\nenum E { A = 1 }\n", "type"},
	"tsx":             {"type Props = { title: string };\nexport const C = ({ title }: Props) => <h1 className=\"t\">{title}</h1>;\n", "type"},
	"json":            {"{\"a\": [1, 2.5, true, null], \"b\": {\"c\": \"d\"}}\n", "string"},
	"bash":            {"#!/bin/sh\nset -eu\nfor f in *.go; do\n  echo \"$f ${HOME}\" | grep -v x > /dev/null\ndone\ncat <<EOF\nhi\nEOF\n", "function"},
	"css":             {"@media (min-width: 10px) {\n  .a > #b:hover, a[href] { color: #fff; margin: 0 auto !important; }\n}\n", "property"},
	"yaml":            {"# c\nkey: value\nlist:\n  - 1\n  - \"two\"\nmap: {a: b}\nblock: |\n  text\n", "property"},
	"c":               {"#include <stdio.h>\n\n/* c */\nstatic int add(int a, int b) { return a + b; }\nint main(void) { printf(\"%d\\n\", add(1, 2)); return 0; }\n", "function"},
	"cpp":             {"#include <vector>\n\nnamespace n {\ntemplate <typename T> class A : public B {\n public:\n  auto f() const -> T { return std::vector<T>{}.size(); }\n};\n}  // namespace n\n", "type"},
	"markdown":        {"# Title\n\nSome *text* and `code`.\n\n- item\n- item\n\n```go\nfunc main() {}\n```\n\n> quote\n", "text.title"},
	"markdown_inline": {"Some *em* and **strong** with `code` and [a link](https://x.y).", "text.emphasis"},
	"diff":            {"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n context\n-old\n+new\n", "diff.plus"},
	"dockerfile":      {"FROM golang:1.22 AS build\n# c\nRUN go build -o /app .\nENV A=b\nCOPY --from=build /app /app\nCMD [\"/app\"]\n", "keyword"},
	"java":            {"package a;\n\nimport java.util.List;\n\npublic class A<T> {\n  @Override\n  public String toString() { return \"a\" + List.of(1).size(); }\n}\n", "type"},
	"ruby":            {"require 'set'\n\nclass A < B\n  def f(x, *rest)\n    @y = x.map { |v| v * 2 } # c\n    \"#{x}\"\n  end\nend\n", "function.method"},
	"php":             {"<?php\nnamespace A;\n\nclass B {\n  public function f(int $x): string { return \"v{$x}\"; }\n}\necho (new B)->f(1);\n", "function.method"},
}

func TestBuiltinLanguages(t *testing.T) {
	r, err := syntax.NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if len(Languages()) != len(samples) {
		t.Errorf("%d languages, %d samples", len(Languages()), len(samples))
	}
	for _, bl := range Languages() {
		t.Run(bl.Grammar, func(t *testing.T) {
			wasm, err := bl.Wasm()
			if err != nil {
				t.Fatal(err)
			}
			if missing, err := r.MissingImports(wasm); err != nil || len(missing) > 0 {
				t.Fatalf("missing imports %v (%v)", missing, err)
			}
			l, err := r.LoadLanguage(bl.Grammar, wasm)
			if err != nil {
				t.Fatal(err)
			}
			if bl.Highlights == "" {
				t.Fatal("no highlights.scm")
			}
			hq, err := l.NewQuery(bl.Highlights)
			if err != nil {
				t.Fatal(err)
			}
			defer hq.Close()
			if bl.Injections != "" {
				iq, err := l.NewQuery(bl.Injections)
				if err != nil {
					t.Fatalf("injections: %v", err)
				}
				iq.Close()
			}

			s, ok := samples[bl.Grammar]
			if !ok {
				t.Fatal("no sample")
			}
			tree, err := l.Parse([]byte(s.src), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tree.Close()
			if sexpr := tree.RootSExpr(); strings.Contains(sexpr, "ERROR") || strings.Contains(sexpr, "MISSING") {
				t.Fatalf("parse error: %s", sexpr)
			}
			caps, err := hq.Captures(tree, []byte(s.src), 0, len(s.src))
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, c := range caps {
				names[hq.CaptureNames()[c.Index]] = true
			}
			if !names[s.want] {
				t.Errorf("no @%s capture; got %v", s.want, names)
			}
			if len(syntax.Spans(caps)) == 0 {
				t.Error("no spans")
			}
		})
	}
}

func TestForPath(t *testing.T) {
	for p, want := range map[string]string{
		"main.go":          "go",
		"a/b/App.TSX":      "tsx",
		"Dockerfile":       "dockerfile",
		"x/Gemfile":        "ruby",
		"/home/u/.bashrc":  "bash",
		"lib.h":            "c",
		"lib.hpp":          "cpp",
		"README.md":        "markdown",
		"build.dockerfile": "dockerfile",
	} {
		l, ok := ForPath(p)
		if !ok || l.Grammar != want {
			t.Errorf("%s: got %q %v, want %s", p, l.Grammar, ok, want)
		}
	}
	for _, p := range []string{"go", "notes.txt", "Makefile"} {
		if l, ok := ForPath(p); ok {
			t.Errorf("%s: got %s", p, l.Grammar)
		}
	}
	if l, ok := ByGrammar("markdown_inline"); !ok || len(l.Suffixes) != 0 {
		t.Errorf("markdown_inline: %+v %v", l, ok)
	}
}
