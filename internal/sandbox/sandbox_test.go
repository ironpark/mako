package sandbox

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// shortTemp is a folder whose paths stay short enough for a Unix socket.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sbx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func mustMkdir(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture is a repository with a worktree, an agent home, a temporary
// folder, and a Mako folder whose run folder the session may read.
type fixture struct {
	base, repo, worktree, home, temp, mako, run, outside string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := shortTemp(t)
	f := fixture{
		base:     base,
		repo:     filepath.Join(base, "repo"),
		worktree: filepath.Join(base, "wt"),
		home:     filepath.Join(base, "home"),
		temp:     filepath.Join(base, "tmp"),
		mako:     filepath.Join(base, "mako"),
		outside:  filepath.Join(base, "outside"),
	}
	f.run = filepath.Join(f.mako, "run")
	mustMkdir(t, f.repo, f.home, f.temp, f.run, f.outside)
	writeFile(t, filepath.Join(f.mako, "peer.key"), "secret")
	writeFile(t, filepath.Join(f.run, "clip", "pbcopy"), "#!/bin/sh\n")
	git(t, f.repo, "init")
	writeFile(t, filepath.Join(f.repo, "a.txt"), "a\n")
	git(t, f.repo, "add", "a.txt")
	git(t, f.repo, "commit", "-m", "first")
	git(t, f.repo, "worktree", "add", "-b", "side", f.worktree)
	return f
}

func (f fixture) spec(root string) Spec {
	return Spec{
		Roots:    []string{root},
		Home:     f.home,
		Temp:     f.temp,
		Hidden:   []string{f.mako},
		Readable: []string{f.run},
		ReadOnly: []string{f.run},
		UserHome: filepath.Join(f.base, "user"),
	}
}

func TestProfileKeepsControlFilesOutOfReach(t *testing.T) {
	f := newFixture(t)
	spec := f.spec(f.worktree)
	spec.Sockets = []string{filepath.Join(f.run, "hooks.sock")}
	spec.Writable = []string{filepath.Join(f.run, "codex-1.sock")}
	profile, err := Profile(spec)
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(f.repo, ".git")
	admin := filepath.Join(common, "worktrees", "wt")
	for _, want := range []string{
		"(allow default)",
		"(deny file-write*)",
		`(subpath "` + f.worktree + `")`,
		`(regex #"^` + quoteRegex(f.worktree) + `/(.*/)?(\.git|\.mako|\.claude|\.codex|\.gemini|\.opencode|\.pi|\.cursor)(/.*)?$")`,
		`(regex #"^` + quoteRegex(f.worktree) + `/(.*/)?(\.mcp\.json|opencode\.json|opencode\.jsonc)$")`,
		`(regex #"^` + quoteRegex(f.worktree) + `/(\.vscode|\.zed|\.idea|\.husky|\.githooks)(/.*)?$")`,
		`(subpath "` + admin + `")`,
		`(subpath "` + common + `")`,
		`(subpath "` + filepath.Join(common, "config") + `")`,
		`(subpath "` + filepath.Join(common, "hooks") + `")`,
		`(subpath "` + filepath.Join(admin, "commondir") + `")`,
		`(subpath "` + filepath.Join(admin, "gitdir") + `")`,
		`(subpath "` + filepath.Join(common, "modules") + `")`,
		`(literal "` + common + `")`,
		`(literal "` + admin + `")`,
		`(regex #"^` + quoteRegex(common) + `/worktrees/[^/]+/(config|config\.worktree|hooks|commondir|gitdir|modules)(/.*)?$")`,
		"(deny file-read* file-write*\n  (subpath \"" + f.mako + "\"))",
		"(allow file-read*\n  (subpath \"" + f.run + "\"))",
		`(subpath "` + f.home + `")`,
		`(subpath "` + filepath.Join(f.run, "codex-1.sock") + `")`,
		"(deny network-outbound (remote unix-socket))",
		`(remote unix-socket (subpath "/private/var/run/mDNSResponder"))`,
		`(remote unix-socket (subpath "` + filepath.Join(f.run, "hooks.sock") + `"))`,
		"(deny lsopen)",
		"(deny appleevent-send)",
		"(deny job-creation)",
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile lacks %s\n%s", want, profile)
		}
	}
	// Later rules win: each narrowing comes after what it narrows.
	order := []string{
		`(subpath "` + f.worktree + `")`,
		`/(.*/)?(\.git|`,
		`(subpath "` + common + `")`,
		`(subpath "` + filepath.Join(common, "config") + `")`,
		"(deny file-read* file-write*",
		"(allow file-read* file-write*\n  (subpath \"" + f.home + "\")",
	}
	at := -1
	for _, s := range order {
		i := strings.Index(profile, s)
		if i <= at {
			t.Fatalf("%q is out of order in\n%s", s, profile)
		}
		at = i
	}
	// The system's temporary folders hold files that programs outside
	// the sandbox run code from, such as xcrun's cache.
	if strings.Contains(profile, `(subpath "/private/tmp")`) || strings.Contains(profile, `(subpath "/private/var/folders")`) {
		t.Fatalf("profile opens the system's temporary folders:\n%s", profile)
	}
	if strings.Contains(profile, `(subpath "`+filepath.Join(f.base, "user")) {
		t.Fatalf("profile opens the user's home:\n%s", profile)
	}
}

