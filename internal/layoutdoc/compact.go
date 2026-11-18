package layoutdoc

// A narrow screen, a phone, does not lay a document out side by side: it
// shows the document's blocks one page each, in a list to pick from. The
// pages are those of the document's compact root, variants.compact, when
// it has one, which the document writes for such a screen; else of its
// root, folded:
//
//   - the blocks in document order, depth first, a row's and column's
//     children in turn and a stack's blocks in the order of its tabs;
//   - the panes left out, which such a screen shows in its own way, as it
//     does the window's own frame, the blocks mako.sidebar and
//     mako.header;
//   - a node of a kind this version does not know left out.
//
// Sizes, grow, collapsing and a stack's active tab mean nothing on a page.
// A front-end leaves out, besides, the blocks it cannot draw: the Android
// app has its own screens for the window's other blocks (docs/protocol.md,
// "Layout documents"). The app's port of this is checked against a
// fixture of this package's (android_test.go).

// Page is a block a narrow screen shows a page of.
type Page struct {
	ID    string         `json:"id"`
	Block string         `json:"block"`
	Props map[string]any `json:"props,omitempty"`
}

// Pages are the pages of d, nil for a nil d.
func Pages(d *Doc) []Page {
	if d == nil {
		return nil
	}
	root := d.Compact
	if root == nil {
		root = d.Root
	}
	if root == nil {
		return nil
	}
	var out []Page
	root.Walk(nil, func(path []*Node) bool {
		n := path[len(path)-1]
		if n.Kind == KindBlock && n.Block != BlockSidebar && n.Block != BlockHeader {
			out = append(out, Page{ID: n.ID, Block: n.Block, Props: n.Props})
		}
		return true
	})
	return out
}
