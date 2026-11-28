// Package sandbox runs an agent session's process tree in the operating
// system's sandbox, so that what the agent runs can write to its project,
// its own agent home and temporary folders, but not to the files that would
// make code run outside the sandbox: Mako's configuration, the settings and
// hooks of agents and editors, shell start-up files, or a repository's git
// hooks and config.
//
// Only macOS has a sandbox that can say so: Seatbelt, run through
// sandbox-exec, which can deny a path by pattern wherever the agent would
// create it. Elsewhere Command refuses, rather than run a weaker sandbox.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/pathscope"
	"github.com/ironpark/mako/internal/safeexec"
)

// Spec is what one sandboxed process tree may reach. Paths are absolute.
type Spec struct {
	// Roots are the session's project folders. They are writable, but
	// for the control files in them (ProtectedDirs, ProtectedFiles), and
	// their repositories' git folders, of which only what staging and
	// committing write is.
	Roots []string
	// Home is the session's own agent home, writable as a whole: only
	// sandboxed sessions use it, so nothing written there runs outside the
	// sandbox.
	Home string
	// Temp is the session's own temporary folder, its TMPDIR, where it
	// may also connect to the sockets it makes. The system's temporary
	// folders are not writable: programs outside the sandbox keep files
	// there they run code from, such as xcrun's cache of where the
	// compilers are.
	Temp string
	// Hidden are folders it can neither read nor write, such as Mako's
	// configuration folder, which holds the node's private key.
	Hidden []string
	// Readable are paths inside Hidden it may read, such as Mako's run
	// folder, where the helpers its hooks run are.
	Readable []string
	// ReadOnly are folders it may not write even inside a writable one,
	// such as Mako's run folder when it is in the temporary folder.
	ReadOnly []string
	// Writable are more paths it may write, such as the socket its own
	// codex app-server listens on.
	Writable []string
	// Sockets are the Unix sockets it may connect to beyond those in
	// Roots, Home and Temp: Mako's hooks and agent tools. Every other
	// socket is out of reach, Mako's own control socket among them.
	Sockets []string
	// UserHome is the user's home folder, which no root may be or hold;
	// empty for os.UserHomeDir.
	UserHome string
}

// ProtectedDirs are the folders, at any depth of a project, whose files
// make code run when another program opens the project, or a folder of
// it, outside the sandbox: a repository's git folder, Mako's project
// folder, and the agents' project settings, which can name hooks, MCP
// servers and plugins.
var ProtectedDirs = []string{".git", ".mako", ".claude", ".codex", ".gemini", ".opencode", ".pi", ".cursor"}

// ProtectedFiles are the files, at any depth of a project, that do so.
var ProtectedFiles = []string{".mcp.json", "opencode.json", "opencode.jsonc"}

// ProtectedTopDirs are the folders that do so only at the top of a
// project, where editors read their tasks and git hook managers keep
// their hooks. Deeper, packages ship them, and installing one must work.
var ProtectedTopDirs = []string{".vscode", ".zed", ".idea", ".husky", ".githooks"}

// gitControl are the files and folders of a git folder that git runs or
// takes commands from: the config with its aliases, hooks path, pagers,
// filters and fsmonitor; the hooks; the files that point a worktree at
// its repository; and the submodules' own git folders.
var gitControl = []string{"config", "config.worktree", "hooks", "commondir", "gitdir", "modules"}

// sandboxExec is the program that applies a Seatbelt profile.
const sandboxExec = "/usr/bin/sandbox-exec"

// ErrUnsupported is the error Command returns where there is no sandbox
// that can keep the agent from writing control files.
var ErrUnsupported = errors.New("workspace sandbox is not supported on this system")

// Command returns argv as it runs inside the sandbox spec describes. It
// fails, rather than run argv in a weaker sandbox, where it cannot: on a
// system other than macOS, or for a spec that would leave the sandbox
// open, such as a project that is the user's home folder.
func Command(spec Spec, argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, errors.New("workspace sandbox: empty command")
	}
	if err := Supported(); err != nil {
		return nil, err
	}
	profile, err := Profile(spec)
	if err != nil {
		return nil, err
	}
	return append([]string{sandboxExec, "-p", profile, "--"}, argv...), nil
}

// Supported reports, as an error, why this system cannot run the
// workspace sandbox, nil when it can.
func Supported() error {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(sandboxExec); err != nil {
			return fmt.Errorf("%w: %s is missing", ErrUnsupported, sandboxExec)
		}
		return nil
	case "linux":
		return fmt.Errorf("%w: on Linux, bubblewrap can make read-only only the paths that already exist, so it cannot stop the agent from creating control files such as .git/hooks or .claude/settings.json in the project; turn the sandbox off for this agent or profile on this node", ErrUnsupported)
	default:
		return fmt.Errorf("%w: %s has no sandbox Mako can use", ErrUnsupported, runtime.GOOS)
	}
}