func TestCheckRefusesSpecsThatLeaveTheSandboxOpen(t *testing.T) {
	base := shortTemp(t)
	home := filepath.Join(base, "home")
	project := filepath.Join(home, "project")
	mako := filepath.Join(home, "mako")
	dotfiles := filepath.Join(home, ".config", "nvim")
	library := filepath.Join(home, "Library", "LaunchAgents")
	mustMkdir(t, project, mako, dotfiles, library)
	for name, spec := range map[string]Spec{
		"no project":           {UserHome: home},
		"home as the project":  {Roots: []string{home}, UserHome: home},
		"above home":           {Roots: []string{base}, UserHome: home},
		"root folder":          {Roots: []string{"/"}, UserHome: home},
		"inside Mako's folder": {Roots: []string{mako}, Hidden: []string{mako}, UserHome: home},
		"holds Mako's folder":  {Roots: []string{project}, Hidden: []string{filepath.Join(project, "m")}, UserHome: home},
		"relative path":        {Roots: []string{"project"}, UserHome: home},
		"quote in a path":      {Roots: []string{project}, Home: filepath.Join(base, `a"b`), UserHome: home},
		"missing project":      {Roots: []string{filepath.Join(base, "none")}, UserHome: home},
		"in a dot folder":      {Roots: []string{dotfiles}, UserHome: home},
		"in Library":           {Roots: []string{library}, UserHome: home},
	} {
		if _, err := Profile(spec); err == nil {
			t.Errorf("%s: Profile took %+v", name, spec)
		}
	}
	if _, err := Profile(Spec{Roots: []string{project}, Hidden: []string{mako}, UserHome: home}); err != nil {
		t.Fatalf("a project in the home folder: %v", err)
	}
}

func TestCommandRefusesWithoutASafeSandbox(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS has the sandbox")
	}
	_, err := Command(Spec{Roots: []string{t.TempDir()}}, []string{"true"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Command = %v, want ErrUnsupported", err)
	}
	if runtime.GOOS == "linux" && !strings.Contains(err.Error(), "bubblewrap") {
		t.Fatalf("the Linux refusal does not say why: %v", err)
	}
}

// TestHelperProcess connects to the Unix socket in SANDBOX_DIAL when the
// sandboxed tests run this test binary as their client.
func TestHelperProcess(t *testing.T) {
	path := os.Getenv("SANDBOX_DIAL")
	if path == "" {
		return
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		os.Exit(3)
	}
	_ = conn.Close()
	os.Exit(0)
}

// canSandbox skips where sandbox-exec cannot apply a profile, as inside
// another sandbox.
func canSandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("the workspace sandbox runs on macOS")
	}
	out, err := exec.Command(sandboxExec, "-p", "(version 1)(allow default)(deny file-write* (literal \"/nonexistent-mako\"))", "/usr/bin/true").CombinedOutput()
	if err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v %s", err, out)
	}
}

func run(t *testing.T, spec Spec, dir, script string, env ...string) error {
	t.Helper()
	argv, err := Command(spec, []string{"/bin/sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s: %v\n%s", script, err, out)
	}
	return err
}

