package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// CanonicalDirs resolves each non-empty directory to an absolute, symlink-resolved,
// clean path, deduplicated in order. If symlink resolution fails (for example,
// when a path does not exist yet), the cleaned absolute path is kept.
func CanonicalDirs(dirs []string) []string {
	var out []string
	seen := make(map[string]bool, len(dirs))
	for _, raw := range dirs {
		d := strings.TrimSpace(raw)
		if d == "" {
			continue
		}
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			d = real
		}
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// LaunchHome resolves the effective home directory for launch env, honoring
// HOME (and USERPROFILE on Windows) before falling back to os.UserHomeDir.
func LaunchHome(env map[string]string) (string, error) {
	if env != nil {
		if h := strings.TrimSpace(env["HOME"]); h != "" {
			return h, nil
		}
		if runtime.GOOS == "windows" {
			if h := strings.TrimSpace(env["USERPROFILE"]); h != "" {
				return h, nil
			}
		}
	}
	return os.UserHomeDir()
}

// LookupEnv resolves key from launch env if present (even if empty, allowing an
// explicit override to clear an inherited variable), otherwise os.Getenv.
func LookupEnv(env map[string]string, key string) string {
	if env != nil {
		if v, ok := env[key]; ok {
			return v
		}
	}
	return os.Getenv(key)
}

// ExtractDirs extracts directory arguments passed via --add-dir (and, when
// recognized by f.Values, -C / --cd) from command, resolving relative paths
// against baseDir. For variadic --add-dir flags, only consecutive non-flag
// arguments that exist as directories on disk are consumed so a trailing
// positional prompt is never mistaken for a directory.
func (f *Flags) ExtractDirs(command []string, baseDir string) []string {
	if len(command) <= 1 {
		return nil
	}
	hasAddVariadic := slices.Contains(f.Variadic, "--add-dir")
	hasAddValue := slices.Contains(f.Values, "--add-dir")
	hasCD := slices.Contains(f.Values, "--cd") || slices.Contains(f.Values, "-C")
	if !hasAddVariadic && !hasAddValue && !hasCD {
		return nil
	}

	resolve := func(raw string) string {
		p := strings.TrimSpace(raw)
		if p == "" {
			return ""
		}
		if !filepath.IsAbs(p) && baseDir != "" {
			p = filepath.Join(baseDir, p)
		}
		return filepath.Clean(p)
	}
	isDir := func(path string) bool {
		fi, err := os.Stat(path)
		return err == nil && fi.IsDir()
	}

	var out []string
	for i := 1; i < len(command); i++ {
		arg := command[i]
		if arg == "--" {
			break
		}
		name, val, hasEq := strings.Cut(arg, "=")
		switch {
		case name == "--add-dir" && (hasAddVariadic || hasAddValue):
			if hasEq {
				if p := resolve(val); p != "" {
					out = append(out, p)
				}
				continue
			}
			if hasAddVariadic {
				for i+1 < len(command) && !strings.HasPrefix(command[i+1], "-") {
					p := resolve(command[i+1])
					if p == "" || !isDir(p) {
						break
					}
					out = append(out, p)
					i++
				}
			} else if i+1 < len(command) {
				i++
				if p := resolve(command[i]); p != "" {
					out = append(out, p)
				}
			}
		case hasCD && (name == "-C" || name == "--cd"):
			if hasEq {
				if p := resolve(val); p != "" {
					out = append(out, p)
				}
			} else if i+1 < len(command) {
				i++
				if p := resolve(command[i]); p != "" {
					out = append(out, p)
				}
			}
		case !hasEq && (name == f.PromptFlag || name == f.PinFlag || slices.Contains(f.Values, name) || slices.Contains(f.PickerValues, name) || slices.Contains(f.ResumeFlags, name)):
			if i+1 < len(command) {
				i++
			}
		}
	}
	return out
}
