package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"

	"github.com/ironpark/mako/internal/plugin/pack"
)

// A plugin installed is two things that must agree: its folder, which
// the store wrote from a package, and its pin in installs.json, naming
// the package's contents' digest. Neither is trusted to name the other:
// whether the folder is still the one installed is read from the folder
// itself, its signature's file and its contents' digest, before the store
// takes it for the install (to list it, update it or remove it).
//
// A folder that is not what was pinned was written by hand, in place of
// the plugin installed or over it: it is a plugin in development, never
// replaced or deleted by the store. Its pin is dropped, and what the
// plugin installed was allowed and kept on this node is forgotten, as a
// removal forgets it: the folder is another plugin, which has not been
// allowed anything.
//
// An install writes the pin of the version it puts in place first, as
// pending beside the pin it replaces, then swaps the folder, then makes
// the pending pin the pin. Whichever of these a crash cuts short, the
// folder found afterwards is one of the two pinned, by its digest, and
// the pins are settled to it; the folders an install or a removal stages
// beside the plugins' are removed, or, for a folder moved aside to make
// way and not yet replaced, put back.

// standing is how a plugin's folder stands against its pin.
type standing int

const (
	missing     standing = iota // there is no folder
	handWritten                 // a folder that is neither pinned version
	asPinned                    // the version pinned
	asPending                   // the version an install was putting in place
)

// examine reads the plugin's folder against its pin.
func (s *Store) examine(id string, in Install) standing {
	dir := filepath.Join(s.man.Dir(), id)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return missing
	}
	digest, sig, err := pack.ReadInstalled(dir)
	if err != nil || sig == nil || sig.Contents != digest {
		return handWritten
	}
	is := func(p *Install) bool {
		return p != nil && p.Contents != "" && p.Contents == digest && sig.Version == p.Version && pack.SameKey(sig.Key, p.Key)
	}
	switch {
	case is(in.Pending):
		return asPending
	case is(&in):
		return asPinned
	}
	return handWritten
}

// promoted is the pin of the version pending.
func (in Install) promoted() Install {
	next := *in.Pending
	next.Pending = nil
	return next
}

// casInstall replaces the pin of the ID by next (nil to drop it) if it
// is still was: a pin changed meanwhile is left as it is.
func (s *Store) casInstall(id string, was Install, next *Install) bool {
	s.mu.Lock()
	cur, ok := s.installs[id]
	s.mu.Unlock()
	if !ok || !reflect.DeepEqual(cur, was) {
		return false
	}
	return s.putInstall(id, next) == nil
}

// Settle makes the store a plugin.Installs: it is told the plugin's
// folder changed, before the folder is read, and settles the pin to it.
// A folder that is the version pending is the install's; one that is
// neither, with no install under way, is a folder written by hand, whose
// pin it drops, saying so, for the manager to forget what the plugin
// installed was allowed and kept. A folder missing says nothing here: a
// swap may be under way, and Reconcile settles a folder gone.
func (s *Store) Settle(id string) (disowned bool) {
	if s.man == nil {
		return false
	}
	s.mu.Lock()
	in, ok := s.installs[id]
	s.mu.Unlock()
	if !ok {
		return false
	}
	switch s.examine(id, in) {
	case asPending:
		next := in.promoted()
		s.casInstall(id, in, &next)
	case handWritten:
		if in.Pending == nil && s.casInstall(id, in, nil) {
			s.cfg.Log.Warn("plugin's folder is not the one installed: it is taken for a plugin in development", "plugin", id)
			return true
		}
	}
	return false
}

// leftover matches what an install or a removal stages in the plugins'
// folder: a package being written, a folder moved aside to make way for
// its replacement, and one moved aside to be removed.
var (
	leftoverStage = regexp.MustCompile(`^\.install-[a-z0-9-]+-[0-9a-f]{12}$`)
	leftoverAside = regexp.MustCompile(`^\.([a-z0-9-]+)\.(old|removed)-[0-9a-f]{12}$`)
)

// Reconcile settles what a daemon stopped midway left: the folders
// staged, and every pin against its folder. The daemon runs it before
// the manager first reads the folder, and an install or a removal before
// it starts.
func (s *Store) Reconcile() {
	if s.man == nil {
		return
	}
	s.installMu.Lock()
	defer s.installMu.Unlock()
	s.reconcileLocked()
}

// reconcileLocked is Reconcile with installMu held.
func (s *Store) reconcileLocked() {
	dir := s.man.Dir()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if leftoverStage.MatchString(name) {
			_ = os.RemoveAll(filepath.Join(dir, name))
			continue
		}
		m := leftoverAside.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		// A folder moved aside for its replacement, which never came:
		// it is the plugin, put back.
		if _, err := os.Lstat(filepath.Join(dir, m[1])); m[2] == "old" && errors.Is(err, fs.ErrNotExist) {
			if os.Rename(filepath.Join(dir, name), filepath.Join(dir, m[1])) == nil {
				s.cfg.Log.Info("plugin's folder put back after an install cut short", "plugin", m[1])
				continue
			}
		}
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
	s.mu.Lock()
	pins := make(map[string]Install, len(s.installs))
	for id, in := range s.installs {
		pins[id] = in
	}
	s.mu.Unlock()
	for id, in := range pins {
		switch s.examine(id, in) {
		case missing:
			// Removed, by hand or by a removal cut short.
			if s.casInstall(id, in, nil) {
				_ = s.man.Disown(id)
				s.cfg.Log.Info("plugin's folder is gone: its install is forgotten", "plugin", id)
			}
		case asPending:
			next := in.promoted()
			s.casInstall(id, in, &next)
		case asPinned:
			if in.Pending != nil {
				next := in
				next.Pending = nil
				s.casInstall(id, in, &next)
			}
		case handWritten:
			if s.casInstall(id, in, nil) {
				_ = s.man.Disown(id)
				s.cfg.Log.Warn("plugin's folder is not the one installed: it is taken for a plugin in development", "plugin", id)
			}
		}
	}
}
