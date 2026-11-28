package gitstat

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"go.yaml.in/yaml/v3"

	"github.com/ironpark/mako/internal/repotrust"
	"github.com/ironpark/mako/internal/safeexec"
)

const (
	// WorktreeConfigFile is where a repository keeps its worktree bootstrap,
	// from its root.
	WorktreeConfigFile = ".mako/worktree.toml"
	orcaFallbackFile   = "orca.yaml"
	// worktreeHookDisableEnv set to 1 in the daemon's environment runs no
	// setup_cmd or teardown_cmd, trusted or not.
	worktreeHookDisableEnv = "MAKO_NO_WORKTREE_HOOKS"
	// worktreeHookTimeout is how long a setup_cmd or teardown_cmd can run.
	worktreeHookTimeout = 2 * time.Minute
	// maxWorktreeConfig is the largest .mako/worktree.toml or orca.yaml
	// read: as large as a command file the user can trust, which is
	// repotrust.MaxSize.
	maxWorktreeConfig = 256 << 10
)

// HookTrust says whether the user trusts the commands of file, a
// slash-separated path from the root of the repository at repo, as data
// has it. A nil HookTrust trusts nothing.
type HookTrust func(repo, file string, data []byte) bool

// WorktreeConfig configures automatic worktree bootstrap and teardown for a
// repository. It is read from .mako/worktree.toml at the repository root, with
// a read-only compatibility fallback to orca.yaml's worktree.sharedDirectories.
//
// The repository's own file gives setup_cmd and teardown_cmd, which run
// with the user's permissions; anyone who can commit to the repository, or
// an agent working in it, can write them. They run only while the user
// trusts the file as it reads (internal/repotrust), checked against the
// bytes they were read from.
type WorktreeConfig struct {
	BaseRef     string   `toml:"base_ref"`
	SharedPaths []string `toml:"shared_paths"`
	CopyPaths   []string `toml:"copy_paths"`
	SetupCmd    string   `toml:"setup_cmd"`
	TeardownCmd string   `toml:"teardown_cmd"`

	// data is the file as it was read, which a trust is checked against.
	data []byte
}

// hooksRun reports whether cfg's commands, read from the repository at
// repo, may run: trusted as they read, and not turned off for the daemon.
func (cfg WorktreeConfig) hooksRun(repo string, trusted HookTrust) bool {
	return trusted != nil && cfg.data != nil && os.Getenv(worktreeHookDisableEnv) != "1" &&
		trusted(repo, WorktreeConfigFile, cfg.data)
}

// skippedHook is the line a worktree's output ends with when its
// repository's cmd did not run, untrusted: what to run to trust it.
func skippedHook(repo, cmd string) string {
	file := filepath.Join(repo, filepath.FromSlash(WorktreeConfigFile))
	return fmt.Sprintf("%s of %s did not run: the file is not trusted as it reads. Review it and trust it with: %s", cmd, file, repotrust.AllowCommand("", file))
}

type orcaYAMLConfig struct {
	Worktree struct {
		SharedDirectories []string `yaml:"sharedDirectories"`
	} `yaml:"worktree"`
}

// LoadWorktreeConfig reads .mako/worktree.toml (or orca.yaml fallback) from
// the main work tree of the repository dir is in, from any folder or
// worktree of it, or from dir itself outside a repository. When neither
// file exists, a zero WorktreeConfig and nil error are returned.
func LoadWorktreeConfig(dir string) (WorktreeConfig, error) {
	if strings.TrimSpace(dir) == "" {
		return WorktreeConfig{}, nil
	}
	repo := cmp.Or(Repo(dir), dir)
	if data, err := readRepoFile(repo, WorktreeConfigFile, maxWorktreeConfig); err == nil {
		var cfg WorktreeConfig
		md, err := toml.Decode(string(data), &cfg)
		if err != nil {
			return WorktreeConfig{}, fmt.Errorf("parse %s: %w", WorktreeConfigFile, err)
		}
		if undec := md.Undecoded(); len(undec) > 0 {
			return WorktreeConfig{}, fmt.Errorf("parse %s: unknown field %q", WorktreeConfigFile, undec[0].String())
		}
		if err := validateWorktreeConfig(cfg); err != nil {
			return WorktreeConfig{}, fmt.Errorf("%s: %w", WorktreeConfigFile, err)
		}
		cfg.data = data
		return cfg, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeConfig{}, fmt.Errorf("read %s: %w", WorktreeConfigFile, err)
	}

	if data, err := readRepoFile(repo, orcaFallbackFile, maxWorktreeConfig); err == nil {
		var oc orcaYAMLConfig
		if err := yaml.Unmarshal(data, &oc); err != nil {
			return WorktreeConfig{}, fmt.Errorf("parse %s: %w", orcaFallbackFile, err)
		}
		cfg := WorktreeConfig{SharedPaths: oc.Worktree.SharedDirectories}
		if err := validateWorktreeConfig(cfg); err != nil {
			return WorktreeConfig{}, fmt.Errorf("%s: %w", orcaFallbackFile, err)
		}
		return cfg, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeConfig{}, fmt.Errorf("read %s: %w", orcaFallbackFile, err)
	}
	return WorktreeConfig{}, nil
}

