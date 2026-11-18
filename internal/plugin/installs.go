package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	// Settle is told the plugin's folder changed, before it is read
	// again; it says the folder is no longer the one installed (written
	// over by hand), whose install it forgot: the manager then forgets
	// what the plugin was allowed and kept, which the folder written in
	// its place is not given.
	Settle(id string) (disowned bool)
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
func (noInstalls) Settle(string) bool                  { return false }

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

// Disown forgets what the plugin of the ID was allowed and kept on this
// node, its folder there or not, for a folder no longer the one
// installed: a runtime up is closed first, without the save that would
// keep its blocks' states for the folder now in its place.
func (m *Manager) Disown(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not a plugin's ID", id)
	}
	p, ok := m.plugin(id)
	if !ok {
		return m.forget(id)
	}
	ctx, cancel := context.WithTimeout(m.ctx, m.lim.Wall+5*time.Second)
	defer cancel()
	err := p.do(ctx, func() error { return p.disown() })
	if errors.Is(err, errGone) {
		return m.forget(id)
	}
	return err
}

// disown is Disown on the plugin's actor.
func (p *plugin) disown() error {
	p.closeRuntime()
	p.saved = map[string]string{}
	err := p.m.forget(p.id)
	p.store.reset()
	p.settings.reset()
	p.vault.reset()
	p.subs.reset()
	p.m.subsChanged()
	p.status(func() { p.running = false })
	return err
}

func (m *Manager) forget(id string) error {
	if err := m.grant.update(id, func(e *grantEntry) { *e = grantEntry{} }); err != nil {
		return err
	}
	if m.cfg.Layout != nil {
		m.cfg.Layout.Forget(id)
	}
	// The secrets in the keychain go first, by the names the data folder
	// lists; a secret not removed is said, its index kept to try again.
	if err := newVault(m, id).purge(); err != nil {
		m.cfg.Log.Warn("plugin's secrets were not all removed from the keychain", "plugin", id, "err", err)
		return fmt.Errorf("the plugin's secrets were not all removed from the keychain: %w", err)
	}
	return os.RemoveAll(filepath.Join(m.cfg.DataDir, id))
}

// Recheck has every plugin look again at how it stands as installed: a
// plugin newly blocked stops, and one no longer blocked starts if one of
// its blocks shows. Each plugin is told by a signal of its own, which a
// full queue of jobs cannot drop, and which a second Recheck before the
// first is taken joins.
func (m *Manager) Recheck() {
	for _, p := range m.all() {
		select {
		case p.recheck <- struct{}{}:
		default:
		}
	}
	m.cfg.Changed()
}

// restand is a Recheck on the plugin's actor.
func (p *plugin) restand() {
	if state, _ := p.state(); state == protocol.PluginBlocked {
		if p.rt != nil {
			p.closeRuntime()
			p.m.cfg.Log.Warn("plugin stopped: blocked", "plugin", p.id)
		}
		p.status(func() { p.running = false })
		return
	}
	p.status(func() {})
	p.showAll()
}

func nonce() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
