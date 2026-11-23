package store

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// SyncSnapshotData captures the store state replicated across mutually trusted
// peers: custom registries, whether the built-in default registry is removed,
// and settled store-installed plugins.
type SyncSnapshotData struct {
	Added     []string
	NoDefault bool
	Installs  map[string]Install
}

// SyncSnapshot returns a copy of the node's registries and settled store
// installs for peer-to-peer config sync.
func (s *Store) SyncSnapshot() SyncSnapshotData {
	s.mu.Lock()
	defer s.mu.Unlock()
	added := slices.Clone(s.settings.Added)
	slices.Sort(added)
	installs := make(map[string]Install, len(s.installs))
	for id, in := range s.installs {
		if in.settled() {
			installs[id] = in
		}
	}
	return SyncSnapshotData{
		Added:     added,
		NoDefault: s.settings.NoDefault,
		Installs:  installs,
	}
}

// ApplySyncedRegistries reconciles the node's registry list to desiredAdded and
// noDefault without returning an error for already-present or missing entries.
// Any newly added registries have their index fetched using ctx.
func (s *Store) ApplySyncedRegistries(ctx context.Context, desiredAdded []string, noDefault bool) (changed bool) {
	var canonical []string
	for _, raw := range desiredAdded {
		canon, _, err := registry.Normalize(raw, s.cfg.Loopback)
		if err != nil {
			continue
		}
		if s.cfg.Default != "" {
			if def, _, err := registry.Normalize(s.cfg.Default, s.cfg.Loopback); err == nil && def == canon {
				continue
			}
		}
		if !slices.Contains(canonical, canon) {
			canonical = append(canonical, canon)
		}
	}
	if len(canonical) > 32 {
		canonical = canonical[:32]
	}

	s.mu.Lock()
	curAdded := slices.Clone(s.settings.Added)
	slices.Sort(curAdded)
	sortedDesired := slices.Clone(canonical)
	slices.Sort(sortedDesired)
	if slices.Equal(curAdded, sortedDesired) && s.settings.NoDefault == noDefault {
		s.mu.Unlock()
		return false
	}

	next := registrySettings{Added: canonical, NoDefault: noDefault}
	if err := writeJSON(s.path("registries.json"), next); err != nil {
		s.mu.Unlock()
		s.cfg.Log.Warn("synced registries not kept", "err", err)
		return false
	}

	oldRegs := map[string]*reg{}
	for _, r := range s.regs {
		oldRegs[r.url] = r
	}
	s.settings = next
	rebuilt := s.loadRegs()
	var toFetch []*reg
	for i, r := range rebuilt {
		if prev, ok := oldRegs[r.url]; ok {
			prev.builtin = r.builtin
			rebuilt[i] = prev
			delete(oldRegs, r.url)
		} else {
			toFetch = append(toFetch, r)
		}
	}
	s.regs = rebuilt
	s.mu.Unlock()

	for url := range oldRegs {
		_ = removeFile(s.cachePath(url))
	}
	for _, r := range toFetch {
		s.read(ctx, r)
	}
	s.recheck()
	return true
}

// EnsureSyncedInstall installs or updates a store plugin requested by peer sync.
// It never overwrites a local development folder, verifies the publisher key
// against pinnedKey when provided, and leaves permissions ungranted so the user
// approves them explicitly on this node.
func (s *Store) EnsureSyncedInstall(ctx context.Context, id, registryURL, pinnedKey, version string) (bool, error) {
	if s.man == nil || !plugin.ValidID(id) {
		return false, nil
	}
	if s.devFolder(id) {
		return false, nil
	}
	if prev, had := s.Installed(id); had {
		if pinnedKey != "" && !pack.SameKey(prev.Key, pinnedKey) {
			return false, fmt.Errorf("synced plugin %s publisher key mismatch with local pin", id)
		}
		if version == "" || plugin.CompareVersions(version, prev.Version) <= 0 {
			return false, nil
		}
		if _, err := s.Install(ctx, protocol.UpdateOp, id, registryURL); err != nil {
			if protocol.HasCode(err, protocol.CodeInstalledAlready) || protocol.HasCode(err, protocol.CodeDowngrade) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}

	// First install via sync: ensure registry index is fresh and publisher key
	// matches what the peer pinned before installing.
	if pinnedKey != "" {
		s.refreshOlder(ctx, 0)
		s.mu.Lock()
		canon := ""
		if registryURL != "" {
			if c, _, err := registry.Normalize(registryURL, s.cfg.Loopback); err == nil {
				canon = c
			}
		}
		_, e, err := s.pickLocked(id, canon)
		s.mu.Unlock()
		if err != nil {
			return false, err
		}
		if !pack.SameKey(e.Key, pinnedKey) {
			return false, fmt.Errorf("synced plugin %s key in registry does not match peer pinned key", id)
		}
	}
	if _, err := s.Install(ctx, protocol.InstallOp, id, registryURL); err != nil {
		if protocol.HasCode(err, protocol.CodeInstalledAlready) || protocol.HasCode(err, protocol.CodeDevFolder) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RemoveSyncedInstall removes a store-installed plugin when peer sync records
// its removal. Local development folders are never removed.
func (s *Store) RemoveSyncedInstall(id string) (bool, error) {
	if s.man == nil || !plugin.ValidID(id) {
		return false, nil
	}
	if s.devFolder(id) {
		return false, nil
	}
	if _, had := s.Installed(id); !had {
		return false, nil
	}
	if err := s.Remove(id); err != nil {
		if protocol.HasCode(err, protocol.CodeNotInstalled) || protocol.HasCode(err, protocol.CodeDevFolder) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CloneInstalls copies an installs map for safe comparison.
func CloneInstalls(in map[string]Install) map[string]Install {
	return maps.Clone(in)
}
