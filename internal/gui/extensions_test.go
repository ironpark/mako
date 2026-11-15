package gui

import (
	"fmt"
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/zed"
)

func TestExtensionCardsFollowTheFilters(t *testing.T) {
	r := extSearch{Listings: []zed.Listing{
		{ID: "html", Name: "HTML", Version: "0.3.2", Provides: []string{"languages", "grammars"}, DownloadCount: 7_580_403},
		{ID: "ctx", Name: "Context7", Version: "1.0.0", Provides: []string{"context-servers"}},
	}}
	installed := []zed.Extension{
		{Manifest: zed.Manifest{ID: "html", Name: "HTML", Version: "0.3.1"}},
		{Manifest: zed.Manifest{ID: "toml", Name: "TOML", Version: "1.1.0", Languages: []string{"languages/toml"}}},
	}
	ids := func(cards []extCard) []string {
		var out []string
		for _, c := range cards {
			out = append(out, c.ID)
		}
		return out
	}
	all := extCards(r, installed, extShowAll, "")
	if got := ids(all); !slices.Equal(got, []string{"toml", "html", "ctx"}) {
		t.Fatalf("all %v", got)
	}
	if all[1].Installed != "0.3.1" || all[1].Downloads != 7_580_403 {
		t.Fatalf("html %+v", all[1])
	}
	if got := ids(extCards(r, installed, extShowInstalled, "")); !slices.Equal(got, []string{"toml", "html"}) {
		t.Fatalf("installed %v", got)
	}
	if got := ids(extCards(r, installed, extShowAvailable, "")); !slices.Equal(got, []string{"ctx"}) {
		t.Fatalf("available %v", got)
	}
	if got := ids(extCards(r, installed, extShowAll, "context-servers")); !slices.Equal(got, []string{"ctx"}) {
		t.Fatalf("mcp %v", got)
	}
	if got := ids(extCards(r, installed, extShowAll, "languages")); !slices.Equal(got, []string{"toml", "html"}) {
		t.Fatalf("languages %v", got)
	}
	if thousands(7580403) != "7,580,403" || thousands(999) != "999" {
		t.Fatal("thousands")
	}
}

// The filters pick from all the store listed before the list is cut to
// its length: MCP servers, downloaded less than the languages, still
// show under their filter, and an installed extension listed past the
// cut still shows.
func TestExtensionFiltersComeBeforeTheCut(t *testing.T) {
	var r extSearch
	for i := range maxListings + 5 {
		r.Listings = append(r.Listings, zed.Listing{ID: fmt.Sprintf("lang%d", i), Provides: []string{"languages"}, DownloadCount: 1_000_000 - i})
	}
	for i := range 3 {
		r.Listings = append(r.Listings, zed.Listing{ID: fmt.Sprintf("mcp%d", i), Provides: []string{"context-servers"}, DownloadCount: 100 - i})
	}
	if got := len(extCards(r, nil, extShowAll, "context-servers")); got != 3 {
		t.Fatalf("%d MCP servers listed, want 3", got)
	}
	if got := len(extCards(r, nil, extShowAll, "")); got != maxListings {
		t.Fatalf("%d listed, want %d", got, maxListings)
	}
	installed := []zed.Extension{{Manifest: zed.Manifest{ID: "lang44", Name: "Lang 44", Version: "1.0.0", Languages: []string{"languages/x"}}}}
	cards := extCards(r, installed, extShowInstalled, "")
	if len(cards) != 1 || cards[0].ID != "lang44" {
		t.Fatalf("installed past the cut: %+v", cards)
	}
}
