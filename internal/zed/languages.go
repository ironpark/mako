package zed

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

// Language is a language an installed extension gives.
type Language struct {
	Extension string // the ID of the extension it is in
	Dir       string
	Config    LanguageConfig
	// Grammar is the wasm of the grammar it parses with, found in its own
	// extension or another installed one; empty when none carries it.
	Grammar string

	firstLine *regexp.Regexp
}

// Languages lists the languages of the installed extensions, in their
// order. A grammar is looked for first in the language's own extension.
func Languages(exts []Extension) []Language {
	grammars := map[string]string{}
	for _, e := range exts {
		for name := range e.Grammars {
			path := filepath.Join(e.Dir, "grammars", name+".wasm")
			if _, ok := grammars[name]; !ok && exists(path) {
				grammars[name] = path
			}
		}
	}
	var out []Language
	for _, e := range exts {
		for _, dir := range e.languageDirs(e.Dir) {
			c, err := readLanguageConfig(dir)
			if err != nil {
				continue
			}
			l := Language{Extension: e.ID, Dir: dir, Config: c}
			if own := filepath.Join(e.Dir, "grammars", c.Grammar+".wasm"); c.Grammar != "" && exists(own) {
				l.Grammar = own
			} else {
				l.Grammar = grammars[c.Grammar]
			}
			if c.FirstLinePattern != "" {
				l.firstLine, _ = regexp.Compile(c.FirstLinePattern)
			}
			out = append(out, l)
		}
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Query reads one of the language's queries, as "highlights" or
// "injections": "" when it has none.
func (l Language) Query(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(l.Dir, name+".scm"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// Matches reports whether a file is in the language: by its name, or
// failing that by its first line.
func (l Language) Matches(name, firstLine string) bool {
	if l.Config.MatchesName(name) {
		return true
	}
	return l.firstLine != nil && firstLine != "" && l.firstLine.MatchString(firstLine)
}
