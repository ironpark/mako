package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// Some plugins of the folder were installed from a registry's package
// (internal/plugin/store), the rest written there by hand. The manager
// runs both alike; what it learns of one from Config.Installs is shown
// beside it, and a plugin a registry blocks does not run. The installer
// changes a plugin's folder only through Replace and Remove, which no
// reading of the folder sees half done.

// Installs tells the manager how each plugin stands as installed.
type Installs interface {
	// Status is how the plugin of the ID, at the version read from its
	// folder ("" if none was), stands.
	Status(id, version string) InstallStatus
}

// InstallStatus is how a plugin stands as installed.
type InstallStatus struct {
	// Installed says it came from a registry's package; Source is that
	// registry and Key the publisher key pinned.
	Installed bool
	Source    string
	Key       string
	// Update is a newer version its registry lists, signed by Key.
	Update string
	// Blocked is why a registry of the node blocks it, "" if none does.
	Blocked string
}

type noInstalls struct{}

func (noInstalls) Status(string, string) InstallStatus { return InstallStatus{} }

// installStatus is how the plugin stands as installed, as its version
// read says.
func (p *plugin) installStatus() InstallStatus {
	version := ""
	if v := p.current(); v != nil {
		version = v.man.Version
	}
	return p.m.installs.Status(p.id, version)
}

// Dir is the plugins' folder. A folder made in it whose name starts with
// a dot is not read as a plugin: the installer stages there.
func (m *Manager) Dir() string { return m.cfg.Dir }

// Replace puts the folder stage, of the plugins' folder, in place of the
// plugin's, if it has one, and reads the folder again. No reading of the
// folder runs meanwhile, so the plugin is never seen gone, and a version
// running is replaced as a folder changed in development is.
func (m *Manager) Replace(id, stage string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not a plugin's ID", id)
	}
	err := func() error {
		m.scanMu.Lock()
		defer m.scanMu.Unlock()
		dst := filepath.Join(m.cfg.Dir, id)
		old := ""
		if _, err := os.Lstat(dst); err == nil {
			old = filepath.Join(m.cfg.Dir, "."+id+".old-"+nonce())
			if err := os.Rename(dst, old); err != nil {
				return err
			}
		}
		if err := os.Rename(stage, dst); err != nil {
			if old != "" {
				_ = os.Rename(old, dst)
			}
			return err
		}
		if old != "" {
			_ = os.RemoveAll(old)
		}
		return nil
	}()
	if err != nil {
		return err
	}
	m.scan()
	return nil
}

// Remove takes the plugin's folder away, stops the plugin, and forgets
// what it was allowed and what it kept on this node.
func (m *Manager) Remove(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not a plugin's ID", id)
	}
	p, had := m.plugin(id)
	err := func() error {
		m.scanMu.Lock()
		defer m.scanMu.Unlock()
		gone := filepath.Join(m.cfg.Dir, "."+id+".removed-"+nonce())
		if err := os.Rename(filepath.Join(m.cfg.Dir, id), gone); err != nil {
			return err
		}
		return os.RemoveAll(gone)
	}()
	if err != nil {
		return err
	}
	m.scan()
	if had {
		// What it keeps is removed once it can no longer write it.
		select {
		case <-p.exited:
		case <-time.After(m.lim.Wall + 5*time.Second):
		}
	}
	return m.forget(id)
}

// Forget clears what the plugin of the ID was allowed and kept on this
// node, before a first install of that ID: a plugin installed is not
// given what another of its ID was. A plugin of the folder is refused.
func (m *Manager) Forget(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not a plugin's ID", id)
	}
	if _, err := os.Lstat(filepath.Join(m.cfg.Dir, id)); err == nil {
		return fmt.Errorf("plugins/%s is there: what it was allowed stays", id)
	}
	return m.forget(id)
}

func (m *Manager) forget(id string) error {
	if err := m.grant.update(id, func(e *grantEntry) { *e = grantEntry{} }); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(m.cfg.DataDir, id))
}

// Recheck has every plugin look again at how it stands as installed: a
// plugin newly blocked stops, and one no longer blocked starts if one of
// its blocks shows.
func (m *Manager) Recheck() {
	for _, p := range m.all() {
		p.post(func() {
			if state, _ := p.state(); state == protocol.PluginBlocked {
				if p.rt != nil {
					p.closeRuntime()
					m.cfg.Log.Warn("plugin stopped: blocked", "plugin", p.id)
				}
				p.status(func() { p.running = false })
				return
			}
			p.status(func() {})
			p.showAll()
		})
	}
	m.cfg.Changed()
}

func nonce() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
