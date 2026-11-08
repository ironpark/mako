package claude

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/atomicfile"
)

// mod is mako-live, the mod that streams a reply to mako as Claude Code
// writes it: each piece of its text, and each tool call begun, of the
// main conversation, as its turn.step event hands them on.
//
//go:embed all:mod
var mod embed.FS

// modName is the mod's folder, under the directory it is written to.
const modName = "mako-live"

func (*Agent) LiveMod(dir string) (string, error) {
	folder := filepath.Join(dir, modName)
	err := fs.WalkDir(mod, "mod", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("mod", path)
		to := filepath.Join(folder, rel)
		if e.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		data, err := mod.ReadFile(path)
		if err != nil {
			return err
		}
		if old, err := os.ReadFile(to); err == nil && string(old) == string(data) {
			return nil // as written: a session loading it reads it whole
		}
		return atomicfile.Write(to, data)
	})
	return folder, err
}

// LiveCheck validates the mod: a version that loads mods says the event
// it hooks.
func (*Agent) LiveCheck(folder string) ([]string, string) {
	return []string{"plugin", "validate", folder}, "turn.step"
}

func (*Agent) WithLive(l *agent.Launch, folder string) {
	l.Command = append(l.Command, "--plugin-dir", folder)
}
