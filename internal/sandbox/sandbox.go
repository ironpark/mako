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
	"io"
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
// folder, the agents' project settings, which can name hooks, MCP
// servers and plugins, and direnv's folder, which .envrc layouts source.
var ProtectedDirs = []string{".git", ".mako", ".claude", ".codex", ".gemini", ".opencode", ".pi", ".cursor", ".direnv"}

// ProtectedFiles are the files, at any depth of a project, that do so:
// the agents' MCP servers, direnv's .envrc, which the shell runs on cd,
// npm's .npmrc and Cargo's config, which name the programs they run, and
// .gitmodules, whose submodules git clones and checks out.
var ProtectedFiles = []string{".mcp.json", "opencode.json", "opencode.jsonc", ".envrc", ".npmrc", ".cargo/config.toml", ".cargo/config", ".gitmodules"}

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
	repos, err := check(spec)
	if err != nil {
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
	p.rule("allow file-write*", p.subpaths(spec.Roots...), nil)

	// The project's control files, wherever in it they are.
	var controls []string
	for _, root := range spec.Roots {
		for _, r := range forms(root) {
			controls = append(controls,
				p.regex(r, `/(.*/)?(`+p.alternatives(ProtectedDirs)+`)(/.*)?$`),
				p.regex(r, `/(.*/)?(`+p.alternatives(ProtectedFiles)+`)$`),
				p.regex(r, `/(`+p.alternatives(ProtectedTopDirs)+`)(/.*)?$`))
		}
	}
	p.rule("deny file-write*", nil, controls)

	// What git writes to stage and commit, in the project's repositories,
	// wherever their git folders are, but none of the files git runs.
	var gitDirs, gitDeny []string
	var gitRegex []string
	for _, repo := range repos {
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
			gitRegex = append(gitRegex, p.literal(d),
				p.regex(d, `/worktrees/[^/]+/(`+p.alternatives(gitControl)+`)(/.*)?$`))
		}
	}
	p.rule("allow file-write*", p.subpaths(gitDirs...), nil)
	p.rule("deny file-write*", p.subpaths(gitDeny...), gitRegex)

	// Mako's own folders: never written, and the hidden ones not read but
	// for what the session runs from them.
	p.rule("deny file-read* file-write*", p.subpaths(spec.Hidden...), nil)
	p.rule("deny file-write*", p.subpaths(spec.ReadOnly...), nil)
	p.rule("allow file-read*", p.subpaths(spec.Readable...), nil)

	// The session's own folders.
	own := slices.DeleteFunc([]string{spec.Home, spec.Temp}, func(s string) bool { return s == "" })
	p.rule("allow file-read* file-write*", p.subpaths(append(own, spec.Writable...)...), nil)

	// The folders themselves, which replaced by a symbolic link would
	// widen the next session's profile to wherever the link points.
	var folders []string
	for _, dir := range append(slices.Clone(spec.Roots), own...) {
		for _, f := range forms(dir) {
			folders = append(folders, p.literal(f))
		}
	}
	p.rule("deny file-write*", nil, folders)

	// Sockets: those the session needs, and those it makes itself.
	p.line("(deny network-outbound (remote unix-socket))")
	sockets := append([]string{"/private/var/run/mDNSResponder"}, spec.Sockets...)
	sockets = append(sockets, own...)
	sockets = append(sockets, spec.Roots...)
	p.rule("allow network-outbound", nil, prefixed("remote unix-socket ", p.subpaths(sockets...)))

	// The ways one program asks another, outside the sandbox, to run
	// something: opening a file or app, an Apple event, a launchd job.
	p.line("(deny lsopen)")
	p.line("(deny appleevent-send)")
	p.line("(deny job-creation)")
	if p.err != nil {
		return "", p.err
	}
	return p.String(), nil
}

// Check reports, as an error, why spec would leave the sandbox open or
// cannot be said in a profile; nil when Profile can make one. The daemon
// asks before it makes anything for a session it would refuse.
func Check(spec Spec) error {
	_, err := check(spec)
	return err
}

