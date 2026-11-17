package workspace

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// The window's document: the workspace in the shapes the desktop window
// keeps it in (internal/gui's spaceList, layouts and tab nodes), as JSON.
// It is what a window wrote down whole before the workspace was a CRDT
// (FromLegacy), and still what a window holds: View is a state as one,
// and Diff the edit from one to another as a delta.

type document struct {
	Spaces         []docSpace         `json:"spaces,omitempty"`
	NoAll          bool               `json:"no_all,omitempty"`
	Default        string             `json:"default_space,omitempty"`
	Projects       []docProject       `json:"projects,omitempty"`
	Names          map[string]string  `json:"names,omitempty"`
	Looks          map[string]docLook `json:"looks,omitempty"`
	PinnedProjects []docScope         `json:"pinned_projects,omitempty"`
	Pinned         []string           `json:"pinned,omitempty"`
	// Layouts are the pane trees, by "spaceID|context", or by the space's
	// ID alone as before spaces kept them by project.
	Layouts  map[string]*docPane `json:"layouts,omitempty"`
	TabNodes map[string]string   `json:"tab_nodes,omitempty"`
	// WindowLayouts are the window layout documents, by name, as their
	// JSON (internal/layoutdoc); a space's Layout names the one it uses.
	WindowLayouts map[string]json.RawMessage `json:"window_layouts,omitempty"`
}

type docSpace struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Color    string   `json:"color,omitempty"`
	Owners   []string `json:"owners,omitempty"`
	Projects []string `json:"projects,omitempty"`
	Hidden   []string `json:"hidden,omitempty"`
	Layout   string   `json:"layout,omitempty"`
}

type docProject struct {
	ID       string    `json:"id"`
	Name     string    `json:"name,omitempty"`
	Roots    []docRoot `json:"roots,omitempty"`
	NoJoin   bool      `json:"no_join,omitempty"`
	GiveDirs bool      `json:"give_dirs,omitempty"`
}

type docRoot struct {
	// ID is the root's in the state, which a window keeps so that its
	// edits of the root are of the same one; a root a window made has
	// none until the state gives it one (rootID).
	ID        string      `json:"id,omitempty"`
	Label     string      `json:"label,omitempty"`
	Remote    string      `json:"remote,omitempty"`
	Owner     string      `json:"owner,omitempty"`
	Locations []docFolder `json:"locations,omitempty"`
	Declined  []docFolder `json:"declined,omitempty"`
}

type docFolder struct {
	Node string `json:"node"`
	Path string `json:"path"`
}

type docLook struct {
	Icon  string `json:"icon,omitempty"`
	Color string `json:"color,omitempty"`
}

type docScope struct {
	Section string `json:"section"`
	Root    string `json:"root,omitempty"`
}

// docPane is a pane tree as the window writes one: field names and all,
// which a LayoutEntry's doc keeps.
type docPane struct {
	ID            int
	Group         *docGroup
	Vertical      bool
	Ratio         float64
	First, Second *docPane
}

type docGroup struct {
	ID     int
	Tabs   []string
	Active string
}

// tabs are the tabs of the tree, in order.
func (p *docPane) tabs() []string {
	if p == nil {
		return nil
	}
	var out []string
	if p.Group != nil {
		out = append(out, p.Group.Tabs...)
	}
	return append(append(out, p.First.tabs()...), p.Second.tabs()...)
}

// fromDocument is the state of a window's document, every entry stamped
// with the mark Stamp takes as an edit, for Diff to compare.
func fromDocument(body []byte) (*protocol.WorkspaceState, error) {
	var d document
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	return convert(d, hlc(mark())), nil
}

// View is st as a window's document: the spaces, projects and their roots
// that show, in order, each with what refers to them, but what refers to
// one that does not show; the names and looks given; the pins; and the
// pane trees that are not deleted, of spaces that are not, each with its
// tabs' nodes.
func View(st *protocol.WorkspaceState) []byte {
	d := document{NoAll: st.GetNoAll().GetValue(), Default: st.GetDefaultSpace().GetValue()}
	for _, sp := range Spaces(st) {
		d.Spaces = append(d.Spaces, docSpace{
			ID:       sp.GetId(),
			Name:     sp.GetName().GetValue(),
			Color:    sp.GetColor().GetValue(),
			Owners:   Members(sp.GetOwners()),
			Projects: SpaceProjects(st, sp),
			Hidden:   SpaceHidden(st, sp),
			Layout:   sp.GetLayout().GetValue(),
		})
	}
	for _, p := range Projects(st) {
		dp := docProject{ID: p.GetId(), Name: p.GetName().GetValue(), NoJoin: p.GetNoJoin().GetValue(), GiveDirs: p.GetGiveDirs().GetValue()}
		for _, r := range Roots(p) {
			dp.Roots = append(dp.Roots, docRoot{
				ID:        r.GetId(),
				Label:     r.GetLabel().GetValue(),
				Remote:    r.GetRemote().GetValue(),
				Owner:     r.GetOwner().GetValue(),
				Locations: folders(Members(r.GetLocations())),
				Declined:  folders(Members(r.GetDeclined())),
			})
		}
		d.Projects = append(d.Projects, dp)
	}
	if names := Names(st); len(names) > 0 {
		d.Names = names
	}
	for key, l := range Looks(st) {
		if d.Looks == nil {
			d.Looks = map[string]docLook{}
		}
		d.Looks[key] = docLook{Icon: l.Icon, Color: l.Color}
	}
	for _, key := range PinnedProjects(st) {
		section, root, _ := strings.Cut(key, "\x00")
		d.PinnedProjects = append(d.PinnedProjects, docScope{Section: section, Root: root})
	}
	d.Pinned = PinnedSessions(st)
	// A tab's node is the first layout's to give one, by key, should two
	// differ.
	for _, key := range slices.Sorted(maps.Keys(st.GetLayouts())) {
		l := Layout(st, key)
		var tree docPane
		if l == nil || json.Unmarshal(l.GetDoc(), &tree) != nil {
			continue
		}
		if d.Layouts == nil {
			d.Layouts = map[string]*docPane{}
		}
		d.Layouts[key] = &tree
		for tab, node := range l.GetTabNodes() {
			if d.TabNodes == nil {
				d.TabNodes = map[string]string{}
			}
			if _, ok := d.TabNodes[tab]; !ok {
				d.TabNodes[tab] = node
			}
		}
	}
	// A document that is not JSON, which no front-end writes, is left out
	// rather than spoil the whole.
	for name := range st.GetWindowLayouts() {
		if l := WindowLayout(st, name); l != nil && json.Valid(l.GetDoc()) {
			if d.WindowLayouts == nil {
				d.WindowLayouts = map[string]json.RawMessage{}
			}
			d.WindowLayouts[name] = l.GetDoc()
		}
	}
	body, _ := json.Marshal(d)
	return body
}

// folders are the folders of locations' keys.
func folders(keys []string) []docFolder {
	var out []docFolder
	for _, key := range keys {
		if node, path, ok := SplitLocation(key); ok {
			out = append(out, docFolder{Node: node, Path: path})
		}
	}
	return out
}
