package gui

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/sanbo/internal/zed"
)

// Settings ▸ Extensions installs Zed extensions from Zed's store, for the
// languages they give the editor and the MCP servers they give agents.

// extSearch is the store's answer to a search.
type extSearch struct {
	Query    string
	Listings []zed.Listing
	Err      string
	Loading  bool
}

// searchExtensions asks the store for the extensions matching query.
func (m *model) searchExtensions(query string) {
	query = strings.TrimSpace(query)
	m.ExtResults.Set(extSearch{Query: query, Listings: ggui.Peek(m.ExtResults).Listings, Loading: true})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		found, err := extensionStore().Search(ctx, query)
		m.post(func() {
			if ggui.Peek(m.ExtResults).Query != query {
				return // another search went out meanwhile
			}
			r := extSearch{Query: query, Listings: found}
			if err != nil {
				r.Err = err.Error()
			}
			m.ExtResults.Set(r)
		})
	}()
}

// setExtBusy marks an extension as being installed or removed, or done.
func (m *model) setExtBusy(id string, busy bool) {
	next := maps.Clone(ggui.Peek(m.ExtBusy))
	if busy {
		next[id] = true
	} else {
		delete(next, id)
	}
	m.ExtBusy.Set(next)
}

// installExtension downloads an extension and highlights with it.
func (m *model) installExtension(id string) {
	m.setExtBusy(id, true)
	m.ExtErr.Set("")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, err := extensionStore().Install(ctx, id)
		m.post(func() {
			m.setExtBusy(id, false)
			if err != nil {
				m.ExtErr.Set(err.Error())
			}
			m.loadExtensions()
		})
	}()
}

// uninstallExtension removes an extension.
func (m *model) uninstallExtension(id string) {
	if err := extensionStore().Uninstall(id); err != nil {
		m.ExtErr.Set(err.Error())
	}
	m.loadExtensions()
}

// maxListings is how many of the store's extensions the page lists.
const maxListings = 40

func extensionSettings(m *model) ggui.Widget {
	return page(T("Extensions"), extensionsAbout(), extensionSections(m)...)
}

func extensionsAbout() string {
	return T("Languages for the editor and MCP servers for the agents, from Zed's extensions. Go, Rust, Python, TypeScript and the other languages Zed builds in come with sanbo.")
}

// extensionSections are the extensions installed and the store's, for
// the settings page and the palette's dialog alike.
func extensionSections(m *model) []ggui.Widget {
	// The most downloaded show before anything is searched for.
	if r := ggui.Peek(m.ExtResults); r.Query == "" && r.Listings == nil && !r.Loading && r.Err == "" {
		m.searchExtensions("")
	}
	installed := ggui.Reactive(func() ggui.Widget {
		exts := m.Extensions.Get()
		if len(exts) == 0 {
			return noteCard(T("No extensions installed."))
		}
		rows := make([]ggui.Widget, 0, len(exts))
		for _, e := range exts {
			id := e.ID
			hint := e.Version
			if langs := m.extLanguages[id]; len(langs) > 0 {
				hint += " · " + Tf("Languages: %s", strings.Join(langs, ", "))
			}
			if servers := e.ContextServerIDs(); len(servers) > 0 {
				hint += " · " + Tf("MCP servers: %s", strings.Join(servers, ", "))
			}
			rows = append(rows, row(e.Name, hint,
				ui.Button(T("Remove"), func() { m.uninstallExtension(id) }).Outline()))
		}
		return card(rows...)
	})

	query := ggui.State(ggui.Peek(m.ExtResults).Query)
	search := ui.TextField(query).Placeholder(T("Search Zed's extensions")).Name(T("Search extensions")).
		OnSubmit(m.searchExtensions)
	results := ggui.Reactive(func() ggui.Widget {
		r := m.ExtResults.Get()
		busy := m.ExtBusy.Get()
		have := map[string]string{}
		for _, e := range m.Extensions.Get() {
			have[e.ID] = e.Version
		}
		switch {
		case r.Err != "":
			return noteCard(r.Err)
		case r.Loading && len(r.Listings) == 0:
			return ggui.Box(ui.Spinner()).Pad(14, 16)
		case len(r.Listings) == 0 && r.Query != "":
			return noteCard(T("No extensions found."))
		case len(r.Listings) == 0:
			return ggui.Box()
		}
		rows := make([]ggui.Widget, 0, len(r.Listings))
		for _, l := range r.Listings[:min(len(r.Listings), maxListings)] {
			id := l.ID
			var action ggui.Widget
			switch v, ok := have[id]; {
			case busy[id]:
				action = ui.Spinner()
			case ok && v == l.Version:
				action = muted(ggui.Text(T("Installed")).Size(12.5))
			default:
				label := T("Install")
				if ok {
					label = T("Update")
				}
				action = ui.Button(label, func() { m.installExtension(id) }).Outline()
			}
			hint := strings.TrimSpace(l.Description)
			if hint == "" {
				hint = strings.Join(l.Provides, ", ")
			}
			rows = append(rows, row(l.Name, Tf("%s · %s downloads", hint, countLabel(l.DownloadCount)), action))
		}
		return card(rows...)
	})
	errNote := ggui.View(m.ExtErr, func(e string) ggui.Widget {
		if e == "" {
			return ggui.Box()
		}
		return ggui.Text(e).Size(12.5).Color(uitheme.Destructive)
	})
	return []ggui.Widget{
		section(T("Installed"), installed),
		mcpSection(m),
		section(T("Zed extension store"),
			ggui.Row(ggui.Expanded(search), ui.Button(T("Search"), func() { m.searchExtensions(ggui.Peek(query)) }).Outline()).Gap(8),
			errNote,
			results,
		),
	}
}

// noteCard is a card holding a line of muted text.
func noteCard(s string) ggui.Widget {
	return card(ggui.Box(muted(ggui.Text(s).Size(13))).Pad(14, 16))
}

// countLabel shortens a count as the store's own list does: 1.2k, 3.4M.
func countLabel(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
