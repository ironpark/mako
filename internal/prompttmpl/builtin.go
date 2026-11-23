package prompttmpl

import (
	"embed"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"sync"
)

// Built-in templates come with mako, in the binary: listed with the
// user's own, and called as theirs are. A file of the user's of the same
// name stands in for one, so a built-in is changed by copying it, and
// comes back once the copy is deleted; it is never deleted itself.

// MakerName is the built-in template that has the agent write a template
// from a description and save it through the mako MCP server's
// save_template: //template.
const MakerName = "template"

// PluginMakerName is the built-in template that has the agent build a Mako
// plugin from a description into the node's plugins directory: //plugin.
const PluginMakerName = "plugin"

//go:embed builtin
var builtinFiles embed.FS

// builtinSources are the built-in templates' files, by name.
var builtinSources = map[string]string{
	MakerName:       "builtin/maker.md",
	PluginMakerName: "builtin/plugin.md",
}

// builtins are the built-in templates read, by name in order. One that
// does not read is left out, which the tests catch.
var builtins = sync.OnceValue(func() []*Template {
	return loadBuiltins(Parse)
})

var builtinsMetadata = sync.OnceValue(func() []*Template {
	return loadBuiltins(ParseMetadata)
})

func loadBuiltins(parse func(string, []byte) (*Template, error)) []*Template {
	var out []*Template
	for name, path := range builtinSources {
		data, err := builtinFiles.ReadFile(path)
		if err != nil {
			continue
		}
		t, err := parse(name, data)
		if err != nil {
			continue
		}
		t.Builtin = true
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b *Template) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// BuiltinTemplates are the templates mako comes with, by name in order.
func BuiltinTemplates() []*Template { return slices.Clone(builtins()) }

// BuiltinTemplate is the built-in template called name, nil for none.
func BuiltinTemplate(name string) *Template {
	for _, t := range builtins() {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// BuiltinSource is the file of the built-in template called name, as it
// is written; nil for none.
func BuiltinSource(name string) []byte {
	path, ok := builtinSources[name]
	if !ok {
		return nil
	}
	data, _ := builtinFiles.ReadFile(path)
	return data
}

// Reference is the template language's reference, in Markdown: what the
// built-in //template tells the agent of it, in its raw block, and what
// docs/usage.md and Settings ▸ Templates show, word for word.
func Reference() string {
	src := string(BuiltinSource(MakerName))
	start := strings.Index(src, "\n{#raw}\n")
	end := strings.LastIndex(src, "{/raw}\n")
	if start < 0 || end < start {
		return ""
	}
	return src[start+len("\n{#raw}\n") : end]
}

// Find reads the template called name: the user's, from dir, or the
// built-in of the name when dir has none.
func Find(dir, name string) (*Template, error) {
	t, err := Load(dir, name)
	if errors.Is(err, fs.ErrNotExist) {
		if b := BuiltinTemplate(name); b != nil {
			return b, nil
		}
	}
	return t, err
}

// LoadAll reads the templates in dir, as LoadDir does, with the built-in
// templates no file of dir stands in for, by name in order.
func LoadAll(dir string) ([]*Template, []Broken) {
	out, broken := LoadDir(dir)
	return mergeWithBuiltins(out, broken, builtins())
}

// LoadAllMetadata reads the templates' metadata in dir, as LoadDirMetadata
// does, with the built-in templates' metadata no file of dir stands in for, by
// name in order, without instantiating a JavaScript runtime.
func LoadAllMetadata(dir string) ([]*Template, []Broken) {
	out, broken := LoadDirMetadata(dir)
	return mergeWithBuiltins(out, broken, builtinsMetadata())
}

func mergeWithBuiltins(out []*Template, broken []Broken, built []*Template) ([]*Template, []Broken) {
	for _, b := range built {
		taken := slices.ContainsFunc(out, func(t *Template) bool { return t.Name == b.Name }) ||
			slices.ContainsFunc(broken, func(x Broken) bool { return x.Name == b.Name })
		if !taken {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b *Template) int { return strings.Compare(a.Name, b.Name) })
	return out, broken
}
