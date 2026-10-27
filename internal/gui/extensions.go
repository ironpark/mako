package gui

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/zed"
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

// Which extensions the list shows: all of them, those installed or those
// not, and of every kind or one.
const (
	extShowAll       = "all"
	extShowInstalled = "installed"
	extShowAvailable = "available"
)

// extKinds are the kinds the list can be narrowed to, by what the store
// says an extension provides.
var extKinds = []struct{ Provides, Label string }{
	{"", "All"},
	{"languages", "Languages"},
	{"context-servers", "MCP Servers"},
}

// providesLabels name what an extension provides, as its badges show.
var providesLabels = map[string]string{
	"languages":        "Languages",
	"grammars":         "Grammars",
	"language-servers": "Language Servers",
	"context-servers":  "MCP Servers",
	"themes":           "Themes",
	"icon-themes":      "Icon Themes",
	"debug-adapters":   "Debug Adapters",
	"snippets":         "Snippets",
	"slash-commands":   "Slash Commands",
}

// extCard is an extension as the list shows it: the store's listing, an
// installed one's manifest, or both.
type extCard struct {
	ID, Name, Version, Description, Repository string
	Authors, Provides                          []string
	Downloads                                  int
	Installed                                  string // the version installed, "" for none
	ContextServers                             []string
}

// installedCard is an installed extension as a card, for when the store
// has not listed it.
func installedCard(e zed.Extension) extCard {
	var provides []string
	if len(e.Languages) > 0 || len(e.Grammars) > 0 {
		provides = append(provides, "languages")
	}
	if len(e.Grammars) > 0 {
		provides = append(provides, "grammars")
	}
	if len(e.LanguageServers) > 0 {
		provides = append(provides, "language-servers")
	}
	if len(e.ContextServers) > 0 {
		provides = append(provides, "context-servers")
	}
	return extCard{ID: e.ID, Name: e.Name, Version: e.Version, Description: e.Description, Repository: e.Repository,
		Authors: e.Authors, Provides: provides, Installed: e.Version, ContextServers: e.ContextServerIDs()}
}

// extCards are the extensions the list shows for what is searched and
// the filters picked: the store's, most downloaded first, after those
// installed it has not listed.
func extCards(r extSearch, installed []zed.Extension, show, kind string) []extCard {
	have := map[string]zed.Extension{}
	for _, e := range installed {
		have[e.ID] = e
	}
	listed := map[string]bool{}
	var store []extCard
	for _, l := range r.Listings[:min(len(r.Listings), maxListings)] {
		c := extCard{ID: l.ID, Name: l.Name, Version: l.Version, Description: l.Description, Repository: l.Repository,
			Authors: l.Authors, Provides: l.Provides, Downloads: l.DownloadCount}
		if e, ok := have[l.ID]; ok {
			c.Installed, c.ContextServers = e.Version, e.ContextServerIDs()
		}
		listed[l.ID] = true
		store = append(store, c)
	}
	var out []extCard
	q := strings.ToLower(r.Query)
	for _, e := range installed {
		c := installedCard(e)
		if !listed[e.ID] && strings.Contains(strings.ToLower(c.Name+" "+c.ID+" "+c.Description), q) {
			out = append(out, c)
		}
	}
	out = append(out, store...)
	return slices.DeleteFunc(out, func(c extCard) bool {
		switch {
		case show == extShowInstalled && c.Installed == "", show == extShowAvailable && c.Installed != "":
			return true
		case kind != "" && !slices.Contains(c.Provides, kind) && !(kind == "languages" && slices.Contains(c.Provides, "grammars")):
			return true
		}
		return false
	})
}

func extensionSettings(m *model) ggui.Widget {
	return page(T("Extensions"), extensionsAbout(), extensionsHeader(m), extensionsList(m))
}

func extensionsAbout() string {
	return T("Languages for the editor and MCP servers for the agents, from Zed's extensions. Go, Rust, Python, TypeScript and the other languages Zed builds in come with mako.")
}

// extSearchDelay is how long typing pauses before the store is asked.
const extSearchDelay = 300 * time.Millisecond

// extensionsDialog is Settings ▸ Extensions in a window of its own, as
// the palette opens it: the search and filters stay as the list scrolls.
func extensionsDialog(m *model) ggui.Widget {
	content := ggui.If(m.ExtensionsOpen, func() ggui.Widget {
		list := ggui.Scroll(ggui.Padding(extensionsList(m), 0, 12, 0, 0)).Bar(tokSurface3)
		return ggui.Box(ggui.Column(extensionsHeader(m), ggui.Expanded(list)).Gap(16).Stretch()).Height(600)
	})
	return ui.Dialog(m.ExtensionsOpen, content).Title(T("Extensions")).Width(880)
}

