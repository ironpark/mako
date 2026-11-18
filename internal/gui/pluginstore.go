package gui

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// The plugin store, in Settings ▸ Plugins: the plugins the daemon's
// registries list, searched across them all, installed, updated and
// removed by the daemon (internal/plugin/store), and the registries it
// reads. Nobody reviews a registry, so the store names, for every plugin,
// the registry it is from and its publisher's key, and nothing in it reads
// as "verified".

// storeState is the store as the window last had it. Its fields are the
// UI goroutine's.
type storeState struct {
	Query   *ggui.StateValue[string]
	Results *ggui.StateValue[storeSearch]
	Busy    *ggui.StateValue[map[string]bool] // plugins being installed, updated or removed
	Err     *ggui.StateValue[string]
	// Registries are the daemon's, and RegistryURL the one being added.
	Registries  *ggui.StateValue[[]*protocol.RegistryInfo]
	RegistryURL *ggui.StateValue[string]
	// Known are the listings of plugins whose blocks a document places
	// but this node lacks, by ID, for their placeholders to offer them.
	Known *ggui.StateValue[map[string][]*protocol.PluginListing]
	// NewKey are the plugins installed by another publisher key than the
	// one this node had for their IDs, by ID: the fingerprints before and
	// now, said where the plugin asks to be allowed.
	NewKey *ggui.StateValue[map[string][2]string]
	asking map[string]bool
}

// storeSearch is the registries' answer to a search.
type storeSearch struct {
	Query    string
	Listings []*protocol.PluginListing
	Err      string
	Loading  bool
}

func newStoreState() *storeState {
	return &storeState{
		Query:       ggui.State(""),
		Results:     ggui.State(storeSearch{}).WithEqual(deepEqual),
		Busy:        ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		Err:         ggui.State(""),
		Registries:  ggui.State([]*protocol.RegistryInfo(nil)).WithEqual(deepEqual),
		RegistryURL: ggui.State(""),
		Known:       ggui.State(map[string][]*protocol.PluginListing{}).WithEqual(deepEqual),
		NewKey:      ggui.State(map[string][2]string{}).WithEqual(maps.Equal),
		asking:      map[string]bool{},
	}
}

// searchStore asks the daemon's registries for the plugins matching
// query.
func (m *model) searchStore(query string) {
	if m.post == nil {
		return
	}
	query = strings.TrimSpace(query)
	m.store.Results.Set(storeSearch{Query: query, Listings: ggui.Peek(m.store.Results).Listings, Loading: true})
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.PluginListings, error) {
		return c.SearchPlugins(ctx, &protocol.SearchPlugins{Query: query})
	}, func(found *protocol.PluginListings, err error) {
		if ggui.Peek(m.store.Results).Query != query {
			return // another search went out meanwhile
		}
		r := storeSearch{Query: query, Listings: found.GetListings()}
		if err != nil {
			r.Err = err.Error()
		}
		m.store.Results.Set(r)
		m.loadRegistries() // reading them may have failed, or come back
	})
}

// loadRegistries lists the daemon's registries again.
func (m *model) loadRegistries() {
	if m.post == nil {
		return
	}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Registries, error) {
		return c.Registries(ctx)
	}, func(regs *protocol.Registries, err error) {
		if err == nil {
			m.store.Registries.Set(regs.GetRegistries())
		}
	})
}

// changeRegistry adds, removes or reads again the daemon's registries,
// then searches again, for the listings to follow.
func (m *model) changeRegistry(op, url string) {
	if m.post == nil {
		return
	}
	m.store.Err.Set("")
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Registries, error) {
		return c.RegistryAction(ctx, &protocol.RegistryAction{Op: op, Url: url})
	}, func(regs *protocol.Registries, err error) {
		if err != nil {
			m.store.Err.Set(err.Error())
			return
		}
		if op == protocol.RegistryAdd {
			m.store.RegistryURL.Set("")
		}
		m.store.Registries.Set(regs.GetRegistries())
		m.store.Known.Set(map[string][]*protocol.PluginListing{})
		m.store.asking = map[string]bool{}
		m.searchStore(ggui.Peek(m.store.Query))
	})
}

// installPlugin installs, updates or removes a plugin by the daemon. What
// it asks for is then asked in the window's bar, as for any plugin.
func (m *model) installPlugin(op, id, registry string) {
	if m.post == nil {
		return
	}
	busy := func(on bool) {
		next := maps.Clone(ggui.Peek(m.store.Busy))
		if on {
			next[id] = true
		} else {
			delete(next, id)
		}
		m.store.Busy.Set(next)
	}
	busy(true)
	m.store.Err.Set("")
	fetch(m, 3*time.Minute, func(ctx context.Context, c *client.Client) (*protocol.PluginInstalled, error) {
		return c.InstallPlugin(ctx, &protocol.InstallPlugin{Op: op, Id: id, Registry: registry})
	}, func(done *protocol.PluginInstalled, err error) {
		busy(false)
		if err != nil {
			m.store.Err.Set(err.Error())
		}
		m.store.NewKey.Update(func(k map[string][2]string) map[string][2]string {
			next := maps.Clone(k)
			delete(next, id)
			if prev := done.GetPreviousFingerprint(); err == nil && prev != "" {
				next[id] = [2]string{prev, done.GetFingerprint()}
			}
			return next
		})
		// What is asked for is asked at once, whatever was put off before.
		m.plugins.Later.Update(func(l map[string]bool) map[string]bool {
			next := maps.Clone(l)
			delete(next, id)
			return next
		})
		m.link.pluginsStale.Store(true)
		m.link.pollNow()
		delete(m.store.asking, id)
		m.lookUpPlugin(id)
		if r := ggui.Peek(m.store.Results); r.Listings != nil || r.Query != "" {
			m.searchStore(r.Query)
		}
	})
}

// lookUpPlugin asks the registries once for a plugin whose block a
// document places but this node lacks, for its placeholder to offer it.
func (m *model) lookUpPlugin(id string) {
	if m.post == nil || m.store.asking[id] {
		return
	}
	m.store.asking[id] = true
	fetch(m, 40*time.Second, func(ctx context.Context, c *client.Client) (*protocol.PluginListings, error) {
		return c.SearchPlugins(ctx, &protocol.SearchPlugins{Id: id})
	}, func(found *protocol.PluginListings, err error) {
		if err != nil {
			return
		}
		m.store.Known.Update(func(k map[string][]*protocol.PluginListing) map[string][]*protocol.PluginListing {
			next := maps.Clone(k)
			next[id] = found.GetListings()
			return next
		})
	})
}

// storeOpen says this window's daemon has registries to install from.
func (m *model) storeOpen() bool {
	return ggui.Peek(m.Conn).Self.GetRevision() >= protocol.RevisionPluginStore
}