// Profile is the Seatbelt profile spec makes. Later rules win over
// earlier ones, so each layer narrows or widens the one before it.
func Profile(spec Spec) (string, error) {
	if err := check(spec); err != nil {
		return "", err
	}
	var p profile
	p.line("(version 1)")
	p.line("(allow default)")

	// Writes: none, then the devices a terminal program writes, and the
	// project.
	p.line("(deny file-write*)")
	p.rule("allow file-write*", nil, []string{
		`(literal "/dev/null")`, `(literal "/dev/zero")`, `(literal "/dev/tty")`,
		`(literal "/dev/ptmx")`, `(literal "/dev/dtracehelper")`,
		`(regex #"^/dev/ttys[0-9]+$")`, `(regex #"^/dev/fd/[0-9]+$")`,
	})
	p.rule("allow file-write*", subpaths(spec.Roots...), nil)

	// The project's control files, wherever in it they are.
	var controls []string
	for _, root := range spec.Roots {
		for _, r := range forms(root) {
			controls = append(controls,
				`(regex #"^`+quoteRegex(r)+`/(.*/)?(`+alternatives(ProtectedDirs)+`)(/.*)?$")`,
				`(regex #"^`+quoteRegex(r)+`/(.*/)?(`+alternatives(ProtectedFiles)+`)$")`,
				`(regex #"^`+quoteRegex(r)+`/(`+alternatives(ProtectedTopDirs)+`)(/.*)?$")`)
		}
	}
	p.rule("deny file-write*", nil, controls)

	// What git writes to stage and commit, in the project's repositories,
	// wherever their git folders are, but none of the files git runs.
	var gitDirs, gitDeny []string
	var gitRegex []string
	for _, root := range spec.Roots {
		repo := findRepo(root)
		for _, dir := range repo.dirs() {
			if !slices.Contains(gitDirs, dir) {
				gitDirs = append(gitDirs, dir)
			}
		}
		if repo.hooks != "" {
			gitDeny = append(gitDeny, repo.hooks)
		}
	}
	for _, dir := range gitDirs {
		for _, name := range gitControl {
			gitDeny = append(gitDeny, filepath.Join(dir, name))
		}
		for _, d := range forms(dir) {
			// The folder itself, which moved away would make room for
			// another with a config of the agent's.
			gitRegex = append(gitRegex, `(literal "`+d+`")`,
				`(regex #"^`+quoteRegex(d)+`/worktrees/[^/]+/(`+alternatives(gitControl)+`)(/.*)?$")`)
		}
	}
	p.rule("allow file-write*", subpaths(gitDirs...), nil)
	p.rule("deny file-write*", subpaths(gitDeny...), gitRegex)

	// Mako's own folders: never written, and the hidden ones not read but
	// for what the session runs from them.
	p.rule("deny file-read* file-write*", subpaths(spec.Hidden...), nil)
	p.rule("deny file-write*", subpaths(spec.ReadOnly...), nil)
	p.rule("allow file-read*", subpaths(spec.Readable...), nil)

	// The session's own folders.
	own := slices.DeleteFunc([]string{spec.Home, spec.Temp}, func(s string) bool { return s == "" })
	p.rule("allow file-read* file-write*", subpaths(append(own, spec.Writable...)...), nil)

	// Sockets: those the session needs, and those it makes itself.
	p.line("(deny network-outbound (remote unix-socket))")
	sockets := append([]string{"/private/var/run/mDNSResponder"}, spec.Sockets...)
	sockets = append(sockets, own...)
	sockets = append(sockets, spec.Roots...)
	p.rule("allow network-outbound", nil, prefixed("remote unix-socket ", subpaths(sockets...)))

	// The ways one program asks another, outside the sandbox, to run
	// something: opening a file or app, an Apple event, a launchd job.
	p.line("(deny lsopen)")
	p.line("(deny appleevent-send)")
	p.line("(deny job-creation)")
	return p.String(), nil
}

