package plugin

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Bundled plugins ship inside mako itself (Config.Bundled) and may be
// overridden on disk during development (Config.BundledDev). They are loaded
// before the first user-folder scan, are trusted with every permission their
// manifest declares (while still honouring user disable in grants.json), and
// cannot be removed or replaced through the plugin store.

// loadBundled registers and starts every plugin found in Config.BundledDev or
// Config.Bundled before the first user-folder scan.
func (m *Manager) loadBundled() {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()

	devDirs := map[string]bool{}
	if m.cfg.BundledDev != "" {
		if entries, err := os.ReadDir(m.cfg.BundledDev); err == nil {
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, ".") || !pluginID.MatchString(name) {
					continue
				}
				if fi, err := os.Lstat(filepath.Join(m.cfg.BundledDev, name)); err == nil && fi.IsDir() {
					devDirs[name] = true
				}
			}
		}
	}

	embeddedIDs := map[string]bool{}
	if m.cfg.Bundled != nil {
		if entries, err := fs.ReadDir(m.cfg.Bundled, "."); err == nil {
			for _, e := range entries {
				name := e.Name()
				if !e.IsDir() || strings.HasPrefix(name, ".") || !pluginID.MatchString(name) {
					continue
				}
				embeddedIDs[name] = true
			}
		}
	}

	var ids []string
	seen := map[string]bool{}
	for id := range devDirs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for id := range embeddedIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)

	for _, id := range ids {
		p := newPlugin(m, id)
		p.bundled = true
		if devDirs[id] {
			p.devDir = m.cfg.BundledDev
			p.noteFolder(fingerprint(m.cfg.BundledDev, id))
		} else {
			sub, err := fs.Sub(m.cfg.Bundled, id)
			if err != nil {
				m.cfg.Log.Warn("bundled plugin sub-fs failed", "plugin", id, "err", err)
				continue
			}
			p.bundledFS = sub
		}
		m.mu.Lock()
		m.plugins[id] = p
		m.wg.Go(func() { p.run(m.ctx) })
		m.mu.Unlock()
		p.post(func() { p.reload() })
	}
}

// scanBundledDev checks Config.BundledDev for changes to bundled plugins during
// development so edits hot-reload on the poll interval. Caller holds m.scanMu.
func (m *Manager) scanBundledDev() {
	if m.cfg.BundledDev == "" {
		return
	}
	entries, err := os.ReadDir(m.cfg.BundledDev)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !pluginID.MatchString(name) {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(m.cfg.BundledDev, name)); err != nil || !fi.IsDir() {
			continue
		}
		print := fingerprint(m.cfg.BundledDev, name)
		m.mu.Lock()
		p, ok := m.plugins[name]
		if !ok {
			p = newPlugin(m, name)
			p.bundled = true
			p.devDir = m.cfg.BundledDev
			m.plugins[name] = p
			m.wg.Go(func() { p.run(m.ctx) })
		}
		isDevBundled := p.bundled && p.devDir != ""
		m.mu.Unlock()
		if isDevBundled && p.noteFolder(print) {
			p.post(func() { p.reload() })
		}
	}
}

func (m *Manager) isBundled(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	return ok && p.bundled
}

// readVersion reads the plugin's next version from its embedded FS, its
// BundledDev override folder, or the user plugins folder.
func (p *plugin) readVersion() (*version, error) {
	if p.bundled {
		if p.devDir != "" {
			return readVersionDir(p.devDir, p.id, true)
		}
		return readVersionFS(p.bundledFS, p.id, "", true)
	}
	return readVersion(p.m.cfg.Dir, p.id)
}

// grantForLocked returns the effective grantEntry for p: bundled plugins are
// granted every permission their manifest declares, while still honouring
// Disabled from grants.json. Caller holds p.mu.
func (p *plugin) grantForLocked(g grantEntry) grantEntry {
	if !p.bundled || p.v == nil {
		return g
	}
	g.Granted = slices.Clone(p.v.man.Permissions)
	g.Denied = nil
	return g
}
