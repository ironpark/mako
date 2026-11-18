package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// downloadWithin bounds a package's download.
const downloadWithin = 60 * time.Second

// Install installs the plugin id from the registry ("" when one registry
// alone lists it), or updates it, op protocol.UpdateOp, from the
// registry it was installed from unless another is named. It returns
// the version installed and the permissions it asks for that the
// version it replaced did not.
func (s *Store) Install(ctx context.Context, op, id, registryURL string) (*protocol.PluginInstalled, error) {
	if s.man == nil {
		return nil, errors.New("the node runs no plugins")
	}
	if !plugin.ValidID(id) {
		return nil, fmt.Errorf("%q is not a plugin's ID", id)
	}
	s.installMu.Lock()
	defer s.installMu.Unlock()
	prev, had := s.Installed(id)
	if op == protocol.UpdateOp && !had {
		return nil, fmt.Errorf("%s was not installed from a registry: there is nothing to update", id)
	}
	if registryURL != "" {
		canon, _, err := registry.Normalize(registryURL, s.cfg.Loopback)
		if err != nil {
			return nil, err
		}
		registryURL = canon
	} else if had {
		registryURL = prev.Registry
	}
	// Every registry is read first, for its blocklist; one out of reach
	// counts with the list it gave last.
	s.refreshOlder(ctx, 0)
	s.recheck()

	s.mu.Lock()
	r, e, err := s.pickLocked(id, registryURL)
	var v *registry.Version
	if err == nil {
		if v = e.Newest(plugin.APIVersion); v == nil {
			err = fmt.Errorf("%s lists no version of %s for this mako, which carries out Host API %d", r.url, id, plugin.APIVersion)
		}
	}
	if err == nil {
		err = s.checkListingLocked(id, r, e, v, prev, had)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}

	pkg, data, err := s.download(ctx, r, e, v)
	if err != nil {
		return nil, err
	}
	if had && pkg.Key != prev.Key {
		return nil, fmt.Errorf("the package is signed by %s, not by the key pinned for %s", pack.Fingerprint(pkg.Key), id)
	}
	if !had && s.devFolder(id) {
		return nil, devFolderErr(id)
	}
	if err := os.MkdirAll(s.man.Dir(), 0o755); err != nil {
		return nil, err
	}
	stage := filepath.Join(s.man.Dir(), ".install-"+id+"-"+nonce())
	if err := pkg.WriteTo(stage); err != nil {
		_ = os.RemoveAll(stage)
		return nil, fmt.Errorf("the package was not written: %w", err)
	}
	if !had {
		// A first install starts with nothing allowed and nothing kept,
		// whatever a plugin of the ID before it was allowed or kept.
		if err := s.man.Forget(id); err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
	}
	sum := sha256.Sum256(data)
	next := Install{Registry: r.url, Key: pkg.Key, Version: pkg.Manifest.Version, SHA256: hex.EncodeToString(sum[:]),
		Contents: pkg.Digest, Permissions: pkg.Manifest.Permissions, At: s.cfg.Now().UTC()}
	if err := s.putInstall(id, &next); err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}
	if err := s.man.Replace(id, stage); err != nil {
		_ = os.RemoveAll(stage)
		if had {
			_ = s.putInstall(id, &prev)
		} else {
			_ = s.putInstall(id, nil)
		}
		return nil, fmt.Errorf("the plugin was not put in place: %w", err)
	}
	var added []string
	for _, p := range pkg.Manifest.Permissions {
		if !had || !slices.Contains(prev.Permissions, p) {
			added = append(added, p)
		}
	}
	s.cfg.Log.Info("plugin installed", "plugin", id, "version", next.Version, "registry", r.url,
		"key", pack.Fingerprint(next.Key), "asks", added)
	s.recheck()
	return &protocol.PluginInstalled{Id: id, Version: next.Version, Added: added}, nil
}

