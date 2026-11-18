package gui

import (
	"fmt"
	"image/color"
	"net/url"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// storeSearchDelay is how long typing pauses before the registries are
// asked.
const storeSearchDelay = 300 * time.Millisecond

// storeSection is the search across the registries and what it found.
func storeSection(m *model) ggui.Widget {
	if r := ggui.Peek(m.store.Results); r.Query == "" && r.Listings == nil && !r.Loading && r.Err == "" {
		m.searchStore("")
		m.loadRegistries()
	}
	var pending *time.Timer
	ggui.Effect(func() ggui.Cleanup {
		q := strings.TrimSpace(m.store.Query.Get())
		if q == ggui.Peek(m.store.Results).Query {
			return nil
		}
		pending = time.AfterFunc(storeSearchDelay, func() {
			m.post(func() {
				if strings.TrimSpace(ggui.Peek(m.store.Query)) == q {
					m.searchStore(q)
				}
			})
		})
		return func() { pending.Stop() }
	})
	search := widget.Box(widget.Row(
		icon("search").Size(15).Color(skin.Muted),
		widget.Expanded(widget.TextInput(m.store.Query).Placeholder(T("Search the registries…")).Name(T("Search plugins")).
			Style(ggui.TextStyle{Size: 13.5}).OnSubmit(m.searchStore)),
	).Gap(8).Align(widget.AlignCenter)).Pad(8, 12).Border(1, skin.Border).Radius(8).Fill(skin.Surface0)
	errNote := ggui.View(m.store.Err, func(e string) ggui.Widget {
		if e == "" {
			return widget.Box()
		}
		return widget.Box(widget.Row(icon("shield-alert").Size(14).Color(uitheme.Destructive),
			widget.Expanded(widget.Text(e).Size(12.5).Color(uitheme.Destructive))).Gap(8)).Pad(2, 2)
	})
	type shown struct {
		Listings []*protocol.PluginListing
		Err      string
		Loading  bool
	}
	list := ggui.Derived(func() shown {
		r := m.store.Results.Get()
		return shown{Listings: r.Listings, Err: r.Err, Loading: r.Loading && r.Listings == nil}
	}).WithEqual(func(a, b shown) bool { return deepEqual(a, b) })
	results := ggui.View(list, func(v shown) ggui.Widget {
		switch {
		case v.Err != "":
			return noteCard(v.Err)
		case v.Loading:
			return widget.Box(ui.Spinner()).Pad(14, 16)
		case len(v.Listings) == 0:
			return noteCard(T("No registry of this node lists a plugin of that name. Add a registry below to find more."))
		}
		out := make([]ggui.Widget, len(v.Listings))
		for i, l := range v.Listings {
			out[i] = listingCard(m, l)
		}
		return widget.Column(out...).Gap(12).Stretch()
	})
	return widget.Column(search, errNote, results).Gap(10).Stretch()
}

// listingCard is a plugin as a registry lists it: what it is and does,
// whose key signed it and which registry lists it, what it asks for, and
// what can be done with it here. Nothing on it says anyone checked it:
// nobody did.
func listingCard(m *model, l *protocol.PluginListing) ggui.Widget {
	name := l.GetName()
	if name == "" {
		name = l.GetId()
	}
	head := []ggui.Widget{
		widget.Text(name).Size(15).Weight(ggui.WeightMedium).NoWrap(),
		muted(widget.Text("v" + l.GetVersion()).Size(13).NoWrap()),
	}
	if name != l.GetId() {
		head = append(head, chip(l.GetId()))
	}
	head = append(head, widget.Spacer(), listingAction(m, l))
	rows := []ggui.Widget{widget.Row(head...).Gap(8).Align(widget.AlignCenter)}
	if d := strings.TrimSpace(l.GetDescription()); d != "" {
		rows = append(rows, widget.Text(d).Size(13).MaxLines(2).Ellipsis())
	}
	rows = append(rows, originLine(l.GetPublisher(), l.GetFingerprint(), l.GetRegistry(), l.GetHomepage()))
	rows = append(rows, asksLine(l.GetPermissions()))
	switch {
	case l.GetBlocked() != "":
		rows = append(rows, warnLine("ban", uitheme.Destructive, Tf("Blocked: %s", l.GetBlocked())))
	case l.GetOtherKey():
		rows = append(rows, warnLine("key-round", skin.DotWaiting,
			Tf("Installed here from %s, signed by another publisher key: this one cannot replace it. Remove that one first to install this.", registryLabel(l.GetInstalledFrom()))))
	case l.GetInstalled() == "dev":
		rows = append(rows, warnLine("folder", skin.DotWaiting, T("A plugin in development has this ID on this node: an install never replaces its folder.")))
	}
	return widget.Box(widget.Column(rows...).Gap(9).Stretch()).Pad(14, 16).Fill(skin.Surface1).Border(1, skin.Border).Radius(10)
}

// originLine says whose a plugin is, as far as anyone can tell: the name
// its publisher gives itself, which nobody checked, the fingerprint of
// the key that signs it, and the registry that lists it.
func originLine(publisher, fingerprint, registry, homepage string) ggui.Widget {
	parts := []ggui.Widget{
		icon("user").Size(13).Color(skin.ExtraMuted),
		muted(widget.Text(publisher).Size(12.5).NoWrap()),
		icon("key-round").Size(13).Color(skin.ExtraMuted),
		ui.Tooltip(muted(widget.Text(shortFingerprint(fingerprint)).Size(12).Font(ggui.DefaultMonoFont()).NoWrap()),
			Tf("The publisher's key: %s", fingerprint)),
		icon("globe").Size(13).Color(skin.ExtraMuted),
		widget.Expanded(muted(widget.Text(Tf("from %s", registryLabel(registry))).Size(12.5).NoWrap().Ellipsis())),
	}
	if homepage != "" {
		parts = append(parts, ui.Tooltip(ui.ButtonOf(icon("external-link").Size(14).Color(skin.Muted),
			func() { _ = runtime.OpenURL(homepage) }).Ghost().Pad(4).Radius(6).Name(T("Open its homepage")), T("Open its homepage")))
	}
	return widget.Row(parts...).Gap(6).Align(widget.AlignCenter)
}

// asksLine says what a plugin asks for, as its registry lists it.
func asksLine(perms []string) ggui.Widget {
	if len(perms) == 0 {
		return muted(widget.Text(T("Asks for nothing beyond its blocks, its own settings and the log.")).Size(12.5))
	}
	texts := make([]string, len(perms))
	for i, p := range perms {
		texts[i] = permissionText(p)
	}
	return widget.Row(
		icon("lock").Size(13).Color(skin.ExtraMuted),
		widget.Expanded(widget.Text(Tf("Asks to: %s", strings.Join(texts, " · "))).Size(12.5)),
	).Gap(6).Align(widget.AlignStart)
}

// warnLine is a line that stops the eye, in a colour of warning.
func warnLine(glyph string, c color.Color, text string) ggui.Widget {
	return widget.Row(icon(glyph).Size(14).Color(c), widget.Expanded(widget.Text(text).Size(12.5).Color(c))).Gap(8).Align(widget.AlignStart)
}

// listingAction is what can be done with a listing here.
func listingAction(m *model, l *protocol.PluginListing) ggui.Widget {
	id, registry := l.GetId(), l.GetRegistry()
	return ggui.Reactive(func() ggui.Widget {
		switch {
		case m.store.Busy.Get()[id]:
			return ui.Spinner()
		case l.GetBlocked() != "" || l.GetOtherKey() || l.GetInstalled() == "dev":
			return widget.Box()
		case l.GetInstalled() == "":
			return ui.ButtonOf(widget.Row(icon("download").Size(14), widget.Text(T("Install")).Size(13)).Gap(6).Align(widget.AlignCenter),
				func() { m.installPlugin(protocol.InstallOp, id, registry) }).Secondary().Name(Tf("Install %s", id))
		case l.GetInstalledFrom() == registry && l.GetInstalled() != l.GetVersion():
			return ui.Button(Tf("Update to %s", l.GetVersion()), func() { m.installPlugin(protocol.UpdateOp, id, "") }).Secondary().Name(Tf("Update %s", id))
		case l.GetInstalledFrom() == registry:
			return ui.Button(T("Remove"), func() { m.installPlugin(protocol.RemoveOp, id, "") }).Outline().Name(Tf("Remove %s", id))
		}
		return muted(widget.Text(Tf("Installed from %s", registryLabel(l.GetInstalledFrom()))).Size(12.5).NoWrap())
	})
}

// registriesSection lists the registries the node reads, to add one by
// its URL or remove one.
func registriesSection(m *model) ggui.Widget {
	list := ggui.View(m.store.Registries, func(regs []*protocol.RegistryInfo) ggui.Widget {
		rows := []ggui.Widget{widget.Box(muted(widget.Text(
			T("A registry is a git repository anyone can make. Nobody reviews what one lists, the one mako comes with included: a plugin may do only what you allow it, and a registry's blocklist stops what it names on this node, whichever registry it came from.")).Size(12.5))).Pad(14, 16)}
		for _, r := range regs {
			rows = append(rows, registryRow(m, r))
		}
		add := func(v string) {
			if strings.TrimSpace(v) != "" {
				m.changeRegistry(protocol.RegistryAdd, strings.TrimSpace(v))
			}
		}
		field := ui.TextField(m.store.RegistryURL).Placeholder("https://github.com/owner/repository").Name(T("A registry's URL")).OnSubmit(add)
		rows = append(rows, widget.Box(widget.Row(
			widget.Expanded(field),
			ui.Button(T("Add"), func() { add(ggui.Peek(m.store.RegistryURL)) }).Secondary(),
			ui.Button(T("Read again"), func() { m.changeRegistry(protocol.RegistryRefresh, "") }).Outline(),
		).Gap(8).Align(widget.AlignCenter)).Pad(12, 16))
		return card(rows...)
	})
	return list
}

// registryRow is a registry: its URL, what it says it is, and how its
// index last read.
func registryRow(m *model, r *protocol.RegistryInfo) ggui.Widget {
	title := []ggui.Widget{widget.Text(r.GetUrl()).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()}
	if r.GetBuiltin() {
		title = append(title, chip(T("comes with mako")))
	}
	var status ggui.Widget
	switch {
	case r.GetError() != "":
		// Never read, it gives nothing; read before, what it gave stands.
		status = widget.Text(Tf("Not read: %s", r.GetError())).Size(12).
			Color(pick[color.Color](r.GetFetchedAt() == 0, uitheme.Destructive, skin.DotWaiting)).MaxLines(2).Ellipsis()
	case r.GetFetchedAt() == 0:
		status = muted(widget.Text(T("Not read yet")).Size(12))
	default:
		status = muted(widget.Text(Tf("%s · %d plugins · %d blocked · read %s", nameOr(r.GetName(), T("unnamed")), r.GetPlugins(), r.GetBlocked(),
			time.Unix(r.GetFetchedAt(), 0).Format("Jan 2 15:04"))).Size(12))
	}
	url := r.GetUrl()
	return widget.Box(widget.Row(
		icon("globe").Size(15).Color(skin.Muted),
		widget.Expanded(widget.Column(widget.Row(title...).Gap(8).Align(widget.AlignCenter), status).Gap(3).Stretch()),
		ui.Button(T("Remove"), func() { m.changeRegistry(protocol.RegistryRemove, url) }).Outline().Name(Tf("Remove %s", url)),
	).Gap(10).Align(widget.AlignCenter)).Pad(12, 16)
}

// registryLabel names a registry briefly: its host and path, without the
// index's file name a raw URL ends in.
func registryLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	p := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/mako-registry.json"), "/")
	return u.Host + p
}

// shortFingerprint is a key's fingerprint's first groups, as a person
// compares them at a glance; the whole is in its tooltip.
func shortFingerprint(fp string) string {
	if parts := strings.Split(fp, "-"); len(parts) > 4 {
		return strings.Join(parts[:4], "-") + "…"
	}
	return fp
}

func nameOr(a, b string) string {
	if a != "" {
		return fmt.Sprintf("%q", a)
	}
	return b
}