// extensionsHeader is the search over the list and its filters, as Zed's
// extensions page has them: what is installed or not beside the field,
// and the kinds under it.
func extensionsHeader(m *model) ggui.Widget {
	// The most downloaded show before anything is searched for.
	if r := ggui.Peek(m.ExtResults); r.Query == "" && r.Listings == nil && !r.Loading && r.Err == "" {
		m.searchExtensions("")
	}
	// What is typed is asked for once typing pauses.
	var pending *time.Timer
	ggui.Effect(func() ggui.Cleanup {
		q := strings.TrimSpace(m.ExtQuery.Get())
		if q == ggui.Peek(m.ExtResults).Query {
			return nil
		}
		pending = time.AfterFunc(extSearchDelay, func() {
			m.post(func() {
				if strings.TrimSpace(ggui.Peek(m.ExtQuery)) == q {
					m.searchExtensions(q)
				}
			})
		})
		return func() { pending.Stop() }
	})
	search := ggui.Box(ggui.Row(
		icon("search").Size(15).Color(tokMuted),
		ggui.Expanded(ggui.TextInput(m.ExtQuery).Placeholder(T("Search extensions…")).Name(T("Search extensions")).
			Style(ggui.TextStyle{Size: 13.5}).OnSubmit(m.searchExtensions)),
	).Gap(8).Align(ggui.AlignCenter)).Pad(8, 12).Border(1, tokBorder).Radius(8).Fill(tokSurface0)
	show := ui.ToggleGroup[string](m.ExtShow).Options([]string{extShowAll, extShowInstalled, extShowAvailable}).
		Format(func(v string) string {
			return T(map[string]string{extShowAll: "All", extShowInstalled: "Installed", extShowAvailable: "Not Installed"}[v])
		}).Name(T("Show extensions"))
	kinds := ggui.Reactive(func() ggui.Widget {
		cur := m.ExtKind.Get()
		tabs := make([]ggui.Widget, 0, len(extKinds))
		for _, k := range extKinds {
			fg := tokMuted
			if k.Provides == cur {
				fg = tokAccentBright
			}
			tabs = append(tabs, ui.ButtonOf(ggui.Text(T(k.Label)).Size(13.5).Color(fg).NoWrap(), func() { m.ExtKind.Set(k.Provides) }).
				Ghost().Pad(4, 8).Radius(6).Name(T(k.Label)))
		}
		return ggui.Row(tabs...).Gap(4)
	})
	errNote := ggui.View(m.ExtErr, func(e string) ggui.Widget {
		if e == "" {
			return ggui.Box()
		}
		return ggui.Text(e).Size(12.5).Color(uitheme.Destructive)
	})
	return ggui.Column(
		ggui.Row(ggui.Expanded(search), show).Gap(10).Align(ggui.AlignCenter),
		kinds,
		errNote,
	).Gap(10).Stretch()
}

// extensionsList is the extensions for what is searched, a card each.
func extensionsList(m *model) ggui.Widget {
	// The cards are built again only when they change: not as a search
	// goes out over the same listings, which would close a card's
	// settings for nothing.
	type shown struct {
		Cards   []extCard
		Err     string
		Loading bool
	}
	list := ggui.Derived(func() shown {
		r := m.ExtResults.Get()
		cards := extCards(r, m.Extensions.Get(), m.ExtShow.Get(), m.ExtKind.Get())
		if len(cards) > 0 {
			return shown{Cards: cards}
		}
		return shown{Err: r.Err, Loading: r.Loading}
	}).WithEqual(func(a, b shown) bool { return reflect.DeepEqual(a, b) })
	return ggui.View(list, func(v shown) ggui.Widget {
		switch {
		case v.Err != "":
			return noteCard(v.Err)
		case v.Loading:
			return ggui.Box(ui.Spinner()).Pad(14, 16)
		case len(v.Cards) == 0:
			return noteCard(T("No extensions found."))
		}
		out := make([]ggui.Widget, len(v.Cards))
		for i, c := range v.Cards {
			out[i] = extensionCard(m, c)
		}
		return ggui.Column(out...).Gap(12).Stretch()
	})
}

