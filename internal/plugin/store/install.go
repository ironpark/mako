package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
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
	// What a daemon stopped midway left is settled first, so that an
	// install cut short is retried as what it was.
	s.reconcileLocked()
	prev, had := s.Installed(id)
	if s.devFolder(id) {
		return nil, devFolderErr(id)
	}
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
			err = coded(protocol.CodeAPIMismatch, fmt.Errorf("%s lists no version of %s for this mako, which carries out Host API %d", r.url, id, plugin.APIVersion), id, r.url)
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
	if had && !pack.SameKey(pkg.Key, prev.Key) {
		return nil, coded(protocol.CodePinnedKey, fmt.Errorf("the package is signed by %s, not by the key pinned for %s", pack.Fingerprint(pkg.Key), id), id, prev.Registry)
	}
	// A folder written meanwhile, while the package downloaded.
	if s.devFolder(id) {
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
	// The version is pinned as pending before the folder is swapped, and
	// as installed after: a crash between leaves a pin naming the folder
	// in place, whichever it is (folder.go).
	pending := Install{Pending: &next}
	if had {
		pending = prev
		pending.Pending = &next
	}
	if err := s.putInstall(id, &pending); err != nil {
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
	if err := s.putInstall(id, &next); err != nil {
		// The folder is in place and its pin pending: the manager, as it
		// reads the folder, or the next start settles it.
		s.cfg.Log.Warn("plugin's install not kept; it is settled as its folder is read", "plugin", id, "err", err)
	}
	previous := ""
	if !had {
		previous = s.previousKey(id, next.Key)
	}
	s.rememberKey(id, next.Key)
	var added []string
	for _, p := range pkg.Manifest.Permissions {
		if !had || !slices.Contains(prev.Permissions, p) {
			added = append(added, p)
		}
	}
	s.cfg.Log.Info("plugin installed", "plugin", id, "version", next.Version, "registry", r.url,
		"key", pack.Fingerprint(next.Key), "asks", added)
	if previous != "" {
		s.cfg.Log.Warn("plugin installed by another publisher key than the one this node had for it",
			"plugin", id, "before", previous, "now", pack.Fingerprint(next.Key))
	}
	s.recheck()
	return &protocol.PluginInstalled{Id: id, Version: next.Version, Added: added, PreviousFingerprint: previous,
		Fingerprint: pack.Fingerprint(next.Key)}, nil
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
			return nil, nil, coded(protocol.CodeRegistryUnreachable, fmt.Errorf("%s could not be read: %s", r.url, cmpOr(r.err, "not read yet")), r.url)
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
		return nil, nil, coded(protocol.CodeNotListed, fmt.Errorf("no registry of this node lists %s", id), id)
	case 1:
		return found[0], found[0].idx.Find(id), nil
	}
	urls := make([]string, len(found))
	for i, r := range found {
		urls[i] = r.url
	}
	return nil, nil, coded(protocol.CodeRegistryNeeded, fmt.Errorf("%s is listed by %d registries, perhaps by different publishers: name the one to install from (%s)",
		id, len(found), strings.Join(urls, ", ")), id)
}

// checkListingLocked refuses what may not be installed: a version blocked,
// one signed by another key than the one pinned, or one not newer than
// the one installed.
func (s *Store) checkListingLocked(id string, r *reg, e *registry.Entry, v *registry.Version, prev Install, had bool) error {
	if why := s.blockedLocked(id, v.Version, e.Key); why != "" {
		return coded(protocol.CodePluginBlocked, fmt.Errorf("%s %s is %s", id, v.Version, why), id, v.Version, why)
	}
	if !had {
		return nil
	}
	if !pack.SameKey(e.Key, prev.Key) {
		return coded(protocol.CodePinnedKey, fmt.Errorf("%s lists %s signed by the publisher key %s, but this node pinned %s when it installed it from %s: "+
			"an update must be signed by the same key. To install the other publisher's, remove the plugin first",
			r.url, id, pack.Fingerprint(e.Key), pack.Fingerprint(prev.Key), prev.Registry), id, r.url)
	}
	switch c := plugin.CompareVersions(v.Version, prev.Version); {
	case c == 0:
		return coded(protocol.CodeInstalledAlready, fmt.Errorf("%s %s is installed already", id, prev.Version), id, prev.Version)
	case c < 0:
		return coded(protocol.CodeDowngrade, fmt.Errorf("%s lists %s %s, older than the %s installed: mako does not go back a version; remove the plugin and install it again to",
			r.url, id, v.Version, prev.Version), id, v.Version, prev.Version)
	}
	return nil
}

func devFolderErr(id string) error {
	return coded(protocol.CodeDevFolder, fmt.Errorf("plugins/%s is a folder of a plugin in development, not installed from a registry: an install never replaces one. Move it away first", id), id)
}

