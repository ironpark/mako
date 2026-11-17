package gui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// A node's file browser lists down its side the folders of the projects
// on that node, as they come, and none of another node's.
func TestBrowserListsTheNodesProjects(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.post = func(func()) {} // no daemon to hear from
	b := newBrowser(m, "mac", "/w")
	p := ggui.ProbeBuilder(func() ggui.Widget { return browserPlaces(b) }, ggui.Sz(200, 600))
	defer p.Close()
	p.Frame()
	if _, ok := p.Find("server"); ok {
		t.Fatal("a project listed before there is one")
	}
	m.Spaces.Set(spaceList{Projects: []project{product()}})
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "else0000", NodeId: "mac", Project: "/w/else", State: protocol.StateRunning},
		{Id: "away0000", NodeId: "pc", Project: "/x/away", State: protocol.StateRunning},
	})
	p.Frame()
	page := p.Semantics().String()
	for _, want := range []string{"Product / server", "Product / web", "else"} {
		if !strings.Contains(page, want) {
			t.Fatalf("no %q down the side:\n%s", want, page)
		}
	}
	if strings.Contains(page, "away") {
		t.Fatalf("another node's project down the side:\n%s", page)
	}
}

func TestBrowserSortsFoldersFirst(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []fileItem{
		{Name: "b.txt", Size: 5, Mod: at.Add(2 * time.Hour)},
		{Name: "Zed", Dir: true, Mod: at},
		{Name: "a.txt", Size: 50, Mod: at.Add(time.Hour)},
		{Name: "alpha", Dir: true, Mod: at.Add(3 * time.Hour)},
	}
	names := func(items []fileItem) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Name)
		}
		return out
	}
	for _, c := range []struct {
		s    browserSort
		want []string
	}{
		{browserSort{}, []string{"alpha", "Zed", "a.txt", "b.txt"}},
		{browserSort{Desc: true}, []string{"Zed", "alpha", "b.txt", "a.txt"}},
		{browserSort{By: sortSize, Desc: true}, []string{"Zed", "alpha", "a.txt", "b.txt"}},
		{browserSort{By: sortMod}, []string{"Zed", "alpha", "a.txt", "b.txt"}},
	} {
		if got := names(c.s.sorted(items)); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.s, got, c.want)
		}
	}
	if got := names(matching(items, " A ")); !slices.Equal(got, []string{"a.txt", "alpha"}) {
		t.Errorf("matching a: %q", got)
	}
}