// check refuses a spec that would leave the sandbox open, or that a
// profile cannot say.
func check(spec Spec) error {
	if len(spec.Roots) == 0 {
		return errors.New("workspace sandbox: no project folder")
	}
	home := spec.UserHome
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	all := slices.Concat(spec.Roots, spec.Hidden, spec.Readable, spec.ReadOnly, spec.Writable, spec.Sockets, []string{spec.Home, spec.Temp, home})
	for _, path := range all {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("workspace sandbox: %q is not an absolute path", path)
		}
		if strings.ContainsAny(path, "\"\\\x00\n\r") {
			return fmt.Errorf("workspace sandbox: %q has a character a sandbox profile cannot hold", path)
		}
	}
	for _, root := range spec.Roots {
		info, err := os.Stat(root)
		if err != nil {
			return fmt.Errorf("workspace sandbox: project folder: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace sandbox: project folder %s is not a folder", root)
		}
		for _, r := range forms(root) {
			if r == "/" {
				return errors.New("workspace sandbox: the project folder cannot be the root folder")
			}
			if home != "" && slices.ContainsFunc(forms(home), func(h string) bool { return pathscope.Within(h, r) }) {
				return fmt.Errorf("workspace sandbox: the project folder %s is or holds your home folder, whose settings and start-up files the sandbox must keep; open a project folder instead", root)
			}
			if dir := settingsFolder(home, r); dir != "" {
				return fmt.Errorf("workspace sandbox: the project folder %s is in %s, where programs outside the sandbox keep settings they run code from; open a project folder instead", root, dir)
			}
			for _, hidden := range spec.Hidden {
				for _, h := range forms(hidden) {
					if pathscope.Within(h, r) || pathscope.Within(r, h) {
						return fmt.Errorf("workspace sandbox: the project folder %s overlaps Mako's folder %s", root, hidden)
					}
				}
			}
		}
	}
	return nil
}

// settingsFolder is the folder of home that path is in where programs
// keep their settings, a dot folder such as ~/.config or ~/Library on
// macOS; "" for none.
func settingsFolder(home, path string) string {
	if home == "" {
		return ""
	}
	for _, h := range forms(home) {
		rel, err := filepath.Rel(h, path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		if strings.HasPrefix(top, ".") || top == "Library" {
			return filepath.Join(h, top)
		}
	}
	return ""
}

// repo is the git repository that holds a project folder.
type repo struct {
	gitDir, commonDir string
	// hooks is the folder git runs hooks from, its core.hooksPath.
	hooks string
}

func (r repo) dirs() []string {
	var out []string
	for _, d := range []string{r.gitDir, r.commonDir} {
		if d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// findRepo asks git where the repository holding dir keeps its git
// folder, the folder its worktrees share, and its hooks: a worktree's
// .git is a file naming a folder in its main repository's. It is the zero
// repo outside one, or without git.
func findRepo(dir string) repo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--git-path", "hooks")
	// The daemon's own environment must not point git at another
	// repository.
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY"}, name)
	})
	out, err := safeexec.Output(cmd)
	if err != nil {
		return repo{}
	}
	lines := strings.Split(strings.TrimSpace(string(bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n")))), "\n")
	if len(lines) != 3 {
		return repo{}
	}
	abs := func(p string) string {
		p = strings.TrimSpace(p)
		if p == "" {
			return ""
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return filepath.Clean(p)
	}
	return repo{gitDir: abs(lines[0]), commonDir: abs(lines[1]), hooks: abs(lines[2])}
}

// profile builds a Seatbelt profile a line at a time.
type profile struct{ b strings.Builder }

func (p *profile) line(s string) { p.b.WriteString(s + "\n") }

// rule writes (verb filters...) for paths, each as it is and as its
// symbolic links resolve, as Seatbelt matches the resolved path; it
// writes nothing without a filter.
func (p *profile) rule(verb string, paths, filters []string) {
	all := append(slices.Clone(paths), filters...)
	if len(all) == 0 {
		return
	}
	p.b.WriteString("(" + verb)
	for _, f := range all {
		p.b.WriteString("\n  " + f)
	}
	p.b.WriteString(")\n")
}

func (p *profile) String() string { return p.b.String() }

// subpaths are (subpath ...) filters for paths, in both their forms.
func subpaths(paths ...string) []string {
	var out []string
	for _, path := range paths {
		if path == "" {
			continue
		}
		for _, f := range forms(path) {
			filter := `(subpath "` + f + `")`
			if !slices.Contains(out, filter) {
				out = append(out, filter)
			}
		}
	}
	return out
}

func prefixed(prefix string, filters []string) []string {
	out := make([]string, len(filters))
	for i, f := range filters {
		out[i] = "(" + prefix + f + ")"
	}
	return out
}

// forms are path cleaned and, where it differs, with its symbolic links
// resolved: /tmp is /private/tmp on macOS.
func forms(path string) []string {
	clean := filepath.Clean(path)
	out := []string{clean}
	if real, err := filepath.EvalSymlinks(clean); err == nil && real != clean {
		out = append(out, real)
	} else if err != nil {
		// A path not made yet resolves through the folder that holds it.
		if parent, err := filepath.EvalSymlinks(filepath.Dir(clean)); err == nil {
			if real := filepath.Join(parent, filepath.Base(clean)); real != clean {
				out = append(out, real)
			}
		}
	}
	return out
}

// quoteRegex escapes what a Seatbelt regex reads as an operator.
func quoteRegex(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.^$|()[]{}*+?`, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func alternatives(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quoteRegex(n)
	}
	return strings.Join(quoted, "|")
}
