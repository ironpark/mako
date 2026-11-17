package workspace

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/crdt"
	"github.com/ironpark/mako/internal/protocol"
)

// legacyJSON is a workspace as a window wrote it down, in its own shapes.
const legacyJSON = `{
  "version": 1763000000123456789,
  "node_id": "laptop",
  "doc": {
    "spaces": [
      {"id": "w", "name": "Work", "color": "blue", "owners": ["github.com/acme"], "projects": ["mako", "repo:github.com/x/y"]},
      {"id": "h", "name": "Home", "hidden": ["mako"]},
      {"id": "w", "name": "a second space of the same ID"}
    ],
    "no_all": true,
    "default_space": "h",
    "projects": [
      {"id": "mako", "name": "Mako", "give_dirs": true, "roots": [
        {"label": "core", "remote": "github.com/ironpark/mako", "locations": [{"node": "laptop", "path": "/src/mako"}, {"node": "desk", "path": "C:\\src\\mako"}], "declined": [{"node": "laptop", "path": "/old/mako"}]},
        {"locations": [{"node": "laptop", "path": "/notes"}]},
        {}
      ]},
      {"id": "ggui", "name": "ggui", "no_join": true}
    ],
    "names": {"repo:github.com/x/y": "X"},
    "looks": {"mako": {"icon": "star", "color": "red"}},
    "pinned_projects": [{"section": "mako", "root": "github.com/ironpark/mako"}, {"section": "ggui"}],
    "pinned": ["s2", "s1"],
    "layouts": {
      "w|project:mako": {"ID": 3, "Vertical": true, "Ratio": 0.25,
        "First": {"ID": 1, "Group": {"ID": 1, "Tabs": ["s1", "doc\u0000x"], "Active": "s1"}},
        "Second": {"ID": 2, "Group": {"ID": 2, "Tabs": ["s9"], "Active": "s9"}}},
      "h": {"ID": 1, "Group": {"ID": 1, "Tabs": ["s2"], "Active": "s2"}},
      "all": {"ID": 1, "Group": {"ID": 1, "Tabs": ["old"], "Active": "old"}},
      "all|": {"ID": 1, "Group": {"ID": 1, "Tabs": ["new"], "Active": "new"}}
    },
    "tab_nodes": {"s1": "laptop", "s2": "desk", "s9": "desk", "elsewhere": "desk"}
  }
}`

func TestFromLegacy(t *testing.T) {
	st, err := FromLegacy([]byte(legacyJSON), realTime)
	if err != nil {
		t.Fatal(err)
	}
	if got := spaceIDs(st); !slices.Equal(got, []string{"w", "h"}) {
		t.Errorf("spaces %v", got)
	}
	w := st.GetSpaces()["w"]
	if w.GetName().GetValue() != "Work" || w.GetColor().GetValue() != "blue" {
		t.Errorf("space w %v", w)
	}
	if got := SpaceProjects(st, w); !slices.Equal(got, []string{"mako", "repo:github.com/x/y"}) {
		t.Errorf("w's projects %v", got)
	}
	if got := SpaceHidden(st, st.GetSpaces()["h"]); !slices.Equal(got, []string{"mako"}) {
		t.Errorf("h's hidden %v", got)
	}
	if !st.GetNoAll().GetValue() || st.GetDefaultSpace().GetValue() != "h" {
		t.Errorf("no_all %v, default %v", st.GetNoAll(), st.GetDefaultSpace())
	}

	projects := Projects(st)
	if len(projects) != 2 || projects[0].GetId() != "mako" || projects[1].GetId() != "ggui" {
		t.Fatalf("projects %v", projects)
	}
	if !projects[1].GetNoJoin().GetValue() || !projects[0].GetGiveDirs().GetValue() {
		t.Error("the projects' switches")
	}
	var rootIDs []string
	for _, r := range Roots(projects[0]) {
		rootIDs = append(rootIDs, r.GetId())
	}
	if want := []string{"github.com/ironpark/mako", LocationKey("laptop", "/notes"), "#2"}; !slices.Equal(rootIDs, want) {
		t.Errorf("roots %q, want %q", rootIDs, want)
	}
	core := projects[0].GetRoots()["github.com/ironpark/mako"]
	if got := Members(core.GetLocations()); !slices.Equal(got, []string{LocationKey("laptop", "/src/mako"), LocationKey("desk", `C:\src\mako`)}) {
		t.Errorf("locations %q", got)
	}
	if got := Members(core.GetDeclined()); !slices.Equal(got, []string{LocationKey("laptop", "/old/mako")}) {
		t.Errorf("declined %q", got)
	}
	scopes := Scopes(st, "laptop")
	if len(scopes) != 1 || !slices.Equal(scopes[0].GetPaths(), []string{"/src/mako", "/notes"}) || !scopes[0].GetGiveDirs() {
		t.Errorf("laptop's scopes %v", scopes)
	}

	if got := Names(st); got["repo:github.com/x/y"] != "X" {
		t.Errorf("names %v", got)
	}
	if got := Looks(st)["mako"]; got != (Look{Icon: "star", Color: "red"}) {
		t.Errorf("look %v", got)
	}
	if got := PinnedProjects(st); !slices.Equal(got, []string{"mako\x00github.com/ironpark/mako", "ggui\x00"}) {
		t.Errorf("pinned projects %q", got)
	}
	if got := PinnedSessions(st); !slices.Equal(got, []string{"s2", "s1"}) {
		t.Errorf("pinned sessions %v", got)
	}

	// Each layout under its key, the old space-only key made the whole
	// space's, with only its own tabs' nodes.
	l := Layout(st, "w|project:mako")
	var tree docPane
	if err := json.Unmarshal(l.GetDoc(), &tree); err != nil || !slices.Equal(tree.tabs(), []string{"s1", "doc\x00x", "s9"}) || tree.Ratio != 0.25 {
		t.Errorf("w's tree %s", l.GetDoc())
	}
	if got := l.GetTabNodes(); len(got) != 2 || got["s1"] != "laptop" || got["s9"] != "desk" {
		t.Errorf("w's tab nodes %v", got)
	}
	if got := Layout(st, "h|").GetTabNodes(); len(got) != 1 || got["s2"] != "desk" {
		t.Errorf("h's tab nodes %v", got)
	}
	if !bytes.Contains(Layout(st, "all|").GetDoc(), []byte(`"new"`)) {
		t.Errorf("all| is %s, not the newer key's", Layout(st, "all|").GetDoc())
	}

	// Every stamp is the edit's, to the millisecond.
	want := &protocol.HLC{WallTime: 1763000000123, NodeId: "laptop"}
	eachClock(st, func(h *protocol.HLC) {
		if !proto.Equal(h, want) {
			t.Fatalf("stamp %v", h)
		}
	})
}