func TestSandboxedSessionWritesOnlyItsWorkspace(t *testing.T) {
	canSandbox(t)
	f := newFixture(t)
	spec := f.spec(f.repo)
	commit := `git -c user.name=t -c user.email=t@example.com commit -qm `

	for _, script := range []string{
		"echo b > b.txt && git add b.txt && " + commit + "second",
		"mkdir -p node_modules/p/.vscode && echo x > node_modules/p/.vscode/settings.json",
		"echo x > " + filepath.Join(f.temp, "t"),
		"echo x > " + filepath.Join(f.home, ".claude.json.tmp") + " && mv " + filepath.Join(f.home, ".claude.json.tmp") + " " + filepath.Join(f.home, ".claude.json"),
		"cat " + filepath.Join(f.run, "clip", "pbcopy"),
	} {
		if err := run(t, spec, f.repo, script); err != nil {
			t.Errorf("sandbox refused %q", script)
		}
	}
	for _, script := range []string{
		"echo x > " + filepath.Join(f.outside, "x"),
		"echo x >> .git/config",
		"mkdir -p .git/hooks && echo x > .git/hooks/pre-commit",
		"mv .git .git-old",
		"mkdir -p sub/.git",
		"mkdir -p sub/.claude && echo '{}' > sub/.claude/settings.json",
		"echo '{}' > .mcp.json",
		"mkdir -p .mako && echo x > .mako/hooks.toml",
		"mkdir -p .vscode && echo '{}' > .vscode/tasks.json",
		"git config core.hooksPath /tmp",
		"cat " + filepath.Join(f.mako, "peer.key"),
		"echo x > " + filepath.Join(f.run, "clip", "pbcopy"),
		"ln " + filepath.Join(f.outside, "..", "mako", "peer.key") + " hl",
	} {
		if err := run(t, spec, f.repo, script); err == nil {
			t.Errorf("sandbox let %q through", script)
		}
	}
}

func TestSandboxedWorktreeCommitsButKeepsItsRepository(t *testing.T) {
	canSandbox(t)
	f := newFixture(t)
	spec := f.spec(f.worktree)
	common := filepath.Join(f.repo, ".git")
	if err := run(t, spec, f.worktree, "echo c > c.txt && git add c.txt && git -c user.name=t -c user.email=t@example.com commit -qm third"); err != nil {
		t.Fatal("a sandboxed worktree cannot commit")
	}
	for _, script := range []string{
		"echo x >> " + filepath.Join(common, "config"),
		"echo x > " + filepath.Join(common, "hooks", "post-checkout"),
		"echo /tmp > " + filepath.Join(common, "worktrees", "wt", "commondir"),
		"echo 'gitdir: /tmp/x' > .git",
		"echo x > " + filepath.Join(f.repo, "a.txt"),
		"mv " + common + " " + filepath.Join(f.repo, "old"),
		"mv " + filepath.Join(common, "worktrees", "wt") + " " + filepath.Join(common, "worktrees", "old"),
	} {
		if err := run(t, spec, f.worktree, script); err == nil {
			t.Errorf("sandbox let %q through", script)
		}
	}
}

func TestSandboxedSessionReachesOnlyItsSockets(t *testing.T) {
	canSandbox(t)
	f := newFixture(t)
	listen := func(path string) {
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
	}
	hooks := filepath.Join(f.run, "hooks.sock")
	daemon := filepath.Join(f.run, "daemon.sock")
	other := filepath.Join(f.outside, "tmux.sock")
	own := filepath.Join(f.temp, "own.sock")
	for _, p := range []string{hooks, daemon, other, own} {
		listen(p)
	}
	spec := f.spec(f.repo)
	spec.Sockets = []string{hooks}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dial := func(path string) error {
		argv, err := Command(spec, []string{exe, "-test.run=TestHelperProcess"})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "SANDBOX_DIAL="+path)
		return cmd.Run()
	}
	for _, p := range []string{hooks, own} {
		if err := dial(p); err != nil {
			t.Errorf("sandbox refused the socket %s: %v", p, err)
		}
	}
	for _, p := range []string{daemon, other} {
		if err := dial(p); err == nil {
			t.Errorf("sandbox reached the socket %s", p)
		}
	}
}