// unsafeChars are what a path in a profile cannot hold: they end or
// escape its string, or its line.
const unsafeChars = "\"\\\x00\n\r"

// check refuses a spec that would leave the sandbox open, or that a
// profile cannot say, and returns the repositories of its roots, whose
// git folders the profile opens.
func check(spec Spec) ([]repo, error) {
	if len(spec.Roots) == 0 {
		return nil, errors.New("workspace sandbox: no project folder")
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
			return nil, fmt.Errorf("workspace sandbox: %q is not an absolute path", path)
		}
		// Each as it resolves, too: the profile has both.
		for _, f := range forms(path) {
			if strings.ContainsAny(f, unsafeChars) {
				return nil, fmt.Errorf("workspace sandbox: %q has a character a sandbox profile cannot hold", f)
			}
		}
	}
	for _, root := range spec.Roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("workspace sandbox: project folder: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace sandbox: project folder %s is not a folder", root)
		}
		if err := checkWritable("the project folder", root, home, spec.Hidden); err != nil {
			return nil, err
		}
	}
	// The session's own folders: Mako makes them, in its folders or the
	// system's temporary one, but one replaced by a symbolic link would
	// open wherever it points.
	for _, own := range slices.Concat([]string{spec.Home, spec.Temp}, spec.Writable) {
		if own == "" {
			continue
		}
		if err := checkOwn(own, home, spec.Hidden); err != nil {
			return nil, err
		}
	}
	var repos []repo
	for _, root := range spec.Roots {
		r := findRepo(root)
		if err := checkRepo(r, root, spec.Roots, home, spec.Hidden); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, nil
}

// checkWritable refuses path, which the profile lets the session write,
// in every form, when it is the root folder, is or holds the user's home
// or Mako's folders, or is in a folder of the home where programs keep
// settings they run code from.
func checkWritable(what, path, home string, hidden []string) error {
	for _, r := range forms(path) {
		if r == "/" {
			return fmt.Errorf("workspace sandbox: %s cannot be the root folder", what)
		}
		if home != "" && slices.ContainsFunc(forms(home), func(h string) bool { return within(h, r) }) {
			return fmt.Errorf("workspace sandbox: %s %s is or holds your home folder, whose settings and start-up files the sandbox must keep; open a project folder instead", what, path)
		}
		if dir := settingsFolder(home, r); dir != "" {
			return fmt.Errorf("workspace sandbox: %s %s is in %s, where programs outside the sandbox keep settings they run code from; open a project folder instead", what, path, dir)
		}
		for _, hide := range hidden {
			for _, h := range forms(hide) {
				if within(h, r) || within(r, h) {
					return fmt.Errorf("workspace sandbox: %s %s overlaps Mako's folder %s", what, path, hide)
				}
			}
		}
	}
	return nil
}

// checkOwn refuses one of the session's own folders, which may be in
// Mako's folders but not hold them, when it is the root folder or holds
// the user's home, or when it is a symbolic link out of the folder that
// holds it.
func checkOwn(path, home string, hidden []string) error {
	parent := resolve(filepath.Dir(filepath.Clean(path)))
	for _, r := range forms(path) {
		if r == "/" || filepath.Dir(r) == r {
			return fmt.Errorf("workspace sandbox: the session's folder %s cannot be the root folder", path)
		}
		if home != "" && slices.ContainsFunc(forms(home), func(h string) bool { return within(h, r) }) {
			return fmt.Errorf("workspace sandbox: the session's folder %s is or holds your home folder", path)
		}
		for _, hide := range hidden {
			for _, h := range forms(hide) {
				if within(h, r) && !samePath(h, r) {
					return fmt.Errorf("workspace sandbox: the session's folder %s holds Mako's folder %s", path, hide)
				}
			}
		}
		if !within(r, parent) && !within(r, filepath.Dir(filepath.Clean(path))) {
			return fmt.Errorf("workspace sandbox: the session's folder %s is a link to %s, outside the folder that holds it", path, r)
		}
	}
	return nil
}

