package zed

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// API is Zed's extension store.
const API = "https://api.zed.dev"

// The extension API versions mako takes extensions for. Their grammars
// and languages read the same in every version; their wasm is run only
// for their context servers (internal/zedhost).
const (
	minSchema  = 0
	maxSchema  = 1
	maxWasmAPI = "0.7.0"
)

// maxArchive caps a downloaded extension, unpacked.
const maxArchive = 256 << 20

// Store is the extensions installed in a directory, each in a
// subdirectory named by its ID, and Zed's store to find more in.
type Store struct {
	Dir    string
	API    string       // empty is API
	Client *http.Client // nil is http.DefaultClient
}

// Listing is an extension as the store lists it.
type Listing struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Description   string    `json:"description"`
	Authors       []string  `json:"authors"`
	Repository    string    `json:"repository"`
	Provides      []string  `json:"provides"`
	PublishedAt   time.Time `json:"published_at"`
	DownloadCount int       `json:"download_count"`
}

// Extension is an installed extension.
type Extension struct {
	Manifest
	Dir string
}

func (s *Store) api() string {
	if s.API != "" {
		return strings.TrimSuffix(s.API, "/")
	}
	return API
}

func (s *Store) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

// Search lists the store's extensions that provide languages or context
// servers (MCP servers) and match query, the most downloaded first, as
// Zed's own list does. The store filters by one kind at a time, so it is
// asked for each.
func (s *Store) Search(ctx context.Context, query string) ([]Listing, error) {
	var out []Listing
	seen := map[string]bool{}
	for _, kind := range []string{"languages", "context-servers"} {
		found, err := s.search(ctx, query, kind)
		if err != nil {
			return nil, err
		}
		for _, l := range found {
			if !seen[l.ID] {
				seen[l.ID] = true
				out = append(out, l)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Listing) int { return b.DownloadCount - a.DownloadCount })
	return out, nil
}

func (s *Store) search(ctx context.Context, query, provides string) ([]Listing, error) {
	q := url.Values{
		"filter":             {query},
		"max_schema_version": {fmt.Sprint(maxSchema)},
		"provides":           {provides},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api()+"/extensions?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zed: the store answered %s", resp.Status)
	}
	var body struct {
		Data []Listing `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("zed: the store's list: %w", err)
	}
	return body.Data, nil
}

// validID is what an extension ID may be, as a directory name too.
var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Install downloads an extension from the store and puts it in place of
// any version installed, which stays until the new one is whole.
func (s *Store) Install(ctx context.Context, id string) (Extension, error) {
	if !validID.MatchString(id) {
		return Extension{}, fmt.Errorf("zed: %q is not an extension ID", id)
	}
	q := url.Values{
		"min_schema_version":   {fmt.Sprint(minSchema)},
		"max_schema_version":   {fmt.Sprint(maxSchema)},
		"min_wasm_api_version": {"0.0.0"},
		"max_wasm_api_version": {maxWasmAPI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api()+"/extensions/"+id+"/download?"+q.Encode(), nil)
	if err != nil {
		return Extension{}, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return Extension{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Extension{}, fmt.Errorf("zed: downloading %s: %s", id, resp.Status)
	}
	return s.installArchive(id, resp.Body)
}

// installArchive unpacks a gzipped tar of an extension as id.
func (s *Store) installArchive(id string, r io.Reader) (Extension, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Extension{}, err
	}
	tmp, err := os.MkdirTemp(s.Dir, ".install-"+id+"-")
	if err != nil {
		return Extension{}, err
	}
	defer os.RemoveAll(tmp)
	if err := untar(r, tmp); err != nil {
		return Extension{}, fmt.Errorf("zed: unpacking %s: %w", id, err)
	}
	m, err := readManifest(tmp)
	if err != nil {
		return Extension{}, err
	}
	if m.ID != id {
		return Extension{}, fmt.Errorf("zed: asked for %s, the store sent %s", id, m.ID)
	}
	dst := filepath.Join(s.Dir, id)
	old := dst + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Extension{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(old, dst)
		return Extension{}, err
	}
	_ = os.RemoveAll(old)
	return Extension{Manifest: m, Dir: dst}, nil
}

// untar unpacks a gzipped tar into dir, refusing anything that would land
// outside it and anything but files and directories.
func untar(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(strings.TrimPrefix(h.Name, "./"))
		if name == "" || name == "." {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%q is outside the extension", h.Name)
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxArchive {
				return errors.New("the extension is too large")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}

// Uninstall removes an installed extension.
func (s *Store) Uninstall(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("zed: %q is not an extension ID", id)
	}
	return os.RemoveAll(filepath.Join(s.Dir, id))
}

// Installed lists the extensions installed, by name. One that cannot be
// read is left out.
func (s *Store) Installed() ([]Extension, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Extension
	for _, e := range entries {
		if !e.IsDir() || !validID.MatchString(e.Name()) {
			continue // the leftovers of an install, among others
		}
		dir := filepath.Join(s.Dir, e.Name())
		m, err := readManifest(dir)
		if err != nil {
			continue
		}
		out = append(out, Extension{Manifest: m, Dir: dir})
	}
	slices.SortFunc(out, func(a, b Extension) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}
