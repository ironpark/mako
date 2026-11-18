package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/protocol"
)

// maxListings bounds what a search answers.
const maxListings = 200

// Search lists the plugins of the node's registries matching query, by
// ID, name, description and publisher, or the plugin id alone: an entry
// for each registry listing one, saying which registry and which
// publisher key, and how it stands on this node. A registry not read
// lately is read first.
func (s *Store) Search(ctx context.Context, query, id string) []*protocol.PluginListing {
	s.refreshOlder(ctx, searchStale)
	query = strings.ToLower(strings.TrimSpace(query))
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*protocol.PluginListing
	for _, r := range s.regs {
		if r.idx == nil {
			continue
		}
		for _, e := range r.idx.Plugins {
			if id != "" && e.ID != id {
				continue
			}
			text := strings.ToLower(e.ID + " " + e.Name + " " + e.Description + " " + e.Publisher)
			if id == "" && query != "" && !strings.Contains(text, query) {
				continue
			}
			v := e.Newest(plugin.APIVersion)
			if v == nil {
				continue // made for another mako
			}
			l := &protocol.PluginListing{
				Id: e.ID, Registry: r.url, Version: v.Version, Publisher: e.Publisher, Key: e.Key,
				Fingerprint: pack.Fingerprint(e.Key), Name: e.Name, Description: e.Description, Homepage: e.Homepage,
				Permissions: v.Permissions, Blocked: s.blockedLocked(e.ID, v.Version, e.Key),
			}
			if in, ok := s.installs[e.ID]; ok {
				l.Installed, l.InstalledFrom, l.OtherKey = in.Version, in.Registry, in.Key != e.Key
			} else if s.devFolderLocked(e.ID) {
				l.Installed = "dev"
			}
			out = append(out, l)
		}
	}
	order := map[string]int{}
	for i, r := range s.regs {
		order[r.url] = i
	}
	slices.SortStableFunc(out, func(a, b *protocol.PluginListing) int {
		if c := strings.Compare(a.GetId(), b.GetId()); c != 0 {
			return c
		}
		return order[a.GetRegistry()] - order[b.GetRegistry()]
	})
	if len(out) > maxListings {
		out = out[:maxListings]
	}
	return out
}

// devFolder says the plugins' folder has a folder of the ID that was not
// installed from a registry: written by hand, in development.
func (s *Store) devFolder(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devFolderLocked(id)
}

func (s *Store) devFolderLocked(id string) bool {
	if s.man == nil {
		return false
	}
	if _, ok := s.installs[id]; ok {
		return false
	}
	_, err := os.Lstat(filepath.Join(s.man.Dir(), id))
	return err == nil
}
