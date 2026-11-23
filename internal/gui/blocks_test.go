package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/layoutdoc"
)

// The window's own blocks are registered under mako., in the order the
// catalog lists them, each taking no props.
func TestTheWindowsBlocksAreRegistered(t *testing.T) {
	var names []string
	for _, d := range builtinBlocks().list() {
		names = append(names, d.Name)
		if err := layoutdoc.CheckProps(d.Props, map[string]any{"x": 1.0}); err == nil {
			t.Errorf("%s takes a prop x", d.Name)
		}
		if d.Title == "" {
			t.Errorf("%s has no title", d.Name)
		}
	}
	want := []string{layoutdoc.BlockSidebar, layoutdoc.BlockHeader, filesTab, gitTab, agentsTab, forgeTab, layoutdoc.BlockAvatar}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("blocks %v, want %v", names, want)
	}
}

// A plugin's and an agent's blocks register beside the window's, under
// their namespaces; a name of none, one taken, or a block that draws
// nothing is refused.
func TestBlocksRegisterInTheirNamespaces(t *testing.T) {
	r := builtinBlocks()
	draw := func(*model, map[string]any, blockPlace) blockView { return blockView{Body: widget.Box()} }
	for _, name := range []string{"plugin:ci-status/ci", "agent:822d51f133b0614b/board", "mako.extra"} {
		if err := r.register(blockDef{Name: name, Title: name, Build: draw}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if _, ok := r.lookup(name); !ok {
			t.Errorf("%s is not found", name)
		}
	}
	for _, d := range []blockDef{
		{Name: "git", Build: draw},
		{Name: "Mako.git", Build: draw},
		{Name: "plugin:ci", Build: draw},
		{Name: "plugin:/ci", Build: draw},
		{Name: "agent:abc/", Build: draw},
		{Name: "agent:a b/c", Build: draw},
		{Name: gitTab, Build: draw},
		{Name: "plugin:x/y"},
		{Name: "plugin:x/z", Build: draw, Props: map[string]any{"type": "string"}},
	} {
		if err := r.register(d); err == nil {
			t.Errorf("%q registered", d.Name)
		}
	}
}
