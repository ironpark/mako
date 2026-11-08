package zedhost

import (
	"archive/tar"
	"archive/zip"
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
	"strings"
)

// maxDownload caps what download-file writes, unpacked.
const maxDownload = 1 << 30

// download fetches url to dst as kind says: "uncompressed" and "gzip" are
// a file, "gzip-tar" and "zip" a directory unpacked into.
func download(ctx context.Context, rawURL, dst, kind string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered %s", resp.Status)
	}
	body := io.LimitReader(resp.Body, maxDownload)
	switch kind {
	case "uncompressed":
		return writeFile(dst, body, 0o644)
	case "gzip":
		gz, err := gzip.NewReader(body)
		if err != nil {
			return err
		}
		defer gz.Close()
		return writeFile(dst, gz, 0o644)
	case "gzip-tar":
		gz, err := gzip.NewReader(body)
		if err != nil {
			return err
		}
		defer gz.Close()
		return untar(gz, dst)
	case "zip":
		// A zip is read from its end, so it lands in a file first.
		tmp, err := os.CreateTemp(filepath.Dir(dst), ".download-*.zip")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		n, err := io.Copy(tmp, body)
		if err != nil {
			return err
		}
		return unzip(tmp, n, dst)
	}
	return fmt.Errorf("unknown file type %q", kind)
}

func writeFile(dst string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(r, maxDownload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// local is an archive entry's name as a path inside the archive, "." for
// its top, refusing one that would land outside it.
func local(name string) (string, error) {
	name = filepath.FromSlash(strings.TrimPrefix(name, "./"))
	if name == "" || name == "." {
		return ".", nil
	}
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("%q is outside the archive", name)
	}
	return name, nil
}

// openRoot makes dir if it is missing and opens it as the root an archive
// unpacks into. Every path is resolved within it, links and all, so an
// entry reached through a link the archive made earlier cannot land
// outside dir, as a check of the names alone lets it.
func openRoot(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

// writeIn writes a file at name within root, making its directory.
func writeIn(root *os.Root, name string, r io.Reader, mode os.FileMode) error {
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(r, maxDownload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// untar unpacks files, directories and links that stay inside dir,
// keeping files' permissions, so a binary stays executable.
func untar(r io.Reader, dir string) error {
	root, err := openRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := local(h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0o755)
		case tar.TypeReg:
			err = writeIn(root, name, tr, os.FileMode(h.Mode).Perm()|0o600)
		case tar.TypeSymlink:
			link := filepath.FromSlash(h.Linkname)
			if filepath.IsAbs(link) || !filepath.IsLocal(filepath.Join(filepath.Dir(name), link)) {
				continue // a link out of the archive is left out
			}
			_ = root.MkdirAll(filepath.Dir(name), 0o755)
			_ = root.Remove(name)
			err = root.Symlink(h.Linkname, name)
		}
		if err != nil {
			return err
		}
	}
}

func unzip(f io.ReaderAt, size int64, dir string) error {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	root, err := openRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, zf := range zr.File {
		name, err := local(zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		}
		if !zf.Mode().IsRegular() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = writeIn(root, name, rc, zf.Mode().Perm()|0o600)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// GitHub -----------------------------------------------------------------

type ghRelease struct {
	TagName    string `json:"tag_name"`
	PreRelease bool   `json:"prerelease"`
	Assets     []struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
		Digest      string `json:"digest"`
	} `json:"assets"`
}

// gitHub GETs a path of GitHub's API into v, with GITHUB_TOKEN if set, as
// the anonymous rate limit is low.
func gitHub(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mako")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %s for %s", resp.Status, path)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

// releaseVal is a release as github-release: the API since 0.8.0 adds
// each asset's digest.
func (e *Extension) releaseVal(r ghRelease) Val {
	assets := make([]Val, len(r.Assets))
	for i, a := range r.Assets {
		fields := []Field{{"name", String(a.Name)}, {"download-url", String(a.DownloadURL)}}
		if e.api.atLeast(0, 8, 0) {
			digest := None()
			if a.Digest != "" {
				digest = Some(String(a.Digest))
			}
			fields = append(fields, Field{"digest", digest})
		}
		assets[i] = Record(fields...)
	}
	return Record(Field{"version", String(r.TagName)}, Field{"assets", List(assets...)})
}

func validRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	return ok && owner != "" && name != "" && !strings.ContainsAny(name, "/?#") && url.PathEscape(owner) == owner
}

func (e *Extension) latestGitHubRelease(args []Val) ([]Val, error) {
	repo := arg(args, 0).Str
	opts := arg(args, 1)
	requireAssets, _ := field(opts, "require-assets")
	pre, _ := field(opts, "pre-release")
	if !validRepo(repo) {
		return []Val{Errf("%q is not a GitHub repository", repo)}, nil
	}
	var releases []ghRelease
	if err := gitHub(e.context(), "/repos/"+repo+"/releases?per_page=100", &releases); err != nil {
		return []Val{Err(err.Error())}, nil
	}
	for _, r := range releases {
		if (!requireAssets.Bool || len(r.Assets) > 0) && r.PreRelease == pre.Bool {
			return []Val{Ok(e.releaseVal(r))}, nil
		}
	}
	return []Val{Errf("no release found in %s", repo)}, nil
}

func (e *Extension) gitHubReleaseByTag(args []Val) ([]Val, error) {
	repo, tag := arg(args, 0).Str, arg(args, 1).Str
	if !validRepo(repo) {
		return []Val{Errf("%q is not a GitHub repository", repo)}, nil
	}
	var r ghRelease
	if err := gitHub(e.context(), "/repos/"+repo+"/releases/tags/"+url.PathEscape(tag), &r); err != nil {
		return []Val{Err(err.Error())}, nil
	}
	return []Val{Ok(e.releaseVal(r))}, nil
}