// A pane tree goes into a layout as the window writes one.
func TestFromLegacyKeepsTheWindowsTree(t *testing.T) {
	tree := `{"ID":3,"Group":null,"Vertical":true,"Ratio":0.25,"First":{"ID":1,"Group":{"ID":1,"Tabs":["a"],"Active":"a"},"Vertical":false,"Ratio":0,"First":null,"Second":null},"Second":null}`
	st, err := FromLegacy([]byte(`{"version":5,"doc":{"layouts":{"all|":`+tree+`}}}`), realTime)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(Layout(st, "all|").GetDoc()); got != tree {
		t.Errorf("doc\n%s\nwant\n%s", got, tree)
	}
}

func TestFromLegacySameEverywhere(t *testing.T) {
	// Two nodes, at different times, convert the same document alike.
	a, err := FromLegacy([]byte(legacyJSON), realTime)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := FromLegacy([]byte(legacyJSON), realTime+time.Hour.Milliseconds())
	sameState(t, a, b)
	if !bytes.Equal(Digest(a), Digest(b)) {
		t.Fatal("digests differ")
	}
	if Merge(a, b, nil, never) {
		t.Error("merging the other's conversion changed the state")
	}
}

func TestFromLegacyStamps(t *testing.T) {
	tests := []struct {
		name    string
		version int64
		want    *protocol.HLC
	}{
		{"timed", 1_763_000_000_999_999_999, &protocol.HLC{WallTime: 1_763_000_000_999, NodeId: "n"}},
		{"counted", 7, &protocol.HLC{Logical: 7, NodeId: "n"}},
		{"never edited", 0, &protocol.HLC{Logical: 1, NodeId: "n"}},
		{"counted past the counter", countedEdits - 1, &protocol.HLC{Logical: 1<<32 - 1, NodeId: "n"}},
		{"far ahead", (realTime + 2*time.Hour.Milliseconds()) * 1e6, &protocol.HLC{WallTime: realTime, NodeId: "n"}},
	}
	for _, tt := range tests {
		body, _ := json.Marshal(docFile{Version: tt.version, NodeID: "n", Doc: document{Pinned: []string{"s"}}})
		st, err := FromLegacy(body, realTime)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.GetPinnedSessions()["s"].GetAddedAt(); !proto.Equal(got, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFromLegacyLeavesOutWhatCannotBeEncoded(t *testing.T) {
	bad := string([]byte{0xff, 0xfe})
	doc := document{
		Spaces:   []docSpace{{ID: bad, Name: "x"}, {ID: "ok", Name: bad}},
		Projects: []docProject{{ID: "p", Roots: []docRoot{{Locations: []docFolder{{Node: "n", Path: "/" + bad}, {Node: "n", Path: "/fine"}}}}}},
		Names:    map[string]string{bad: "x"},
		Pinned:   []string{bad, "s"},
		Layouts:  map[string]*docPane{"all|": {Group: &docGroup{Tabs: []string{bad}}}},
		TabNodes: map[string]string{bad: "n"},
	}
	// Decoded from JSON, what is not UTF-8 is replaced already; converted
	// from the types, it is left out.
	st := convert(doc, crdt.HLC{WallTime: 1, NodeID: "n"})
	if _, err := proto.Marshal(st); err != nil {
		t.Fatalf("the state does not encode: %v", err)
	}
	if got := spaceIDs(st); !slices.Equal(got, []string{"ok"}) || st.GetSpaces()["ok"].GetName() != nil {
		t.Errorf("spaces %v", st.GetSpaces())
	}
	if got := Scopes(st, "n"); len(got) != 1 || !slices.Equal(got[0].GetPaths(), []string{"/fine"}) {
		t.Errorf("scopes %v", got)
	}
	if len(st.GetNames()) != 0 || !slices.Equal(PinnedSessions(st), []string{"s"}) {
		t.Errorf("names %v, pins %v", st.GetNames(), PinnedSessions(st))
	}
	body, _ := json.Marshal(docFile{Version: 1, Doc: doc})
	if st, err := FromLegacy(body, realTime); err != nil {
		t.Fatal(err)
	} else if _, err := proto.Marshal(st); err != nil {
		t.Fatalf("the state from JSON does not encode: %v", err)
	}
}
