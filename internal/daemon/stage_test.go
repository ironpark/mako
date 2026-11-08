package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestStagedFilesArriveInPieces(t *testing.T) {
	root := t.TempDir()
	first, err := stageFile(root, &protocol.StageFile{Name: "../shot.png", Data: []byte("abc")})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first.GetPath()) != "shot.png" || filepath.Dir(filepath.Dir(first.GetPath())) != root {
		t.Fatalf("staged at %s", first.GetPath())
	}
	last, err := stageFile(root, &protocol.StageFile{Path: first.GetPath(), Offset: 3, Data: []byte("def")})
	if err != nil || last.GetSize() != 6 {
		t.Fatalf("second piece: %v %v", last, err)
	}
	if b, _ := os.ReadFile(first.GetPath()); string(b) != "abcdef" {
		t.Fatalf("file %q", b)
	}
	// A piece goes only into a file staged here.
	for _, p := range []string{filepath.Join(root, "x"), "/etc/passwd", filepath.Join(root, "a", "..", "..", "x", "y")} {
		if _, err := stageFile(root, &protocol.StageFile{Path: p, Data: []byte("x")}); err == nil {
			t.Errorf("wrote into %s", p)
		}
	}
	if _, err := stageFile(root, &protocol.StageFile{Name: ""}); err == nil {
		t.Error("staged a file without a name")
	}
	// A day on, the next staged file clears the old.
	old := filepath.Dir(first.GetPath())
	past := time.Now().Add(-2 * stagedFor)
	_ = os.Chtimes(old, past, past)
	if _, err := stageFile(root, &protocol.StageFile{Name: "next.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("the old staged folder is still there")
	}
}

// Files are staged in this user's own cache, not in a folder of the
// temporary directory another user may have made first.
func TestFilesAreStagedInTheUsersCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the cache is named by LocalAppData")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	root, err := New(&config.Config{NodeID: "node"}, nil).stageRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(root, home+string(filepath.Separator)) || filepath.Base(root) != "mako-drops" {
		t.Fatalf("staged in %s", root)
	}
	staged, err := stageFile(root, &protocol.StageFile{Name: "a.txt", Data: []byte("a")})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(staged.GetPath())); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("staged folder: %v, %v", info, err)
	}
}