// readRepoFile reads the file rel, slash-separated, of the repository at
// repo: a regular file of at most max bytes, through no link that leads
// out of the repository. It does not wait on what is no regular file, as
// opening a FIFO waits for a writer, so a repository's config cannot hold
// up the worktrees of it waiting for one; a missing file is
// os.ErrNotExist.
func readRepoFile(repo, rel string, max int64) ([]byte, error) {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(io.LimitReader(f, max+1)); err != nil {
		return nil, err
	}
	if int64(b.Len()) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", rel, max)
	}
	return b.Bytes(), nil
}

func validateWorktreeConfig(cfg WorktreeConfig) error {
	if strings.HasPrefix(strings.TrimSpace(cfg.BaseRef), "-") {
		return fmt.Errorf("base_ref %q must not start with '-'", cfg.BaseRef)
	}
	for _, p := range cfg.SharedPaths {
		if _, err := cleanWorktreeRelPath(p); err != nil {
			return fmt.Errorf("shared_paths: %w", err)
		}
	}
	for _, p := range cfg.CopyPaths {
		if _, err := cleanWorktreeRelPath(p); err != nil {
			return fmt.Errorf("copy_paths: %w", err)
		}
	}
	return nil
}

// cleanWorktreeRelPath is raw, a path a repository's config names from
// its root, made clean and native; it refuses one that leaves the root, is
// the root, or goes through a .git.
func cleanWorktreeRelPath(raw string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/"))
	if p == "" {
		return "", errors.New("path must not be empty")
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(raw) || filepath.VolumeName(filepath.FromSlash(p)) != "" {
		return "", fmt.Errorf("path %q must be relative to the repository root", raw)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes the repository root", raw)
	}
	if dotGitPath(clean) {
		return "", fmt.Errorf("path %q must not target .git", raw)
	}
	return filepath.FromSlash(clean), nil
}

// dotGitPath reports whether the slash-separated path p goes through a
// .git, as a case-insensitive file system reads it, or Windows, which
// drops a name's trailing dots and spaces.
func dotGitPath(p string) bool {
	return slices.ContainsFunc(strings.Split(p, "/"), func(seg string) bool {
		return strings.EqualFold(strings.TrimRight(seg, ". "), ".git")
	})
}

// repoSource is what is at rel in the repository whose root, symlinks
// resolved, is realRepo, without following a last symlink; ok is false
// when nothing is there. It refuses a path that resolves outside the
// repository, to its root, or into a .git, and one through a symlink that
// leads nowhere, which could lead anywhere later.
func repoSource(realRepo, rel string) (info fs.FileInfo, ok bool, err error) {
	src := filepath.Join(realRepo, rel)
	info, err = os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return nil, false, fmt.Errorf("%s does not resolve: %w", rel, err)
	}
	r, err := filepath.Rel(realRepo, real)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || dotGitPath(filepath.ToSlash(r)) {
		return nil, false, fmt.Errorf("%s resolves outside the repository's files", rel)
	}
	return info, true, nil
}

// worktreeDest checks that rel can be made in the worktree open at wt,
// with no symlink on the way to it that could lead what is written there
// out of the worktree; exists says something is there already, to be left
// as it is.
func worktreeDest(wt *os.Root, rel, raw string) (exists bool, err error) {
	if err := checkNoIntermediateSymlink(wt, filepath.ToSlash(rel), raw); err != nil {
		return false, err
	}
	if _, err := wt.Lstat(rel); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := wt.MkdirAll(dir, 0o755); err != nil {
			return false, err
		}
	}
	return false, nil
}