// extensionCard is an extension as Zed's list shows one: its name,
// version and what it provides over its description and authors, with
// what can be done with it at the right.
func extensionCard(m *model, c extCard) ggui.Widget {
	id := c.ID
	head := []ggui.Widget{
		ggui.Text(c.Name).Size(15).Weight(ggui.WeightMedium).NoWrap(),
		muted(ggui.Text("v" + strings.TrimPrefix(c.Version, "v")).Size(13).NoWrap()),
	}
	for _, p := range c.Provides {
		head = append(head, providesBadge(p))
	}
	head = append(head, ggui.Spacer(), extensionAction(m, c))

	description := strings.TrimSpace(c.Description)
	second := []ggui.Widget{ggui.Expanded(ggui.Text(description).Size(13).MaxLines(2).Ellipsis())}
	if c.Downloads > 0 {
		second = append(second, muted(ggui.Text(Tf("Downloads: %s", thousands(c.Downloads))).Size(12.5).NoWrap()))
	}

	third := []ggui.Widget{}
	if len(c.Authors) > 0 {
		third = append(third, icon("user").Size(13).Color(tokExtraMuted),
			ggui.Expanded(ggui.Text(strings.Join(c.Authors, ", ")).Size(12.5).Color(tokMuted).NoWrap().Ellipsis()))
	} else {
		third = append(third, ggui.Spacer())
	}
	open := ggui.State(false)
	if len(c.ContextServers) > 0 {
		third = append(third, ui.Button(T("Configure"), func() { ggui.Toggle(open) }).Ghost())
	}
	if c.Repository != "" {
		repo := c.Repository
		third = append(third, ui.Tooltip(ui.ButtonOf(icon(pick(strings.Contains(repo, "github.com"), "github", "external-link")).Size(15).Color(tokMuted),
			func() { _ = runtime.OpenURL(repo) }).Ghost().Pad(5).Radius(6).Name(T("Open the repository")), T("Open the repository")))
	}
	// Uninstall is the action of one that is up to date; one with an
	// update offers it here.
	if c.Installed != "" && c.Version != "" && c.Installed != c.Version {
		third = append(third, iconMenu(ui.MenuOf(icon("ellipsis").Size(15).Color(tokMuted),
			ui.MenuItem(T("Uninstall"), func() { m.uninstallExtension(id) }),
		).Name(Tf("%s actions", c.Name)), 5))
	}

	rows := []ggui.Widget{
		ggui.Row(head...).Gap(8).Align(ggui.AlignCenter),
		ggui.Row(second...).Gap(12).Align(ggui.AlignCenter),
		ggui.Row(third...).Gap(6).Align(ggui.AlignCenter),
	}
	if len(c.ContextServers) > 0 {
		servers := c.ContextServers
		rows = append(rows, ggui.If(open, func() ggui.Widget {
			cards := []ggui.Widget{muted(ggui.Text(T("Sessions on this node of agents that take MCP servers get these servers. Their settings, such as keys, stay in this node's config.json.")).Size(12.5))}
			for _, s := range servers {
				cards = append(cards, mcpServerCard(m, s, c.Name))
			}
			return ggui.Column(cards...).Gap(10).Stretch()
		}))
	}
	return ggui.Box(ggui.Column(rows...).Gap(10).Stretch()).Pad(14, 16).Fill(tokSurface1).Border(1, tokBorder).Radius(10)
}

// extensionAction is what can be done with an extension: install it,
// update it, or remove it, or nothing while one of those is under way.
func extensionAction(m *model, c extCard) ggui.Widget {
	id := c.ID
	return ggui.Reactive(func() ggui.Widget {
		switch {
		case m.ExtBusy.Get()[id]:
			return ui.Spinner()
		case c.Installed == "":
			return ui.ButtonOf(ggui.Row(icon("download").Size(14), ggui.Text(T("Install")).Size(13)).Gap(6).Align(ggui.AlignCenter),
				func() { m.installExtension(id) }).Secondary().Name(Tf("Install %s", c.Name))
		case c.Version != "" && c.Installed != c.Version:
			return ui.Button(T("Update"), func() { m.installExtension(id) }).Secondary().Name(Tf("Update %s", c.Name))
		}
		return ui.Button(T("Uninstall"), func() { m.uninstallExtension(id) }).Outline().Name(Tf("Uninstall %s", c.Name))
	})
}

// providesBadge names a kind an extension provides, in a small box.
func providesBadge(p string) ggui.Widget {
	label := p
	if l, ok := providesLabels[p]; ok {
		label = T(l)
	}
	return ggui.Box(ggui.Text(label).Size(11).Font(ggui.DefaultMonoFont()).Color(tokMuted).NoWrap()).
		Pad(2, 6).Border(1, tokBorder).Radius(4)
}

// noteCard is a card holding a line of muted text.
func noteCard(s string) ggui.Widget {
	return card(ggui.Box(muted(ggui.Text(s).Size(13))).Pad(14, 16))
}

// thousands writes n with its thousands grouped, as Zed's list does.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
