// Package builtin is the grammar pack sanbo ships for languages Zed builds
// in and so has no store extensions for: tree-sitter grammars compiled to
// wasm side modules (the format syntax.Runtime.LoadLanguage loads), with
// the highlight and injection queries from each grammar's own repository.
//
// build.sh fetches the pinned grammar releases and regenerates grammars/.
// Each grammars/<name>/ directory holds the gzipped wasm, its queries and
// the grammar's LICENSE.
package builtin

//go:generate sh build.sh

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
)

//go:embed grammars
var files embed.FS

// Language is one builtin grammar and its metadata.
type Language struct {
	Name        string   // display name, e.g. "Go", "TypeScript"
	Grammar     string   // the grammar name, as in tree_sitter_<name>
	Suffixes    []string // extensions without dot ("go"), or file names, which start with "." or a capital ("Dockerfile")
	LineComment string   // line comment prefix with its trailing space, or ""
	FirstLine   string   // a regexp for a script's first line, its shebang, or ""

	// Highlights and Injections are the grammar repository's queries.
	// Like Zed's, later patterns win (paint with syntax.Spans).
	Highlights, Injections string
}

// Wasm returns the decompressed grammar module.
func (l Language) Wasm() ([]byte, error) {
	f, err := files.Open(path.Join("grammars", l.Grammar, l.Grammar+".wasm.gz"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if _, err := io.Copy(&b, zr); err != nil {
		return nil, err
	}
	return b.Bytes(), zr.Close()
}

// table lists the builtin languages; queries are filled in from files.
var table = []Language{
	{Name: "Go", Grammar: "go", Suffixes: []string{"go"}, LineComment: "// "},
	{Name: "Rust", Grammar: "rust", Suffixes: []string{"rs"}, LineComment: "// "},
	{Name: "Python", Grammar: "python", Suffixes: []string{"py", "pyi", "pyw"}, LineComment: "# ", FirstLine: `^#!.*\bpython[0-9.]*\b`},
	{Name: "JavaScript", Grammar: "javascript", Suffixes: []string{"js", "mjs", "cjs", "jsx"}, LineComment: "// ", FirstLine: `^#!.*\b(node|deno|bun)\b`},
	{Name: "TypeScript", Grammar: "typescript", Suffixes: []string{"ts", "mts", "cts"}, LineComment: "// "},
	{Name: "TSX", Grammar: "tsx", Suffixes: []string{"tsx"}, LineComment: "// "},
	{Name: "JSON", Grammar: "json", Suffixes: []string{"json", "jsonc", "json5"}},
	{Name: "Shell Script", Grammar: "bash", Suffixes: []string{"sh", "bash", "zsh", ".bashrc", ".bash_profile", ".zshrc", ".profile"}, LineComment: "# ", FirstLine: `^#!.*\b(ba|z|k)?sh\b`},
	{Name: "CSS", Grammar: "css", Suffixes: []string{"css"}},
	{Name: "YAML", Grammar: "yaml", Suffixes: []string{"yaml", "yml"}, LineComment: "# "},
	{Name: "C", Grammar: "c", Suffixes: []string{"c", "h"}, LineComment: "// "},
	{Name: "C++", Grammar: "cpp", Suffixes: []string{"cc", "cpp", "cxx", "c++", "hh", "hpp", "hxx", "h++", "ipp", "inl"}, LineComment: "// "},
	{Name: "Markdown", Grammar: "markdown", Suffixes: []string{"md", "markdown", "mdown", "mkd"}},
	// Only reached through markdown's injections of (inline) nodes.
	{Name: "Markdown Inline", Grammar: "markdown_inline"},
	{Name: "Diff", Grammar: "diff", Suffixes: []string{"diff", "patch"}},
	{Name: "Dockerfile", Grammar: "dockerfile", Suffixes: []string{"Dockerfile", "Containerfile", "dockerfile"}, LineComment: "# "},
	{Name: "Java", Grammar: "java", Suffixes: []string{"java"}, LineComment: "// "},
	{Name: "Ruby", Grammar: "ruby", Suffixes: []string{"rb", "rake", "gemspec", "ru", "Gemfile", "Rakefile"}, LineComment: "# ", FirstLine: `^#!.*\bruby\b`},
	{Name: "PHP", Grammar: "php", Suffixes: []string{"php"}, LineComment: "// ", FirstLine: `^#!.*\bphp\b`},
}

var languages = sync.OnceValue(func() []Language {
	out := make([]Language, len(table))
	for i, l := range table {
		l.Highlights = readQuery(l.Grammar, "highlights.scm")
		l.Injections = readQuery(l.Grammar, "injections.scm")
		out[i] = l
	}
	return out
})

func readQuery(grammar, name string) string {
	b, err := fs.ReadFile(files, path.Join("grammars", grammar, name))
	if err != nil {
		return ""
	}
	return string(b)
}

// Languages returns every builtin language. The slice is shared; don't
// modify it.
func Languages() []Language { return languages() }

// ByGrammar finds a language by its grammar name, as used in injection
// queries (injection.language "javascript").
func ByGrammar(grammar string) (Language, bool) {
	for _, l := range languages() {
		if l.Grammar == grammar {
			return l, true
		}
	}
	return Language{}, false
}

// ForPath picks a language for a file path: by file name ("Dockerfile")
// first, then by extension, ignoring case.
func ForPath(p string) (Language, bool) {
	base := path.Base(strings.ReplaceAll(p, "\\", "/"))
	for _, l := range languages() {
		for _, s := range l.Suffixes {
			if isFileName(s) && strings.EqualFold(base, s) {
				return l, true
			}
		}
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		ext := base[i+1:]
		for _, l := range languages() {
			for _, s := range l.Suffixes {
				if strings.EqualFold(ext, s) {
					return l, true
				}
			}
		}
	}
	return Language{}, false
}

func isFileName(suffix string) bool {
	return suffix != "" && (suffix[0] == '.' || suffix[0] >= 'A' && suffix[0] <= 'Z')
}