// checkRepo refuses the git folders of root's repository, which the
// profile lets the session write, when they are where it must not write,
// or not where git keeps the git folders of a repository whose work tree
// holds root: in a project folder, the .git folder of root or of a folder
// that holds it, or a folder that names root's work tree back, as a
// worktree's or a submodule's does. A .git file can name any folder; the
// folder it names cannot be made to name root back but by who could write
// it already.
func checkRepo(r repo, root string, roots []string, home string, hidden []string) error {
	dirs := r.dirs()
	if len(dirs) == 0 {
		return nil
	}
	for _, path := range append(slices.Clone(dirs), r.hooks) {
		for _, f := range forms(path) {
			if path != "" && strings.ContainsAny(f, unsafeChars) {
				return fmt.Errorf("workspace sandbox: the git folder %q of %s has a character a sandbox profile cannot hold", f, root)
			}
		}
	}
	for _, dir := range dirs {
		if err := checkWritable("the git folder", dir, home, hidden); err != nil {
			return fmt.Errorf("%w (of the repository of %s)", err, root)
		}
		for _, d := range forms(dir) {
			for _, other := range roots {
				if slices.ContainsFunc(forms(other), func(o string) bool { return within(o, d) }) {
					return fmt.Errorf("workspace sandbox: the git folder %s of %s holds the project folder %s, which would open the folders beside it", dir, root, other)
				}
			}
		}
	}
	linked := linksBack(r, root)
	for _, dir := range dirs {
		for _, d := range forms(dir) {
			inRoot := slices.ContainsFunc(roots, func(other string) bool {
				return slices.ContainsFunc(forms(other), func(o string) bool { return within(d, o) })
			})
			ofRoot := strings.EqualFold(filepath.Base(d), ".git") && slices.ContainsFunc(forms(root), func(o string) bool { return within(o, filepath.Dir(d)) })
			if !inRoot && !ofRoot && !linked {
				return fmt.Errorf("workspace sandbox: the git folder %s of %s is outside the project and does not name it as its work tree; open the repository's own folder, or set its core.worktree", dir, root)
			}
		}
	}
	return nil
}

// linksBack reports whether the git folders of r name a work tree that
// holds root: a worktree's admin folder, in its repository's worktrees
// folder, names the worktree's .git in its gitdir file, and a submodule's
// git folder names its work tree in core.worktree.
func linksBack(r repo, root string) bool {
	holdsRoot := func(tree string) bool {
		return slices.ContainsFunc(forms(tree), func(t string) bool {
			return slices.ContainsFunc(forms(root), func(o string) bool { return within(o, t) })
		})
	}
	relative := func(p string) string {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, "\x00\n\r") {
			return ""
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(r.gitDir, p)
		}
		return filepath.Clean(p)
	}
	if !samePath(r.gitDir, r.commonDir) {
		admin := filepath.Dir(r.gitDir)
		if !strings.EqualFold(filepath.Base(admin), "worktrees") || !samePath(resolve(filepath.Dir(admin)), resolve(r.commonDir)) {
			return false
		}
		b, err := readSmall(filepath.Join(r.gitDir, "gitdir"))
		if err != nil {
			return false
		}
		dotGit := relative(string(b))
		return dotGit != "" && strings.EqualFold(filepath.Base(dotGit), ".git") && holdsRoot(filepath.Dir(dotGit))
	}
	tree := relative(gitConfig(r.gitDir, "core.worktree"))
	return tree != "" && holdsRoot(tree)
}

// readSmall reads a file of a git folder, which git keeps to a line.
func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 4096))
}

