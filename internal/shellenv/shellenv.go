// Package shellenv gives an app the desktop started the environment of
// the user's login shell. Launched from the Finder or a desktop's menu, a
// program gets the session's bare environment, without the PATH and the
// rest the user's shell profile sets up, so the agents and tools it runs
// would not be found.
package shellenv

import (
	"bytes"
	"strings"
)

// marker fences the environment in the shell's output, apart from what
// the profile prints as it starts.
const marker = "__MAKO_SHELL_ENV__"

// skip are the variables that describe the probing shell, not the user's
// environment.
var skip = map[string]bool{"_": true, "SHLVL": true, "PWD": true, "OLDPWD": true}

// parse reads the variables between the markers of out, as env -0 wrote
// them.
func parse(out []byte) map[string]string {
	_, rest, ok := bytes.Cut(out, []byte(marker))
	if !ok {
		return nil
	}
	body, _, ok := bytes.Cut(rest, []byte(marker))
	if !ok {
		return nil
	}
	vars := map[string]string{}
	for _, kv := range strings.Split(string(body), "\x00") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || skip[k] {
			continue
		}
		vars[k] = v
	}
	return vars
}
