package gui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/ggui"
)

// A file dropped for a session on another node goes there in pieces, and
// the session gets the copy's path; one on this machine, its own.
func TestDroppedFilesReachTheSessionsNode(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	m, _, _, _ := docApp(t, "x")
	self := ggui.Peek(m.Conn).Self.GetId()
	if !m.onThisMachine(self) || m.onThisMachine("another-node") {
		t.Fatal("which node is this machine is wrong")
	}

	// Large enough to go in three pieces.
	data := bytes.Repeat([]byte("0123456789abcdef"), (2*stageChunk+stageChunk/2)/16)
	src := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	remote, err := m.stage(self, src)
	if err != nil {
		t.Fatal(err)
	}
	if remote == src || filepath.Base(remote) != "shot.png" || !strings.Contains(remote, "sanbo-drops") {
		t.Fatalf("staged at %s", remote)
	}
	if got, err := os.ReadFile(remote); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the staged copy differs (%d bytes of %d): %v", len(got), len(data), err)
	}
	if _, err := m.stage(self, t.TempDir()); err == nil {
		t.Fatal("a folder was sent")
	}
}
