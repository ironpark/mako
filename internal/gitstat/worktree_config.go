package gitstat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"go.yaml.in/yaml/v3"
)

const (
	worktreeConfigFile    = ".mako/worktree.toml"
	orcaFallbackFile      = "orca.yaml"
	worktreeHookDisableEn = "MAKO_NO_WORKTREE_HOOKS"
	worktreeHookTimeout   = 2 * time.Minute
)

// WorktreeConfig configures automatic worktree bootstrap and teardown for a
// repository. It is read from .mako/worktree.toml at the repository root, with
// a read-only compatibility fallback to orca.yaml's worktree.sharedDirectories.
type WorktreeConfig struct {
	BaseRef     string   `toml:"base_ref"`
	SharedPaths []string `toml:"shared_paths"`
	CopyPaths   []string `toml:"copy_paths"`
	SetupCmd    string   `toml:"setup_cmd"`
	TeardownCmd string   `toml:"teardown_cmd"`
}

type orcaYAMLConfig struct {
	Worktree struct {
		SharedDirectories []string `yaml:"sharedDirectories"`
	} `yaml:"worktree"`
}

// LoadWorktreeConfig reads .mako/worktree.toml (or orca.yaml fallback) from
// repo (or the main repository root containing dir). When neither file exists,
// a zero WorktreeConfig and nil error are returned.
func LoadWorktreeConfig(repo string) (WorktreeConfig, error) {
	if strings.TrimSpace(repo) == "" {
		return WorktreeConfig{}, nil
	}
	tomlPath := filepath.Join(repo, filepath.FromSlash(worktreeConfigFile))
	if data, err := os.ReadFile(tomlPath); err == nil {
		var cfg WorktreeConfig
		md, err := toml.Decode(string(data), &cfg)
		if err != nil {
			return WorktreeConfig{}, fmt.Errorf("parse %s: %w", worktreeConfigFile, err)
		}
		if undec := md.Undecoded(); len(undec) > 0 {
			return WorktreeConfig{}, fmt.Errorf("parse %s: unknown field %q", worktreeConfigFile, undec[0].String())
		}
		if err := validateWorktreeConfig(cfg); err != nil {
			return WorktreeConfig{}, fmt.Errorf("%s: %w", worktreeConfigFile, err)
		}
		return cfg, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeConfig{}, fmt.Errorf("read %s: %w", worktreeConfigFile, err)
	}

	orcaPath := filepath.Join(repo, orcaFallbackFile)
	if data, err := os.ReadFile(orcaPath); err == nil {
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

func cleanWorktreeRelPath(raw string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/"))
	if p == "" {
		return "", errors.New("path must not be empty")
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(raw) {
		return "", fmt.Errorf("path %q must be relative to the repository root", raw)
	}
	clean := filepath.Clean(filepath.FromSlash(p))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the repository root", raw)
	}
	first := strings.Split(filepath.ToSlash(clean), "/")[0]
	if first == ".git" {
		return "", fmt.Errorf("path %q must not target .git", raw)
	}
	return clean, nil
}

func pathWithin(root, target string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = filepath.Clean(root)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		realTarget = filepath.Clean(target)
	}
	rel, err := filepath.Rel(realRoot, realTarget)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func applyWorktreeBootstrap(ctx context.Context, repo, wtPath, branch, base string, cfg WorktreeConfig) error {
	for _, raw := range cfg.SharedPaths {
		rel, err := cleanWorktreeRelPath(raw)
		if err != nil {
			return err
		}
		src := filepath.Join(repo, rel)
		if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("stat shared path %s: %w", rel, err)
		}
		if !pathWithin(repo, src) {
			return fmt.Errorf("shared path %s resolves outside repository", rel)
		}
		dst := filepath.Join(wtPath, rel)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir for shared path %s: %w", rel, err)
		}
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlink shared path %s: %w", rel, err)
		}
	}

	for _, raw := range cfg.CopyPaths {
		rel, err := cleanWorktreeRelPath(raw)
		if err != nil {
			return err
		}
		src := filepath.Join(repo, rel)
		info, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("stat copy path %s: %w", rel, err)
		}
		if !pathWithin(repo, src) {
			return fmt.Errorf("copy path %s resolves outside repository", rel)
		}
		dst := filepath.Join(wtPath, rel)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := copyWorktreePath(repo, src, dst, info); err != nil {
			return fmt.Errorf("copy path %s: %w", rel, err)
		}
	}

	if cmdStr := strings.TrimSpace(cfg.SetupCmd); cmdStr != "" && os.Getenv(worktreeHookDisableEn) != "1" {
		if out, err := runWorktreeHook(ctx, wtPath, repo, branch, base, cmdStr); err != nil {
			if out != "" {
				return fmt.Errorf("worktree setup_cmd failed: %w\n%s", err, out)
			}
			return fmt.Errorf("worktree setup_cmd failed: %w", err)
		}
	}
	return nil
}

func cleanupWorktreeBeforeRemove(ctx context.Context, repo, wtPath, branch, base string) error {
	cfg, err := LoadWorktreeConfig(repo)
	if err != nil {
		// Don't block removal on a malformed config; proceed with git worktree remove.
		return nil
	}
	if cmdStr := strings.TrimSpace(cfg.TeardownCmd); cmdStr != "" && os.Getenv(worktreeHookDisableEn) != "1" {
		if out, err := runWorktreeHook(ctx, wtPath, repo, branch, base, cmdStr); err != nil {
			if out != "" {
				return fmt.Errorf("worktree teardown_cmd failed: %w\n%s", err, out)
			}
			return fmt.Errorf("worktree teardown_cmd failed: %w", err)
		}
	}
	// Remove symlinks created by shared_paths if they still point into repo so
	// git worktree remove does not reject untracked bootstrap symlinks.
	for _, raw := range cfg.SharedPaths {
		rel, err := cleanWorktreeRelPath(raw)
		if err != nil {
			continue
		}
		dst := filepath.Join(wtPath, rel)
		info, err := os.Lstat(dst)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(dst)
		if err != nil {
			continue
		}
		want := filepath.Join(repo, rel)
		if filepath.Clean(target) == filepath.Clean(want) {
			_ = os.Remove(dst)
		}
	}
	return nil
}

func copyWorktreePath(repo, src, dst string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		if !pathWithin(repo, src) {
			return fmt.Errorf("symlink %s escapes repository", src)
		}
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	if !info.IsDir() {
		return copyRegularFile(src, dst, info.Mode().Perm())
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		outPath := filepath.Join(dst, rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return os.MkdirAll(outPath, fi.Mode().Perm())
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if !pathWithin(repo, p) {
				return fmt.Errorf("symlink %s escapes repository", p)
			}
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, outPath)
		}
		return copyRegularFile(p, outPath, fi.Mode().Perm())
	})
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func runWorktreeHook(ctx context.Context, wtPath, repo, branch, base, command string) (string, error) {
	hookCtx, cancel := context.WithTimeout(ctx, worktreeHookTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(hookCtx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(hookCtx, "sh", "-c", command)
	}
	cmd.Dir = wtPath
	cmd.Env = append(os.Environ(),
		"MAKO_WORKTREE_PATH="+wtPath,
		"MAKO_WORKTREE_REPO="+repo,
		"MAKO_WORKTREE_BRANCH="+branch,
		"MAKO_WORKTREE_BASE="+base,
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