// applyWorktreeBootstrap links and copies cfg's paths into the new
// worktree at wtPath, and runs its setup_cmd there if trusted, told of
// the worktree's branch and the base it records. skipped says the
// setup_cmd did not run, untrusted.
//
// What is linked or copied must be the repository's own files, and is
// written only inside the worktree: os.Root keeps every write in it, and
// a symlink the checkout has on the way to a path is refused.
func applyWorktreeBootstrap(ctx context.Context, repo, wtPath, branch, base string, cfg WorktreeConfig, trusted HookTrust) (skipped bool, err error) {
	if err := bootstrapPaths(repo, wtPath, cfg); err != nil {
		return false, err
	}
	cmdStr := strings.TrimSpace(cfg.SetupCmd)
	if cmdStr == "" {
		return false, nil
	}
	if !cfg.hooksRun(repo, trusted) {
		return cfg.data != nil && os.Getenv(worktreeHookDisableEnv) != "1", nil
	}
	if out, err := runWorktreeHook(ctx, 0, wtPath, repo, branch, base, cmdStr); err != nil {
		if out != "" {
			return false, fmt.Errorf("worktree setup_cmd failed: %w\n%s", err, out)
		}
		return false, fmt.Errorf("worktree setup_cmd failed: %w", err)
	}
	return false, nil
}

// teardownWorktree runs the repository's teardown_cmd in the worktree at
// wtPath if trusted, leaving removeReserve of ctx's time for the removal
// after it. notice is what to tell of it: that it did not run, untrusted,
// or that it failed or ran out of time, which does not stop the
// worktree's removal, with the end of what it printed.
func teardownWorktree(ctx context.Context, repo, wtPath, branch, base string, cfg WorktreeConfig, trusted HookTrust) (notice string) {
	cmdStr := strings.TrimSpace(cfg.TeardownCmd)
	switch {
	case cmdStr == "":
		return ""
	case !cfg.hooksRun(repo, trusted):
		if cfg.data != nil && os.Getenv(worktreeHookDisableEnv) != "1" {
			return skippedHook(repo, "teardown_cmd")
		}
		return ""
	}
	out, err := runWorktreeHook(ctx, removeReserve, wtPath, repo, branch, base, cmdStr)
	if err == nil {
		return ""
	}
	if lines := strings.Split(out, "\n"); out != "" {
		out = strings.Join(lines[max(0, len(lines)-10):], "\n") + "\n"
	}
	return fmt.Sprintf("%swarning: teardown_cmd failed (%v); the worktree was removed anyway", out, err)
}

// bootstrapLeft is what of the worktree at wtPath stands in the way of
// removing it, and what of it the bootstrap made as it made it. made are
// cfg's shared_paths linked to the repository at repo and the untracked
// files of its copy_paths still the same as the repository's, which git
// would refuse to remove the worktree over and can be made again; changed
// are the changes to tracked files, and the untracked files that are not
// made, which lose work if the worktree goes. Both are slash-separated
// paths from the worktree's root.
func bootstrapLeft(ctx context.Context, repo, wtPath string, cfg WorktreeConfig) (made, changed []string, err error) {
	out, err := gitOut(ctx, wtPath, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, nil, err
	}
	var untracked []string
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		r := records[i]
		if len(r) < 4 {
			continue
		}
		switch xy, p := r[:2], r[3:]; {
		case xy == "??":
			untracked = append(untracked, p)
		case xy[0] == 'R' || xy[0] == 'C':
			i++ // the path it was renamed or copied from
			fallthrough
		default:
			changed = append(changed, p)
		}
	}
	if len(cfg.SharedPaths) == 0 && len(cfg.CopyPaths) == 0 {
		return nil, append(changed, untracked...), nil
	}
	wt, err := os.OpenRoot(wtPath)
	if err != nil {
		return nil, nil, err
	}
	defer wt.Close()
	src, err := os.OpenRoot(repo)
	if err != nil {
		return nil, nil, err
	}
	defer src.Close()
	for _, raw := range cfg.SharedPaths {
		rel, err := cleanWorktreeRelPath(raw)
		if err != nil {
			continue
		}
		if checkNoIntermediateSymlink(wt, filepath.ToSlash(rel), raw) != nil {
			continue
		}
		if target, err := wt.Readlink(rel); err == nil && filepath.Clean(target) == filepath.Join(repo, rel) {
			made = append(made, filepath.ToSlash(rel))
		}
	}
	var copies []string
	for _, raw := range cfg.CopyPaths {
		if rel, err := cleanWorktreeRelPath(raw); err == nil {
			copies = append(copies, filepath.ToSlash(rel))
		}
	}
	for _, p := range untracked {
		if slices.Contains(made, p) {
			continue
		}
		within := slices.ContainsFunc(copies, func(c string) bool { return p == c || strings.HasPrefix(p, c+"/") })
		if within && checkNoIntermediateSymlink(wt, p, p) == nil && sameEntry(src, wt, filepath.FromSlash(p)) {
			made = append(made, p)
		} else {
			changed = append(changed, p)
		}
	}
	return made, changed, nil
}

