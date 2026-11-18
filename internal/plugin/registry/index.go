// Package registry reads plugin registries: git repositories anyone
// keeps, holding one file, IndexName, at their root, which lists signed
// plugin packages and the registry's blocklist (docs/plugins.md,
// "Publishing"). Nobody reviews what a registry lists; a node reads the
// index strictly, within bounds, and trusts a package only once its hash
// and signature hold.
package registry

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/uiblock"
)

// IndexName is the index's file at the registry's root.
const IndexName = "mako-registry.json"

// Format is the index's format this mako reads.
const Format = "mako-registry/1"

// The bounds of an index.
const (
	MaxIndexBytes  = 4 << 20
	maxPlugins     = 5000
	maxVersions    = 64
	maxBlocks      = 5000
	maxName        = 80
	maxDescription = 300
	maxURL         = 2048
)

// Index is a registry's index.
type Index struct {
	Format    string   `json:"format"`
	Name      string   `json:"name"`
	Plugins   []*Entry `json:"plugins"`
	Blocklist []*Block `json:"blocklist,omitempty"`
}

// Entry is a plugin a registry lists: its versions, all signed by Key.
type Entry struct {
	ID          string     `json:"id"`
	Name        string     `json:"name,omitempty"`
	Description string     `json:"description,omitempty"`
	Homepage    string     `json:"homepage,omitempty"`
	Publisher   string     `json:"publisher"`
	Key         string     `json:"key"`
	Versions    []*Version `json:"versions"`
}

