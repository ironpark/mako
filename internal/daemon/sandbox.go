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

// sandboxSpec is the sandbox a run named id, of agent agentName in mode,
// in the project folders roots, works in, with home its agent home ("" for
// none), but for its temporary folder, which newSandbox makes. It makes
// nothing else, so that a spec the sandbox would refuse is refused before
// a home or a worktree is made for it.
func (d *Daemon) sandboxSpec(id, agentName, mode string, roots []string, home string) (*sandbox.Spec, error) {
	makoDir, err := d.configDir()
	if err != nil {
		return nil, fmt.Errorf("workspace sandbox: %w", err)
	}
	run, err := config.RunDir(makoDir)
	if err != nil {
		return nil, fmt.Errorf("workspace sandbox: %w", err)
	}
	userHome, _ := os.UserHomeDir()

	// Mako's folders, and what of the user's an agent outside the sandbox
	// runs code from or signs in with: the agents' isolated homes, which
	// hold their settings and sign-ins, the other agents' sandbox homes,
	// and the user's SSH keys.
	hidden := []string{makoDir}
	if !pathscope.Within(run, makoDir) {
		hidden = append(hidden, run)
	}
	for _, base := range []func() (string, error){agentspec.IsolatedHomeBase, agentspec.SandboxHomeBase} {
		if dir, err := base(); err == nil && !pathscope.Within(dir, makoDir) {
			hidden = append(hidden, dir)
		}
	}
	if userHome != "" {
		hidden = append(hidden, filepath.Join(userHome, ".ssh"))
	}
	// What the session runs from Mako's folders: the helpers its hooks
	// and tools run (mako-msg, mako-plugin, the clipboard tools), Claude's
	// live-replies mod, its own MCP configuration and hook token, the hook
	// extensions of
	// pi, omp, omo and OpenCode, and the MCP servers of the Zed extensions
	// installed. Not the agy sidecar's token, with which a sandboxed
	// sidecar would take the messages of every agy session.
	readable := []string{
		filepath.Join(run, "clip"),
		filepath.Join(run, "mako-live"),
		filepath.Join(run, "mcp-"+id+".json"),
		filepath.Join(run, "pi-mcp-"+id+".js"),
		filepath.Join(makoDir, "extensions"),
		filepath.Join(makoDir, "extension-work"),
		// Its own hook token, with which its hooks, status line and
		// clipboard tools, and those its codex app-server runs, report
		// to the hook socket for it; not another session's.
		hookTokenFile(cmp.Or(d.hookRun, run), id),
	}
	for _, pattern := range []string{"opencode-mako-*.js", "*-hooks-*.js"} {
		matches, _ := filepath.Glob(filepath.Join(run, pattern))
		readable = append(readable, matches...)
	}
	sockets := []string{
		filepath.Join(run, "hooks.sock"),
		filepath.Join(run, agentCLISock),
		filepath.Join(run, "agents.sock"),
		filepath.Join(run, "live.sock"),
	}
	// The SSH agent signs in to hosts and remotes, where what the session
	// runs is outside the sandbox: only a sandbox that gives it has it,
	// with what ssh reads of the user's to know the hosts.
	if mode == agentspec.SandboxWorkspaceSSH {
		if sock := os.Getenv("SSH_AUTH_SOCK"); filepath.IsAbs(sock) {
			sockets = append(sockets, sock)
		}
		if userHome != "" {
			for _, name := range []string{"known_hosts", "known_hosts2", "config", "config.d"} {
				readable = append(readable, filepath.Join(userHome, ".ssh", name))
			}
		}
	}
	return &sandbox.Spec{
		Roots:    roots,
		Home:     home,
		Hidden:   hidden,
		Readable: readable,
		ReadOnly: []string{run},
		Sockets:  sockets,
	}, nil
}

// checkSandbox refuses, before anything is made for it, a session of
// agentName in mode in the project folders roots that the sandbox would
// refuse.
func (d *Daemon) checkSandbox(id, agentName, mode string, roots []string) error {
	if err := sandbox.Supported(); err != nil {
		return err
	}
	_, home, err := agentspec.SandboxHomeEnv(agentName)
	if err != nil {
		return fmt.Errorf("workspace sandbox: %w", err)
	}
	spec, err := d.sandboxSpec(id, agentName, mode, roots, home)
	if err != nil {
		return err
	}
	return sandbox.Check(*spec)
}

// newSandbox is sandboxSpec with the run's temporary folder, which it
// makes and gives l with the caches, and returns what removes it.
func (d *Daemon) newSandbox(id, agentName, mode string, roots []string, home string, l *agent.Launch) (*sandbox.Spec, func(), error) {
	if err := sandbox.Supported(); err != nil {
		return nil, nil, err
	}
	spec, err := d.sandboxSpec(id, agentName, mode, roots, home)
	if err != nil {
		return nil, nil, err
	}
	temp := filepath.Join(os.TempDir(), "mako-sandbox-"+id)
	_ = os.RemoveAll(temp)
	if err := os.MkdirAll(temp, 0o700); err != nil {
		return nil, nil, fmt.Errorf("workspace sandbox: %w", err)
	}
	spec.Temp = temp
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
	if mode != agentspec.SandboxWorkspaceSSH {
		// Out of reach either way; empty, ssh and git say so at once.
		l.SetEnv("SSH_AUTH_SOCK", "")
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
	makoDir, err := d.configDir()
	if err != nil {
		return
	}
	run, err := config.RunDir(makoDir)
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

// errSandboxPlugins is why a sandboxed session cannot call plugin tools.
var errSandboxPlugins = errors.New("a sandboxed session cannot call plugin tools, which run in the daemon, outside its sandbox")

// errSandboxReach is why a sandboxed session cannot reach another.
var errSandboxReach = errors.New("a sandboxed session can reach only sessions in the sandbox, and send messages to its parent: an agent outside it would read or act on what it is told outside the sandbox")

// sandboxReach refuses session from, when it runs in the sandbox, a
// message to (message) or a read of session to outside it: an agent
// outside the sandbox does what it is told there, and its conversation
// and terminal hold what the sandbox keeps from the session. A session
// may message its parent, which started it to report back, and any
// session in the sandbox; a session of another node is outside it.
func (d *Daemon) sandboxReach(from, to string, message bool) error {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	src := d.saved[from]
	switch {
	case src.Sandbox == "":
		return nil
	case message && to != "" && to == src.ParentID:
		return nil
	case d.sandboxedLocked(to):
		return nil
	}
	return errSandboxReach
}