// settingsFolder is the folder of home that path is in where programs
// keep their settings, a dot folder such as ~/.config or ~/Library on
// macOS; "" for none.
func settingsFolder(home, path string) string {
	if home == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for _, h := range forms(home) {
		hp := strings.Split(strings.TrimSuffix(filepath.ToSlash(h), "/"), "/")
		if len(parts) <= len(hp) || !within(path, h) {
			continue
		}
		top := parts[len(hp)]
		if strings.HasPrefix(top, ".") || strings.EqualFold(top, "Library") {
			return filepath.Join(h, top)
		}
	}
	return ""
}

// caseFolds reports whether paths name a file in any case, as they do on
// APFS and HFS+, macOS's file systems: there /users/me/library is
// ~/Library, and every comparison of paths here must take it to be.
var caseFolds = runtime.GOOS == "darwin"

// fold is path as it compares.
func fold(path string) string {
	if caseFolds {
		return strings.ToLower(path)
	}
	return path
}

// within reports whether dir is root or a folder inside it.
func within(dir, root string) bool {
	return pathscope.Within(fold(filepath.Clean(dir)), fold(filepath.Clean(root)))
}

// samePath reports whether a and b are one path.
func samePath(a, b string) bool {
	return fold(filepath.Clean(a)) == fold(filepath.Clean(b))
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

// gitEnv is the daemon's environment without what would point git at
// another repository.
func gitEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"}, name)
	})
}

// findRepo asks git where the repository holding dir keeps its git
// folder, the folder its worktrees share, and its hooks: a worktree's
// .git is a file naming a folder in its main repository's. It is the zero
// repo outside one, or without git.
func findRepo(dir string) repo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--git-path", "hooks")
	cmd.Env = gitEnv()
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

// gitConfig is key in the config file of gitDir alone, "" unset.
func gitConfig(gitDir, key string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "--file", filepath.Join(gitDir, "config"), "--get", key)
	cmd.Env = gitEnv()
	out, err := safeexec.Output(cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// profile builds a Seatbelt profile a line at a time. It keeps the first
// path it was given that a profile cannot hold, which Profile returns.
type profile struct {
	b   strings.Builder
	err error
}

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

// safe is path, which a filter is to hold; one it cannot is kept as the
// profile's error.
func (p *profile) safe(path string) string {
	if strings.ContainsAny(path, unsafeChars) && p.err == nil {
		p.err = fmt.Errorf("workspace sandbox: %q has a character a sandbox profile cannot hold", path)
	}
	return path
}

// literal is a (literal ...) filter for path as it is.
func (p *profile) literal(path string) string {
	return `(literal "` + p.safe(path) + `")`
}

// regex is a (regex ...) filter for what starts with path and goes on as
// pattern, which the code here writes, does.
func (p *profile) regex(path, pattern string) string {
	return `(regex #"^` + quoteRegex(p.safe(path)) + pattern + `")`
}

// subpaths are (subpath ...) filters for paths, in both their forms.
func (p *profile) subpaths(paths ...string) []string {
	var out []string
	for _, path := range paths {
		if path == "" {
			continue
		}
		for _, f := range forms(path) {
			filter := `(subpath "` + p.safe(f) + `")`
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
	if real := resolve(clean); real != clean {
		out = append(out, real)
	}
	return out
}

// resolve is path with its symbolic links resolved as far as it exists:
// what is not made yet is joined to the deepest folder of it that is, and
// a link to what is not made yet is followed to it.
func resolve(path string) string {
	return resolveDepth(filepath.Clean(path), 0)
}

func resolveDepth(path string, depth int) string {
	rest := ""
	for p := path; ; {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		// A link whose target is not made yet resolves to that target.
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 && depth < 40 {
			if target, err := os.Readlink(p); err == nil {
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(p), target)
				}
				return filepath.Join(resolveDepth(filepath.Clean(target), depth+1), rest)
			}
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
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

// alternatives is a regex of any of names.
func (p *profile) alternatives(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quoteRegex(p.safe(n))
	}
	return strings.Join(quoted, "|")
}
