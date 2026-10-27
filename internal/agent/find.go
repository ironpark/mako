package agent

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Find is where program is: on the PATH, else in the folders installers put
// programs in on this OS, else in dirs, folders under the home folder the
// agent's own installer uses. The PATH a desktop gives an app, or a
// service, may leave out where the user's shell finds the agent.
func Find(program string, dirs ...string) (string, error) {
	path, err := exec.LookPath(program)
	if err == nil || strings.ContainsAny(program, `/\`) {
		return path, err
	}
	home, _ := os.UserHomeDir()
	for _, dir := range installDirs(runtime.GOOS, home, os.Getenv, dirs) {
		for _, name := range programFiles(runtime.GOOS, program, os.Getenv) {
			if p := filepath.Join(dir, name); runnable(p) {
				return p, nil
			}
		}
	}
	return "", err
}

// installDirs are the folders to look for a program in, the agent's own
// dirs first, then where installers put programs on goos: the native
// installers', the package managers', and the Node.js managers' that
// install the agents npm carries.
func installDirs(goos, home string, env func(string) string, dirs []string) []string {
	var out []string
	under := func(base string, rel ...string) {
		if base != "" {
			out = append(out, filepath.Join(append([]string{base}, rel...)...))
		}
	}
	for _, d := range dirs {
		under(home, d)
	}
	if goos == "windows" {
		under(home, ".local", "bin")
		under(env("APPDATA"), "npm")
		under(env("LOCALAPPDATA"), "pnpm")
		under(env("LOCALAPPDATA"), "Volta", "bin")
		under(home, ".bun", "bin")
		under(home, "scoop", "shims")
		under(env("ProgramData"), "chocolatey", "bin")
		under(env("LOCALAPPDATA"), "Microsoft", "WinGet", "Links")
		under(env("ProgramFiles"), "nodejs")
		under(home, ".cargo", "bin")
		return out
	}
	under(home, ".local", "bin")
	if goos == "darwin" {
		out = append(out, "/opt/homebrew/bin")
	}
	out = append(out, "/usr/local/bin")
	if goos == "linux" {
		out = append(out, "/home/linuxbrew/.linuxbrew/bin", "/snap/bin")
		under(home, ".linuxbrew", "bin")
	}
	under(home, ".bun", "bin")
	under(home, ".volta", "bin")
	under(home, ".npm-global", "bin")
	under(cmp.Or(env("PNPM_HOME"), pnpmHome(goos, home)))
	under(home, ".cargo", "bin")
	// nvm's newest Node.js, and fnm's default.
	versions, _ := filepath.Glob(filepath.Join(cmp.Or(env("NVM_DIR"), filepath.Join(home, ".nvm")), "versions", "node", "*", "bin"))
	slices.SortFunc(versions, func(a, b string) int { return slices.Compare(nodeVersion(b), nodeVersion(a)) })
	out = append(out, versions...)
	under(fnmDir(goos, home, env), "aliases", "default", "bin")
	return out
}

// pnpmHome is where pnpm puts the programs it installs globally.
func pnpmHome(goos, home string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "pnpm")
	}
	return filepath.Join(home, ".local", "share", "pnpm")
}

// fnmDir is fnm's folder.
func fnmDir(goos, home string, env func(string) string) string {
	if dir := env("FNM_DIR"); dir != "" {
		return dir
	}
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "fnm")
	}
	return filepath.Join(cmp.Or(env("XDG_DATA_HOME"), filepath.Join(home, ".local", "share")), "fnm")
}

// nodeVersion is the version of an nvm folder's Node.js, v22.1.0's
// [22 1 0].
func nodeVersion(bin string) []int {
	var v []int
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Base(filepath.Dir(bin)), "v"), ".") {
		n, _ := strconv.Atoi(part)
		v = append(v, n)
	}
	return v
}

// programFiles are the file names program may have on goos: on Windows,
// with each extension PATHEXT runs, as npm's shims are .cmd files.
func programFiles(goos, program string, env func(string) string) []string {
	if goos != "windows" || filepath.Ext(program) != "" {
		return []string{program}
	}
	var out []string
	for ext := range strings.SplitSeq(strings.ToLower(cmp.Or(env("PATHEXT"), windowsExts)), ";") {
		if ext != "" {
			out = append(out, program+ext)
		}
	}
	return out
}

// windowsExts are the extensions Windows runs when PATHEXT does not say.
const windowsExts = ".com;.exe;.bat;.cmd"

// runnable reports whether path is a file this process may run.
func runnable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode()&0o111 != 0
}

// ProgramName is a program's name without its folder and, on Windows, the
// extension that runs it: claude for C:\…\npm\claude.cmd.
func ProgramName(program string) string {
	base := filepath.Base(program)
	if ext := strings.ToLower(filepath.Ext(base)); ext != "" && slices.Contains(strings.Split(windowsExts+";.ps1", ";"), ext) {
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return base
}