// sameEntry reports whether name is the same in the repository open at
// src as in the worktree open at wt: both regular files with the same
// bytes, or both symlinks that read the same.
func sameEntry(src, wt *os.Root, name string) bool {
	a, err := src.Lstat(name)
	if err != nil {
		return false
	}
	b, err := wt.Lstat(name)
	if err != nil || a.Mode().Type() != b.Mode().Type() {
		return false
	}
	switch {
	case a.Mode()&fs.ModeSymlink != 0:
		ta, errA := src.Readlink(name)
		tb, errB := wt.Readlink(name)
		return errA == nil && errB == nil && ta == tb
	case !a.Mode().IsRegular() || a.Size() != b.Size():
		return false
	}
	fa, err := src.Open(name)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := wt.Open(name)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 32<<10), make([]byte, 32<<10)
	for {
		na, errA := io.ReadFull(fa, ba)
		nb, errB := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if errA != nil || errB != nil {
			return (errA == io.EOF || errA == io.ErrUnexpectedEOF) && (errB == io.EOF || errB == io.ErrUnexpectedEOF)
		}
	}
}

// removeMade removes the paths made, slash-separated from the root of
// the worktree at wtPath, and each folder over them that it leaves empty.
func removeMade(wtPath string, made []string) error {
	wt, err := os.OpenRoot(wtPath)
	if err != nil {
		return err
	}
	defer wt.Close()
	for _, p := range made {
		if err := wt.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			if wt.Remove(filepath.FromSlash(dir)) != nil {
				break
			}
		}
	}
	return nil
}

// bootstrapPaths links cfg's shared_paths and copies its copy_paths from
// the repository at repo into the worktree at wtPath, leaving alone any
// already there. It can run again over what it made.
func bootstrapPaths(repo, wtPath string, cfg WorktreeConfig) error {
	if len(cfg.SharedPaths) == 0 && len(cfg.CopyPaths) == 0 {
		return nil
	}
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return err
	}
	wt, err := os.OpenRoot(wtPath)
	if err != nil {
		return err
	}
	defer wt.Close()
	for _, raw := range cfg.SharedPaths {
		rel, err := cleanWorktreeRelPath(raw)
		if err != nil {
			return err
		}
		if _, ok, err := repoSource(realRepo, rel); err != nil {
			return fmt.Errorf("shared path %s: %w", rel, err)
		} else if !ok {
			continue
		}
		if exists, err := worktreeDest(wt, rel, raw); err != nil {
			return fmt.Errorf("shared path %s: %w", rel, err)
		} else if exists {
			continue
		}
		if err := wt.Symlink(filepath.Join(repo, rel), rel); err != nil {
			return fmt.Errorf("symlink shared path %s: %w", rel, err)
		}
	}
	if len(cfg.CopyPaths) == 0 {
		return nil
	}
	src, err := os.OpenRoot(realRepo)
	if err != nil {
		return err
	}
	defer src.Close()
	for _, raw := range cfg.CopyPaths {
		rel, err := cleanWorktreeRelPath(raw)
		if err != nil {
			return err
		}
		info, ok, err := repoSource(realRepo, rel)
		if err != nil {
			return fmt.Errorf("copy path %s: %w", rel, err)
		} else if !ok {
			continue
		}
		if exists, err := worktreeDest(wt, rel, raw); err != nil {
			return fmt.Errorf("copy path %s: %w", rel, err)
		} else if exists {
			continue
		}
		if err := copyWorktreePath(src, wt, filepath.ToSlash(rel), info); err != nil {
			return fmt.Errorf("copy path %s: %w", rel, err)
		}
	}
	return nil
}

// copyWorktreePath copies name, a slash-separated path, from the
// repository open at src, its root with links resolved, to the worktree
// open at wt, where nothing is at it yet: a directory with all in it.
// Only directories, regular files and symlinks that stay among the
// repository's files are copied; a FIFO, a socket or a device is left
// out, and so is a .git in it, as a repository checked out inside has,
// whose config and hooks git would run in the worktree.
func copyWorktreePath(src, wt *os.Root, name string, info fs.FileInfo) error {
	if !info.IsDir() {
		return copyWorktreeEntry(src, wt, name, info.Mode())
	}
	return fs.WalkDir(src.FS(), name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dotGitPath(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if err := wt.Mkdir(filepath.FromSlash(p), fi.Mode().Perm()|0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			return nil
		}
		return copyWorktreeEntry(src, wt, p, d.Type())
	})
}