// Version is a package of a plugin: where it is, its SHA-256 and size,
// and the Host API version and permissions its manifest gives, which a
// listing shows before the package is fetched.
type Version struct {
	Version     string   `json:"version"`
	API         int      `json:"api"`
	URL         string   `json:"url"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
	Permissions []string `json:"permissions"`
}

// Block is an entry of a registry's blocklist: it blocks every plugin
// on the node that has each field it gives, the ID, the ID at a version,
// or the publisher key.
type Block struct {
	ID      string `json:"id,omitempty"`
	Version string `json:"version,omitempty"`
	Key     string `json:"key,omitempty"`
	Reason  string `json:"reason"`
}

// Parse reads an index strictly: a field it does not know, a value out
// of its grammar or bounds, an ID listed twice, or a format other than
// Format is refused, saying what. loopback lets packages be on this
// machine's loopback, for tests alone.
func Parse(data []byte, loopback bool) (*Index, error) {
	if len(data) > MaxIndexBytes {
		return nil, fmt.Errorf("the index is over %d MiB", MaxIndexBytes>>20)
	}
	var idx Index
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&idx); err != nil {
		var probe struct {
			Format string `json:"format"`
		}
		if json.Unmarshal(data, &probe) == nil && probe.Format != "" && probe.Format != Format {
			return nil, fmt.Errorf("the index is of format %q, for a newer mako than this", short(probe.Format))
		}
		return nil, fmt.Errorf("the index does not read: %v", err)
	}
	if dec.More() {
		return nil, errors.New("the index has more after its end")
	}
	if idx.Format != Format {
		return nil, fmt.Errorf("the index is of format %q; this mako reads %s", short(idx.Format), Format)
	}
	if err := checkText("the registry's name", idx.Name, maxName); err != nil {
		return nil, err
	}
	if len(idx.Plugins) > maxPlugins {
		return nil, fmt.Errorf("the index lists more than %d plugins", maxPlugins)
	}
	if len(idx.Blocklist) > maxBlocks {
		return nil, fmt.Errorf("the blocklist has more than %d entries", maxBlocks)
	}
	ids := map[string]bool{}
	for _, e := range idx.Plugins {
		if e == nil {
			return nil, errors.New("the index lists null")
		}
		if err := e.check(loopback); err != nil {
			return nil, err
		}
		if ids[e.ID] {
			return nil, fmt.Errorf("the index lists %s twice", e.ID)
		}
		ids[e.ID] = true
	}
	for _, b := range idx.Blocklist {
		if b == nil {
			return nil, errors.New("the blocklist holds null")
		}
		if err := b.check(); err != nil {
			return nil, err
		}
	}
	return &idx, nil
}

func (e *Entry) check(loopback bool) error {
	if !plugin.ValidID(e.ID) {
		return fmt.Errorf("%q is not a plugin's ID", short(e.ID))
	}
	in := func(err error) error { return fmt.Errorf("%s: %w", e.ID, err) }
	if !plugin.ValidPublisher(e.Publisher) {
		return in(fmt.Errorf("publisher %q is not lowercase letters, digits and hyphens", short(e.Publisher)))
	}
	if _, err := pack.ParseKey(e.Key); err != nil {
		return in(err)
	}
	for _, t := range []struct {
		what, s string
		max     int
	}{{"name", e.Name, maxName}, {"description", e.Description, maxDescription}} {
		if err := checkText(t.what, t.s, t.max); err != nil {
			return in(err)
		}
	}
	if e.Homepage != "" {
		if err := CheckURL(e.Homepage, false); err != nil {
			return in(fmt.Errorf("homepage: %w", err))
		}
	}
	if len(e.Versions) == 0 || len(e.Versions) > maxVersions {
		return in(fmt.Errorf("a plugin lists 1 to %d versions", maxVersions))
	}
	seen := map[string]bool{}
	for _, v := range e.Versions {
		if v == nil {
			return in(errors.New("a version is null"))
		}
		if !plugin.ValidVersion(v.Version) || seen[v.Version] {
			return in(fmt.Errorf("version %q is not major.minor.patch, or listed twice", short(v.Version)))
		}
		seen[v.Version] = true
		if v.API < 1 || v.API > 1000 {
			return in(fmt.Errorf("%s: api %d", v.Version, v.API))
		}
		if len(v.URL) > maxURL {
			return in(fmt.Errorf("%s: the URL is too long", v.Version))
		}
		if err := CheckURL(v.URL, loopback); err != nil {
			return in(fmt.Errorf("%s: %w", v.Version, err))
		}
		if b, err := hex.DecodeString(v.SHA256); err != nil || len(b) != 32 || strings.ToLower(v.SHA256) != v.SHA256 {
			return in(fmt.Errorf("%s: sha256 is 64 lowercase hex digits", v.Version))
		}
		if v.Size <= 0 || v.Size > pack.MaxPackageBytes {
			return in(fmt.Errorf("%s: size %d is not of a package (at most %d MiB)", v.Version, v.Size, pack.MaxPackageBytes>>20))
		}
		if err := plugin.CheckPermissions(v.Permissions); err != nil {
			return in(fmt.Errorf("%s: %w", v.Version, err))
		}
	}
	return nil
}

func (b *Block) check() error {
	switch {
	case b.ID == "" && b.Key == "":
		return errors.New("a blocklist entry names an ID or a key")
	case b.ID != "" && !plugin.ValidID(b.ID):
		return fmt.Errorf("the blocklist names %q, which is not a plugin's ID", short(b.ID))
	case b.Version != "" && (b.ID == "" || !plugin.ValidVersion(b.Version)):
		return fmt.Errorf("the blocklist's version %q is not a version of a plugin it names", short(b.Version))
	case strings.TrimSpace(b.Reason) == "":
		return errors.New("a blocklist entry says why")
	}
	if b.Key != "" {
		if _, err := pack.ParseKey(b.Key); err != nil {
			return fmt.Errorf("the blocklist: %w", err)
		}
	}
	return checkText("a blocklist entry's reason", b.Reason, maxDescription)
}

// Matches says the entry blocks a plugin of the ID, version and
// publisher key ("" for a folder in development, which has none). Keys
// are compared by their bytes, so that no spelling of a key escapes an
// entry naming it.
func (b *Block) Matches(id, version, key string) bool {
	return (b.ID == "" || b.ID == id) && (b.Version == "" || b.Version == version) && (b.Key == "" || pack.SameKey(b.Key, key))
}

// Newest is the entry's newest version for the Host API api, nil if none.
func (e *Entry) Newest(api int) *Version {
	var best *Version
	for _, v := range e.Versions {
		if v.API == api && (best == nil || plugin.CompareVersions(v.Version, best.Version) > 0) {
			best = v
		}
	}
	return best
}

// Find is the index's entry of the ID, nil if none.
func (idx *Index) Find(id string) *Entry {
	for _, e := range idx.Plugins {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// Marshal writes an index as a registry keeps it: plugins by ID, each
// one's versions newest first, indented, so that a change to one plugin
// is a small change to the file. It must read again, as loopback says.
func (idx *Index) Marshal(loopback bool) ([]byte, error) {
	slices.SortFunc(idx.Plugins, func(a, b *Entry) int { return strings.Compare(a.ID, b.ID) })
	for _, e := range idx.Plugins {
		slices.SortFunc(e.Versions, func(a, b *Version) int { return plugin.CompareVersions(b.Version, a.Version) })
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := Parse(b, loopback); err != nil {
		return nil, fmt.Errorf("the index written would not read: %w", err)
	}
	return append(b, '\n'), nil
}

// checkText refuses text over max characters or holding control or
// reordering characters, which could make it read as something else.
func checkText(what, s string, max int) error {
	if len([]rune(s)) > max {
		return fmt.Errorf("%s is over %d characters", what, max)
	}
	if strings.ContainsFunc(s, uiblock.Spoofing) {
		return fmt.Errorf("%s holds control or reordering characters", what)
	}
	return nil
}

func short(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