// download fetches a version's package and checks it: its SHA-256 and
// size the index's, its signature by the entry's key, its manifest the
// listed ID and version.
func (s *Store) download(ctx context.Context, r *reg, e *registry.Entry, v *registry.Version) (*pack.Package, []byte, error) {
	data, err := s.fetch.Get(ctx, v.URL, pack.MaxPackageBytes, downloadWithin)
	if err != nil {
		code := protocol.CodePackageUnreachable
		switch {
		case errors.Is(err, registry.ErrTimeout):
			code = protocol.CodeTimedOut
		case errors.Is(err, registry.ErrTooLarge):
			code = protocol.CodeTooLarge
		}
		return nil, nil, coded(code, fmt.Errorf("the package of %s %s did not download: %w", e.ID, v.Version, err), e.ID, v.Version)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != v.SHA256 || int64(len(data)) != v.Size {
		return nil, nil, coded(protocol.CodeHashMismatch, fmt.Errorf("the package of %s %s downloaded is not the one %s lists (its SHA-256 or size differs): "+
			"it was changed on the server or on the way. Nothing was installed", e.ID, v.Version, r.url), e.ID, v.Version, r.url)
	}
	pkg, err := pack.Open(data)
	if err != nil {
		return nil, nil, coded(protocol.CodeBadPackage, fmt.Errorf("the package of %s %s does not check: %w", e.ID, v.Version, err), e.ID, v.Version)
	}
	switch {
	case !pack.SameKey(pkg.Key, e.Key):
		return nil, nil, coded(protocol.CodeSignatureMismatch, fmt.Errorf("the package of %s %s is signed by %s, not by the publisher key %s lists, %s",
			e.ID, v.Version, pack.Fingerprint(pkg.Key), r.url, pack.Fingerprint(e.Key)), e.ID, v.Version, r.url)
	case pkg.Manifest.ID != e.ID:
		return nil, nil, coded(protocol.CodeBadPackage, fmt.Errorf("%s lists a package of the plugin %s as %s", r.url, pkg.Manifest.ID, e.ID), e.ID, v.Version)
	case pkg.Manifest.Version != v.Version:
		return nil, nil, coded(protocol.CodeBadPackage, fmt.Errorf("%s lists a package of %s %s as %s", r.url, e.ID, pkg.Manifest.Version, v.Version), e.ID, v.Version)
	}
	// The listing's permissions are what the store showed before the
	// package was fetched: a package asking for others is not the one
	// the user looked at.
	if more, fewer := permDiff(pkg.Manifest.Permissions, v.Permissions); len(more)+len(fewer) > 0 {
		var why []string
		if len(more) > 0 {
			why = append(why, fmt.Sprintf("asks for %s, which the listing does not show", strings.Join(more, ", ")))
		}
		if len(fewer) > 0 {
			why = append(why, fmt.Sprintf("does not ask for %s, which the listing shows", strings.Join(fewer, ", ")))
		}
		return nil, nil, coded(protocol.CodePermissionsDiffer, fmt.Errorf("the package of %s %s %s: it is not what %s lists. Nothing was installed",
			e.ID, v.Version, strings.Join(why, " and "), r.url), e.ID, v.Version, r.url)
	}
	return pkg, data, nil
}

// permDiff compares two sets of permissions: those of got that want does
// not hold, and those of want that got does not, each sorted.
func permDiff(got, want []string) (more, fewer []string) {
	for _, p := range got {
		if !slices.Contains(want, p) && !slices.Contains(more, p) {
			more = append(more, p)
		}
	}
	for _, p := range want {
		if !slices.Contains(got, p) && !slices.Contains(fewer, p) {
			fewer = append(fewer, p)
		}
	}
	slices.Sort(more)
	slices.Sort(fewer)
	return more, fewer
}

// putInstall keeps an install, or forgets it with nil.
func (s *Store) putInstall(id string, in *Install) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]Install{}
	maps.Copy(next, s.installs)
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
	s.reconcileLocked()
	if _, ok := s.Installed(id); !ok || s.devFolder(id) {
		if s.devFolder(id) {
			return coded(protocol.CodeDevFolder, fmt.Errorf("plugins/%s is a folder of a plugin in development: mako removes only what it installed. Delete the folder yourself", id), id)
		}
		return coded(protocol.CodeNotInstalled, fmt.Errorf("%s is not installed", id), id)
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

// coded is err with a code and what its sentence names, for a front-end
// to say it in its user's language (protocol's Code constants).
func coded(code string, err error, args ...string) error {
	return &protocol.CodedError{Code: code, Message: err.Error(), Args: args}
}
