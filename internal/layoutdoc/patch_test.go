package layoutdoc

import (
	"encoding/json"
	"strings"
	"testing"
)

//go:fix inline
func ptr[T any](v T) *T { return new(v) }

func ids(d *Doc) string {
	var out []string
	d.Root.Walk(nil, func(path []*Node) bool {
		out = append(out, path[len(path)-1].ID)
		return true
	})
	return strings.Join(out, " ")
}

// Each change does what it says to a copy, and leaves the IDs of the
// nodes it does not add or take out as they were.
func TestEachChangeOfAPatch(t *testing.T) {
	cat := BuiltinCatalog()
	for _, c := range []struct {
		name string
		ops  []Op
		want string // the IDs, parents first
		then func(d *Doc) string
	}{
		{"insert at the end", []Op{{Op: "insert", Parent: "workspace", Node: json.RawMessage(`{"id":"bottom","block":"mako.git","size":200,"collapsible":true}`)}},
			"window sidebar workspace header main bottom details mako.files mako.git mako.agents mako.forge", nil},
		{"insert at an index", []Op{{Op: "insert", Parent: "window", Index: new(0), Node: json.RawMessage(`{"id":"left","block":"mako.agents","size":200}`)}},
			"window left sidebar workspace header main details mako.files mako.git mako.agents mako.forge", nil},
		{"remove", []Op{{Op: "remove", ID: "mako.forge"}},
			"window sidebar workspace header main details mako.files mako.git mako.agents", nil},
		{"move left of the panes", []Op{{Op: "move", ID: "details", Parent: "window", Index: new(1)}},
			"window sidebar details mako.files mako.git mako.agents mako.forge workspace header main", nil},
		{"move out of a stack, and size it", []Op{
			{Op: "move", ID: "mako.git", Parent: "workspace"},
			{Op: "resize", ID: "mako.git", Size: new(240.0)},
		}, "window sidebar workspace header main mako.git details mako.files mako.agents mako.forge", func(d *Doc) string {
			if n := d.Node("mako.git"); n.Size != 240 {
				return "not sized"
			}
			if b, _ := json.Marshal(d.Node("mako.git")); !strings.Contains(string(b), `"id":"mako.git"`) {
				return "written as a bare name, its size lost: " + string(b)
			}
			return ""
		}},
		{"resize to grow", []Op{{Op: "resize", ID: "sidebar", Grow: new(1.0)}}, "", func(d *Doc) string {
			if n := d.Node("sidebar"); n.Size != 0 || n.Grow != 1 || n.Collapsible {
				return "sidebar not growing"
			}
			return ""
		}},
		{"select by block", []Op{{Op: "select", ID: "details", Block: "mako.git"}}, "", func(d *Doc) string {
			if d.Node("details").Active != 1 {
				return "not on git"
			}
			return ""
		}},
		{"select by index", []Op{{Op: "select", ID: "details", Active: new(3)}}, "", func(d *Doc) string {
			if d.Node("details").ActiveBlock() != "mako.forge" {
				return "not on forge"
			}
			return ""
		}},
		{"a stack shows what it did as children come and go", []Op{
			{Op: "select", ID: "details", Block: "mako.git"},
			{Op: "remove", ID: "mako.files"},
			{Op: "insert", Parent: "details", Index: new(0), Node: json.RawMessage(`"mako.header"`)},
		}, "", func(d *Doc) string {
			if d.Node("details").ActiveBlock() != "mako.git" {
				return "shows " + d.Node("details").ActiveBlock()
			}
			return ""
		}},
	} {
		before := Default()
		got, err := Apply(before, c.ops, cat)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if c.want != "" && ids(got) != c.want {
			t.Errorf("%s: %s\nwant %s", c.name, ids(got), c.want)
		}
		if c.then != nil {
			if why := c.then(got); why != "" {
				t.Errorf("%s: %s", c.name, why)
			}
		}
		if ids(before) != ids(Default()) || before.Node("details").Active != 0 {
			t.Errorf("%s: the document patched was changed", c.name)
		}
	}
}

// A patch that leaves a document a window could not draw, or names what
// is not there, is refused whole.
func TestAPatchThatBreaksTheDocumentIsRefused(t *testing.T) {
	cat := BuiltinCatalog()
	for _, c := range []struct {
		name string
		ops  []Op
		why  string
	}{
		{"none", nil, "no changes"},
		{"an unknown change", []Op{{Op: "rename", ID: "sidebar"}}, "no change"},
		{"the panes removed", []Op{{Op: "remove", ID: "main"}}, "0 panes"},
		{"the root removed", []Op{{Op: "remove", ID: "window"}}, "root"},
		{"a node not there", []Op{{Op: "remove", ID: "nope"}}, "no node"},
		{"an ID twice", []Op{{Op: "insert", Parent: "window", Node: json.RawMessage(`{"id":"sidebar","block":"mako.git"}`)}}, "two nodes"},
		{"into a block", []Op{{Op: "insert", Parent: "sidebar", Node: json.RawMessage(`"mako.git"`)}}, "no row"},
		{"past the end", []Op{{Op: "insert", Parent: "window", Index: new(9), Node: json.RawMessage(`"mako.git"`)}}, "index 9"},
		{"a node not read", []Op{{Op: "insert", Parent: "window", Node: json.RawMessage(`{"id":"g","grid":[]}`)}}, "no kind"},
		{"inside itself", []Op{{Op: "move", ID: "workspace", Parent: "workspace"}}, "inside itself"},
		{"a size in a stack", []Op{{Op: "resize", ID: "mako.git", Size: new(100.0)}}, "has a size"},
		{"both a size and grow", []Op{{Op: "resize", ID: "sidebar", Size: new(1.0), Grow: new(1.0)}}, "one of them"},
		{"a block not in the stack", []Op{{Op: "select", ID: "details", Block: "mako.header"}}, "holds no"},
		{"select out of the stack", []Op{{Op: "select", ID: "details", Active: new(7)}}, "active 7"},
		{"select a row", []Op{{Op: "select", ID: "window", Active: new(0)}}, "no stack"},
		{"props a block does not take", []Op{{Op: "insert", Parent: "window", Node: json.RawMessage(`{"block":"mako.git","props":{"x":1}}`)}}, `prop "x"`},
	} {
		_, err := Apply(Default(), c.ops, cat)
		if err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v, want an error saying %q", c.name, err, c.why)
		}
	}
}
