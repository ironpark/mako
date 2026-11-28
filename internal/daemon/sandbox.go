package daemon

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/sandbox"
)

// The workspace sandbox (see internal/sandbox) runs a session's agent,
// its codex app-server, and its one-shot prompts so that they write only
// to the project, the agent's sandbox home and their own temporary folder.
// Mako's configuration folder, which holds the node's key and every
// agent's settings, is hidden from them but for the helpers their hooks
// and MCP servers run, and Mako's sockets but its hooks and agent tools
// are out of reach.

// configDir is the daemon's configuration folder, which Run reads from
// even when New left d.dir empty.
func (d *Daemon) configDir() (string, error) {
	if dir := d.syncDir(); dir != "" {
		return dir, nil
	}
	return config.Dir()
}

// configuredSandbox is the sandbox agent name's profile (or its own
// configuration, for "") runs sessions in.
func (d *Daemon) configuredSandbox(name, profile string) (string, error) {
	a, ok := agentspec.Lookup(name, d.agents())
	if !ok {
		return "", nil
	}
	if profile == "" {
		return a.EffectiveSandbox()
	}
	p, ok := a.Profile(profile)
	if !ok {
		return "", nil
	}
	return p.EffectiveSandbox(a)
}

// sandboxedLocked reports whether session id runs, or ran, in the
// sandbox. The caller holds sessionMu.
func (d *Daemon) sandboxedLocked(id string) bool {
	return id != "" && d.saved[id].Sandbox != ""
}

// sandboxed is sandboxedLocked for a caller without sessionMu.
func (d *Daemon) sandboxed(id string) bool {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	return d.sandboxedLocked(id)
}

// newSandbox is the sandbox a run named id, of agent agentName in the
// project folders roots, works in, with home its agent home ("" for none).
// It makes the run's temporary folder, gives l it and the caches, and
// returns what removes it.
func (d *Daemon) newSandbox(id, agentName string, roots []string, home string, l *agent.Launch) (*sandbox.Spec, func(), error) {
	if err := sandbox.Supported(); err != nil {
		return nil, nil, err
	}
	makoDir, err := d.configDir()
	if err != nil {
		return nil, nil, fmt.Errorf("workspace sandbox: %w", err)
	}
	run, err := config.RunDir(d.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("workspace sandbox: %w", err)
	}
	temp := filepath.Join(os.TempDir(), "mako-sandbox-"+id)
	_ = os.RemoveAll(temp)
	if err := os.MkdirAll(temp, 0o700); err != nil {
		return nil, nil, fmt.Errorf("workspace sandbox: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(temp) }
	// The system's temporary folders are out of reach: programs that
	// would write there by default write here.
	l.SetEnv("TMPDIR", temp)
	l.SetEnv("TMPPREFIX", filepath.Join(temp, "zsh"))
	l.SetEnv("CLAUDE_CODE_TMPDIR", temp)
	// Caches under the user's home are shared with what runs outside the
	// sandbox, which a poisoned build cache would run code in.
	cache := filepath.Join(cmp.Or(home, temp), ".cache")
	for name, value := range map[string]string{"XDG_CACHE_HOME": cache, "GOCACHE": filepath.Join(cache, "go-build")} {
		if l.Env[name] == "" {
			l.SetEnv(name, value)
		}
	}

	hidden := []string{makoDir}
	if !pathscope.Within(run, makoDir) {
		hidden = append(hidden, run)
	}
	// What the session runs from Mako's folders: the helpers its hooks
	// and tools run (mako-msg, mako-plugin, the clipboard tools), Claude's
	// live-replies mod, its own MCP configuration, and the MCP servers of
	// the Zed extensions installed.
	readable := []string{
		filepath.Join(run, "clip"),
		filepath.Join(run, "mako-live"),
		filepath.Join(run, "mcp-"+id+".json"),
		filepath.Join(run, "pi-mcp-"+id+".js"),
		filepath.Join(makoDir, "extensions"),
		filepath.Join(makoDir, "extension-work"),
	}
	opencode, _ := filepath.Glob(filepath.Join(run, "opencode-mako-*.js"))
	readable = append(readable, opencode...)
	if agentName == "agy" {
		readable = append(readable, filepath.Join(run, sidecarTokenFile))
	}
	sockets := []string{
		filepath.Join(run, "hooks.sock"),
		filepath.Join(run, agentCLISock),
		filepath.Join(run, "agents.sock"),
		filepath.Join(run, "live.sock"),
	}
	if sock := agent.LookupEnv(l.Env, "SSH_AUTH_SOCK"); filepath.IsAbs(sock) {
		sockets = append(sockets, sock)
	}
	spec := &sandbox.Spec{
		Roots:    roots,
		Home:     home,
		Temp:     temp,
		Hidden:   hidden,
		Readable: readable,
		ReadOnly: []string{run},
		Sockets:  sockets,
	}
	return spec, cleanup, nil
}

// sandboxRoots are the folders a session in project, with its project's
// other folders dirs, may write to: the project, and those of the others
// that are there.
func sandboxRoots(project string, dirs []string) []string {
	roots := []string{project}
	for _, dir := range dirs {
		if dirExists(dir) && !slices.Contains(roots, dir) {
			roots = append(roots, dir)
		}
	}
	return roots
}

// withCodexSocket lets session id's codex app-server, in box, listen on
// its socket, and its TUI connect to it.
func (d *Daemon) withCodexSocket(box *sandbox.Spec, id string) {
	run, err := config.RunDir(d.dir)
	if err != nil {
		return
	}
	if sock := codexSocketPath(run, id); sock != "" {
		box.Writable = append(box.Writable, sock)
		box.Sockets = append(box.Sockets, sock)
	}
}

// isNative reports whether program runs agent name's own CLI.
func isNative(name, program string) bool {
	_, ok := agents.Native(name, program)
	return ok
}

// withSandboxHome gives a sandboxed launch of agentName its sandbox home,
// and an unsandboxed one the isolated home when the node isolates them.
func (d *Daemon) withSandboxHome(agentName string, launch *agentspec.Launch) (string, error) {
	if launch.Sandbox == "" {
		if d.isolateHome() {
			return "", agentspec.ApplyIsolatedHome(agentName, launch)
		}
		return "", nil
	}
	return agentspec.ApplySandboxHome(agentName, launch)
}

// errSandboxRemote is why a sandboxed session cannot start one elsewhere.
var errSandboxRemote = errors.New("a sandboxed session cannot start sessions on other nodes, which would run outside its sandbox")
