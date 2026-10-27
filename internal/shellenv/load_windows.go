package shellenv

import (
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Load takes up the PATH as the registry has it now. An app gets the PATH
// Explorer had when the user signed in, without what an installer has
// added since, such as the folder of an agent installed after.
func Load() {
	// The machine's, the user's, then the process's own, as what started it
	// may have set some of its own.
	dirs := slices.Concat(
		registryPath(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`),
		registryPath(registry.CURRENT_USER, `Environment`),
		strings.Split(os.Getenv("PATH"), ";"),
	)
	if path := joinPath(dirs); path != "" {
		_ = os.Setenv("PATH", path)
	}
}

// registryPath is the Path value of an environment key, its folders
// expanded.
func registryPath(root registry.Key, path string) []string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	v, _, err := k.GetStringValue("Path")
	if err != nil {
		return nil
	}
	if expanded, err := registry.ExpandString(v); err == nil {
		v = expanded
	}
	return strings.Split(v, ";")
}

// joinPath is dirs as a PATH, each once, case aside, in order.
func joinPath(dirs []string) string {
	var out []string
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, d) }) {
			out = append(out, d)
		}
	}
	return strings.Join(out, ";")
}