// pickLocked finds the registry and entry to install from: the one named,
// or the only registry that lists the ID.
func (s *Store) pickLocked(id, registryURL string) (*reg, *registry.Entry, error) {
	if registryURL != "" {
		r := s.regLocked(registryURL)
		switch {
		case r == nil:
			return nil, nil, fmt.Errorf("%s is not a registry of this node: add it first", registryURL)
		case r.idx == nil:
			return nil, nil, fmt.Errorf("%s could not be read: %s", r.url, cmpOr(r.err, "not read yet"))
		}
		e := r.idx.Find(id)
		if e == nil {
			return nil, nil, fmt.Errorf("%s does not list %s", r.url, id)
		}
		return r, e, nil
	}
	var found []*reg
	for _, r := range s.regs {
		if r.idx != nil && r.idx.Find(id) != nil {
			found = append(found, r)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil, fmt.Errorf("no registry of this node lists %s", id)
	case 1:
		return found[0], found[0].idx.Find(id), nil
	}
	urls := make([]string, len(found))
	for i, r := range found {
		urls[i] = r.url
	}
	return nil, nil, fmt.Errorf("%s is listed by %d registries, perhaps by different publishers: name the one to install from (%s)",
		id, len(found), strings.Join(urls, ", "))
}

// checkListingLocked refuses what may not be installed: a version blocked,
// one signed by another key than the one pinned, one not newer than the
// one installed, or one over a folder in development.
func (s *Store) checkListingLocked(id string, r *reg, e *registry.Entry, v *registry.Version, prev Install, had bool) error {
	if why := s.blockedLocked(id, v.Version, e.Key); why != "" {
		return fmt.Errorf("%s %s is %s", id, v.Version, why)
	}
	if !had {
		if s.devFolderLocked(id) {
			return devFolderErr(id)
		}
		return nil
	}
	if e.Key != prev.Key {
		return fmt.Errorf("%s lists %s signed by the publisher key %s, but this node pinned %s when it installed it from %s: "+
			"an update must be signed by the same key. To install the other publisher's, remove the plugin first",
			r.url, id, pack.Fingerprint(e.Key), pack.Fingerprint(prev.Key), prev.Registry)
	}
	switch c := plugin.CompareVersions(v.Version, prev.Version); {
	case c == 0:
		return fmt.Errorf("%s %s is installed already", id, prev.Version)
	case c < 0:
		return fmt.Errorf("%s lists %s %s, older than the %s installed: mako does not go back a version; remove the plugin and install it again to",
			r.url, id, v.Version, prev.Version)
	}
	return nil
}

func devFolderErr(id string) error {
	return fmt.Errorf("plugins/%s is a folder of a plugin in development, not installed from a registry: an install never replaces one. Move it away first", id)
}

// download fetches a version's package and checks it: its SHA-256 and
// size the index's, its signature by the entry's key, its manifest the
// listed ID and version.
func (s *Store) download(ctx context.Context, r *reg, e *registry.Entry, v *registry.Version) (*pack.Package, []byte, error) {
	data, err := s.fetch.Get(ctx, v.URL, pack.MaxPackageBytes, downloadWithin)
	if err != nil {
		return nil, nil, fmt.Errorf("the package of %s %s did not download: %w", e.ID, v.Version, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != v.SHA256 || int64(len(data)) != v.Size {
		return nil, nil, fmt.Errorf("the package of %s %s downloaded is not the one %s lists (its SHA-256 or size differs): "+
			"it was changed on the server or on the way. Nothing was installed", e.ID, v.Version, r.url)
	}
	pkg, err := pack.Open(data)
	if err != nil {
		return nil, nil, fmt.Errorf("the package of %s %s does not check: %w", e.ID, v.Version, err)
	}
	switch {
	case pkg.Key != e.Key:
		return nil, nil, fmt.Errorf("the package of %s %s is signed by %s, not by the publisher key %s lists, %s",
			e.ID, v.Version, pack.Fingerprint(pkg.Key), r.url, pack.Fingerprint(e.Key))
	case pkg.Manifest.ID != e.ID:
		return nil, nil, fmt.Errorf("%s lists a package of the plugin %s as %s", r.url, pkg.Manifest.ID, e.ID)
	case pkg.Manifest.Version != v.Version:
		return nil, nil, fmt.Errorf("%s lists a package of %s %s as %s", r.url, e.ID, pkg.Manifest.Version, v.Version)
	}
	return pkg, data, nil
}

// putInstall keeps an install, or forgets it with nil.
func (s *Store) putInstall(id string, in *Install) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]Install{}
	for k, v := range s.installs {
		next[k] = v
	}
	if in == nil {
		delete(next, id)
	} else {
		next[id] = *in
	}
	if err := writeJSON(s.path("installs.json"), next); err != nil {
		return err
	}
	s.installs = next
	return nil
}

// Remove removes a plugin installed from a registry, what it was allowed
// and kept on this node, and its pin: a later install is a first one.
func (s *Store) Remove(id string) error {
	if s.man == nil {
		return errors.New("the node runs no plugins")
	}
	s.installMu.Lock()
	defer s.installMu.Unlock()
	if _, ok := s.Installed(id); !ok {
		if s.devFolder(id) {
			return fmt.Errorf("plugins/%s is a folder of a plugin in development: mako removes only what it installed. Delete the folder yourself", id)
		}
		return fmt.Errorf("%s is not installed", id)
	}
	if err := s.man.Remove(id); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.putInstall(id, nil); err != nil {
		return err
	}
	s.cfg.Log.Info("plugin removed", "plugin", id)
	s.recheck()
	return nil
}

func nonce() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
