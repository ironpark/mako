package agent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A file written once is written again when what is there is not it, as
// one cut short is; launches writing it at once leave it whole, and
// nothing of their own beside it.
func TestWriteOnceLeavesTheFileWhole(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteOnce(dir, "hooks", ".js", "whole")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("wh"), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := WriteOnce(dir, "hooks", ".js", "whole"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got, _ := os.ReadFile(path); string(got) != "whole" {
		t.Errorf("the file holds %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 1 {
		t.Errorf("files left %v", left)
	}
}
