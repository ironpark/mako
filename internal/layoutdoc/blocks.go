package layoutdoc

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
)

// A block is a piece of the window a document places by name. The window's
// own are mako.<name>; plugins' will be plugin:<id>/<block> and agents'
// agent:<session>/<id>. What a block draws is the window's business; what
// it is called and what props it takes is declared here, so that the
// daemon and its agents know the catalog without the window.

// The window's own blocks. The panes, the tab groups' split tree, are not
// one: a document places them with a panes node. Sessions' chat and
// terminal views, and the composer in a chat, are drawn in the panes'
// tabs, so they are not blocks of their own either; the window has no
// status bar.
const (
	BlockSidebar = "mako.sidebar"
	BlockHeader  = "mako.header"
	BlockFiles   = "mako.files"
	BlockGit     = "mako.git"
	BlockAgents  = "mako.agents"
	BlockForge   = "mako.forge"
	BlockAvatar  = "mako.avatar"
)

// Block declares a block: its name, its title in English, and the JSON
// Schema of its props, an object schema whose properties each give a type.
type Block struct {
	Name  string         `json:"name"`
	Title string         `json:"title"`
	Props map[string]any `json:"props"`
}

// Builtin declares the window's own blocks, in the order a catalog lists
// them. None takes props yet.
func Builtin() []Block {
	return []Block{
		{Name: BlockSidebar, Title: "Sidebar", Props: NoProps()},
		{Name: BlockHeader, Title: "Session header", Props: NoProps()},
		{Name: BlockFiles, Title: "Files", Props: NoProps()},
		{Name: BlockGit, Title: "Git", Props: NoProps()},
		{Name: BlockAgents, Title: "Agents", Props: NoProps()},
		{Name: BlockForge, Title: "Forge", Props: NoProps()},
		{Name: BlockAvatar, Title: "Bot avatar", Props: NoProps()},
	}
}

// Catalog says what blocks are registered: whether a name is, and the
// schema of its props. A document naming a block a catalog does not hold
// is not wrong: the block is drawn as missing, and kept.
type Catalog interface {
	BlockProps(name string) (schema map[string]any, ok bool)
}

// BuiltinCatalog is a catalog of the window's own blocks alone.
func BuiltinCatalog() Catalog { return builtinCatalog{} }

type builtinCatalog struct{}

func (builtinCatalog) BlockProps(name string) (map[string]any, bool) {
	for _, b := range Builtin() {
		if b.Name == name {
			return b.Props, true
		}
	}
	return nil, false
}

// Block names: the window's, a plugin's and an agent's.
var (
	makoBlockName   = regexp.MustCompile(`^mako\.[a-z][a-z0-9-]*$`)
	pluginBlockName = regexp.MustCompile(`^plugin:[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$`)
	agentBlockName  = regexp.MustCompile(`^agent:[A-Za-z0-9]+/[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// CheckBlockName says whether name is in one of the three namespaces.
func CheckBlockName(name string) error {
	if makoBlockName.MatchString(name) || pluginBlockName.MatchString(name) || agentBlockName.MatchString(name) {
		return nil
	}
	return fmt.Errorf("block name %q is not mako.<name>, plugin:<id>/<block> or agent:<session>/<id>", name)
}

// NoProps is the schema of a block that takes no props.
func NoProps() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

// CheckProps checks props against schema, an object schema: each prop's
// JSON type against its property's, the required ones present, and no
// others unless additionalProperties allows them. It reads no more of JSON
// Schema than that.
func CheckProps(schema, props map[string]any) error {
	declared, _ := schema["properties"].(map[string]any)
	extra, _ := schema["additionalProperties"].(bool)
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p, ok := declared[k].(map[string]any)
		if !ok {
			if extra {
				continue
			}
			return fmt.Errorf("prop %q is not one the block takes", k)
		}
		if t, _ := p["type"].(string); t != "" && !jsonTypeIs(props[k], t) {
			return fmt.Errorf("prop %q is not of type %s", k, t)
		}
	}
	required, _ := schema["required"].([]any)
	for _, r := range required {
		if k, _ := r.(string); k != "" && !slices.Contains(keys, k) {
			return fmt.Errorf("prop %q is required", k)
		}
	}
	return nil
}

// jsonTypeIs says whether v, as encoding/json decodes into any, is of the
// JSON Schema type t.
func jsonTypeIs(v any, t string) bool {
	switch v := v.(type) {
	case string:
		return t == "string"
	case float64:
		return t == "number" || t == "integer" && v == float64(int64(v))
	case bool:
		return t == "boolean"
	case map[string]any:
		return t == "object"
	case []any:
		return t == "array"
	case nil:
		return t == "null"
	}
	return false
}