// copyWorktreeEntry copies the file or symlink at name, of the type mode
// gives, as copyWorktreePath does.
func copyWorktreeEntry(src, wt *os.Root, name string, mode fs.FileMode) error {
	native := filepath.FromSlash(name)
	switch {
	case mode&fs.ModeSymlink != 0:
		target, err := src.Readlink(native)
		if err != nil {
			return err
		}
		// The link is read again from the worktree, so where it leads is
		// what its words say from there: it must stay among the files
		// both have, and not be the root or a .git.
		t := filepath.ToSlash(target)
		to := path.Join(path.Dir(name), t)
		if filepath.IsAbs(target) || path.IsAbs(t) || filepath.VolumeName(target) != "" ||
			to == "." || to == ".." || strings.HasPrefix(to, "../") || dotGitPath(to) {
			return fmt.Errorf("symlink %s leads outside the repository's files, to %s", name, target)
		}
		// And so must where it ends up, through the links it leads on to,
		// as a tracked link out of the repository: resolved in the
		// repository, whose links the worktree checks out. One that leads
		// nowhere there is left out, as it could lead anywhere later.
		if _, ok, err := repoSource(src.Name(), native); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("symlink %s: %w", name, err)
		} else if !ok {
			return nil
		}
		return wt.Symlink(target, native)
	case mode.IsRegular():
		return copyRegularFile(src, wt, native)
	}
	return nil
}

// copyRegularFile copies the regular file at name in the repository open
// at src to the worktree open at wt, where nothing is at it yet.
func copyRegularFile(src, wt *os.Root, name string) error {
	before, err := src.Lstat(name)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return nil
	}
	in, err := src.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return fmt.Errorf("%s changed while it was copied", name)
	}
	out, err := wt.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// runWorktreeHook runs a repository's setup_cmd or teardown_cmd, command,
// in the worktree at wtPath, told of the worktree as the environment
// says, and nothing of it in its words. It has worktreeHookTimeout, and
// leaves reserve of ctx's time for what comes after it. When its time is
// up, what it started goes with it: on unix its process group is killed,
// not only its shell.
func runWorktreeHook(ctx context.Context, reserve time.Duration, wtPath, repo, branch, base, command string) (string, error) {
	limit := worktreeHookTimeout
	if dl, ok := ctx.Deadline(); ok {
		limit = min(limit, time.Until(dl)-reserve)
	}
	if limit <= 0 {
		return "", errors.New("no time was left for it")
	}
	if runtime.GOOS == "windows" {
		if err := cmdSafe(branch, base); err != nil {
			return "", err
		}
	}
	hookCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(hookCtx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(hookCtx, "sh", "-c", command)
	}
	cmd.Dir = wtPath
	hookGroup(cmd)
	// A command the timeout killed can leave children holding its output
	// open, which waiting on it would wait for too.
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(),
		"MAKO_WORKTREE_PATH="+wtPath,
		"MAKO_WORKTREE_REPO="+repo,
		"MAKO_WORKTREE_BRANCH="+branch,
		"MAKO_WORKTREE_BASE="+base,
	)
	out, err := safeexec.CombinedOutput(cmd)
	if err != nil && errors.Is(hookCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("ran out of its %s", limit.Round(time.Second))
	}
	return strings.TrimSpace(string(out)), err
}

// cmdSpecial are the characters cmd reads as its own in a line, after it
// puts in the values of the variables the line names as %NAME%.
const cmdSpecial = "&|<>^%!\"()"

// cmdSafe refuses a branch, as vals are, with a character cmd reads as its
// own: a hook that names one as %MAKO_WORKTREE_BRANCH% would run what
// follows it. git takes such characters in a branch's name, and a branch
// is named by whoever starts a session, an agent included.
func cmdSafe(vals ...string) error {
	for _, v := range vals {
		if i := strings.IndexAny(v, cmdSpecial); i >= 0 {
			return fmt.Errorf("%q has %q, which cmd would read as its own: name the branch without any of %s to run the repository's commands on Windows", v, v[i], cmdSpecial)
		}
	}
	return nil
}
