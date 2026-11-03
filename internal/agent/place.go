package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

// ValidID matches a conversation ID mako takes from a log, a hook or a
// command line: one it can put in a file name or a glob.
var ValidID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// Place is where an agent keeps its logs: under RootEnv when it is set,
// otherwise HomeDir under HOME, in the directories Dirs matches there.
type Place struct {
	RootEnv, HomeDir string
	Dirs             []string
	// ProfileHome is for an agent that, on Windows, keeps HomeDir in the
	// user's profile whatever HOME says, as a native program does; one
	// on Node, as Claude Code, follows HOME.
	ProfileHome bool
}

// Root is the directory the agent keeps its settings and logs under,
// given env.
func (p Place) Root(env map[string]string) string {
	if root := env[p.RootEnv]; root != "" {
		return root
	}
	home := env["HOME"]
	if home == "" || p.ProfileHome && runtime.GOOS == "windows" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, p.HomeDir)
}

// Pattern is the glob of the logs under root whose file name matches name.
func (p Place) Pattern(root, name string) string {
	return filepath.Join(append(append([]string{root}, p.Dirs...), name)...)
}
