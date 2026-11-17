package workspace

import (
	"cmp"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/crdt"
	"github.com/ironpark/mako/internal/protocol"
)

// Reading the state. An entity shows while it was created later than it
// was deleted: a deletion wins over edits made to it meanwhile, which are
// kept, and come back with it if it is created again. What refers to an
// entity that does not show, a space listing a deleted project, a pin of
// it, its name or look, the layouts of a deleted space, is kept too, and
// left out here.

// visible reports whether an entity created and deleted at these stamps
// shows; key is its spaceKey, projectKey or rootKey.
func visible(created, deleted *protocol.HLC, key string) bool {
	return hlc(created).After(hlc(deleted), key)
}

// Spaces are the spaces that show, by rank and then by ID.
func Spaces(st *protocol.WorkspaceState) []*protocol.SpaceEntry {
	var out []*protocol.SpaceEntry
	for id, sp := range st.GetSpaces() {
		if visible(sp.GetCreatedAt(), sp.GetDeletedAt(), spaceKey(id)) {
			out = append(out, sp)
		}
	}
	slices.SortFunc(out, func(a, b *protocol.SpaceEntry) int {
		return cmp.Or(cmp.Compare(a.GetRank().GetValue(), b.GetRank().GetValue()), cmp.Compare(a.GetId(), b.GetId()))
	})
	return out
}

// Projects are the projects that show, by rank and then by ID.
func Projects(st *protocol.WorkspaceState) []*protocol.ProjectEntry {
	var out []*protocol.ProjectEntry
	for id, p := range st.GetProjects() {
		if visible(p.GetCreatedAt(), p.GetDeletedAt(), projectKey(id)) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b *protocol.ProjectEntry) int {
		return cmp.Or(cmp.Compare(a.GetRank().GetValue(), b.GetRank().GetValue()), cmp.Compare(a.GetId(), b.GetId()))
	})
	return out
}

// Roots are the roots of p that show, by rank and then by ID.
func Roots(p *protocol.ProjectEntry) []*protocol.ProjectRootEntry {
	var out []*protocol.ProjectRootEntry
	for id, r := range p.GetRoots() {
		if visible(r.GetCreatedAt(), r.GetDeletedAt(), rootKey(projectKey(p.GetId()), id)) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b *protocol.ProjectRootEntry) int {
		return cmp.Or(cmp.Compare(a.GetRank().GetValue(), b.GetRank().GetValue()), cmp.Compare(a.GetId(), b.GetId()))
	})
	return out
}

// Members are the elements in a set, by rank and then by themselves.
func Members(set map[string]*protocol.ElementMeta) []string {
	s := make(crdt.Set, len(set))
	for k, e := range set {
		s[k] = metaOf(e)
	}
	return s.Members()
}

// deletedProject reports whether id is a project that does not show. A
// section's ID that is no project's, one the sidebar groups by itself, is
// not.
func deletedProject(st *protocol.WorkspaceState, id string) bool {
	p, ok := st.GetProjects()[id]
	return ok && !visible(p.GetCreatedAt(), p.GetDeletedAt(), projectKey(id))
}

// deletedSpace reports whether id is a space that does not show; the
// space of every session is none.
func deletedSpace(st *protocol.WorkspaceState, id string) bool {
	sp, ok := st.GetSpaces()[id]
	return ok && !visible(sp.GetCreatedAt(), sp.GetDeletedAt(), spaceKey(id))
}

// SpaceProjects are the sections moved to sp, in order, but deleted
// projects.
func SpaceProjects(st *protocol.WorkspaceState, sp *protocol.SpaceEntry) []string {
	return slices.DeleteFunc(Members(sp.GetProjects()), func(id string) bool { return deletedProject(st, id) })
}

// SpaceHidden are the sections left out of sp, but deleted projects.
func SpaceHidden(st *protocol.WorkspaceState, sp *protocol.SpaceEntry) []string {
	return slices.DeleteFunc(Members(sp.GetHidden()), func(id string) bool { return deletedProject(st, id) })
}

// PinnedProjects are the pinned sections and roots, as "section\x00root",
// in order, but those of deleted projects.
func PinnedProjects(st *protocol.WorkspaceState) []string {
	return slices.DeleteFunc(Members(st.GetPinnedProjects()), func(key string) bool {
		section, _, _ := strings.Cut(key, "\x00")
		return deletedProject(st, section)
	})
}

// PinnedSessions are the pinned sessions, by ID, in order.
func PinnedSessions(st *protocol.WorkspaceState) []string {
	return Members(st.GetPinnedSessions())
}

// Names are the names given to sections, by section key, but those of
// deleted projects and those cleared.
func Names(st *protocol.WorkspaceState) map[string]string {
	out := map[string]string{}
	for key, n := range st.GetNames() {
		if n.GetValue() != "" && !deletedProject(st, key) {
			out[key] = n.GetValue()
		}
	}
	return out
}

// Look is a section's icon and colour, by name; "" for none.
type Look struct{ Icon, Color string }

// Looks are the sections' looks, by section key, but those of deleted
// projects and those with nothing set.
func Looks(st *protocol.WorkspaceState) map[string]Look {
	out := map[string]Look{}
	for key, l := range st.GetLooks() {
		look := Look{Icon: l.GetIcon().GetValue(), Color: l.GetColor().GetValue()}
		if look != (Look{}) && !deletedProject(st, key) {
			out[key] = look
		}
	}
	return out
}

// Layout is the pane tree kept under key, "spaceID|context"; nil when there
// is none, it was deleted, or its space was.
func Layout(st *protocol.WorkspaceState, key string) *protocol.LayoutEntry {
	l := st.GetLayouts()[key]
	if l == nil || l.GetDeleted() {
		return nil
	}
	if space, _, ok := strings.Cut(key, "|"); ok && deletedSpace(st, space) {
		return nil
	}
	return l
}

// WindowLayout is the window layout named name; nil when there is none or
// it was deleted.
func WindowLayout(st *protocol.WorkspaceState, name string) *protocol.LayoutEntry {
	l := st.GetWindowLayouts()[name]
	if l == nil || l.GetDeleted() {
		return nil
	}
	return l
}

// LocationKey is the key of a root's location: a folder on a node. A path
// may hold a colon, and no node ID or path holds a NUL.
func LocationKey(node, path string) string { return node + "\x00" + path }

// SplitLocation is the node and path of a location's key; ok is false for
// a key that is none.
func SplitLocation(key string) (node, path string, ok bool) {
	return strings.Cut(key, "\x00")
}

// Scopes are the projects that show as node has them: for each with a
// folder there, its ID, name, whether it gives its agents the others, and
// its folders there, by its roots' order and their locations', each once.
// It is what a daemon keeps for its agents (SetProjects), as the window
// used to tell it.
func Scopes(st *protocol.WorkspaceState, node string) []*protocol.ProjectScope {
	var out []*protocol.ProjectScope
	for _, p := range Projects(st) {
		s := &protocol.ProjectScope{Id: p.GetId(), Name: p.GetName().GetValue(), GiveDirs: p.GetGiveDirs().GetValue()}
		for _, r := range Roots(p) {
			for _, key := range Members(r.GetLocations()) {
				n, path, ok := SplitLocation(key)
				if ok && n == node && path != "" && !slices.Contains(s.Paths, path) {
					s.Paths = append(s.Paths, path)
				}
			}
		}
		if len(s.Paths) > 0 {
			out = append(out, s)
		}
	}
	return out
}
