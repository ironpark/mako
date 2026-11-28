package sshremote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/safeexec"
)

// SessionLaunch describes a direct-SSH session to launch on a remote host.
type SessionLaunch struct {
	Host        config.SSHHost
	ControlPath string
	RemoteHome  string
	RemoteShell string
	Agent       string
	Profile     string
	Project     string
	Args        []string
	Env         map[string]string
	Model       string
	Effort      string
	Yolo        *bool
	// LocalAgents is the local config's agent definitions, used to inherit
	// named profile args/model/effort/yolo when running in direct SSH mode.
	LocalAgents map[string]agentspec.Agent
}

// BuildSessionCommand builds the local `ssh -tt ...` argv and a human-readable
// Shown argv for launching an agent or shell on a remote SSH host.
func BuildSessionCommand(spec SessionLaunch) (cmd []string, shown []string, err error) {
	base, err := BaseArgs(spec.Host, Options{
		ControlPath: spec.ControlPath,
		Interactive: true,
		ForceTTY:    true,
	})
	if err != nil {
		return nil, nil, err
	}

	remoteArgs, err := resolveRemoteAgentCommand(spec)
	if err != nil {
		return nil, nil, err
	}

	var script strings.Builder
	script.WriteString(remotePathPrelude)
	script.WriteString("; ")
	script.WriteString("export TERM=xterm-256color COLORTERM=truecolor TERM_PROGRAM=ghostty; ")

	// Export any explicit environment overrides in deterministic order.
	if len(spec.Env) > 0 {
		keys := make([]string, 0, len(spec.Env))
		for k := range spec.Env {
			if validEnvName(k) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&script, "export %s=%s; ", k, ShellQuote(spec.Env[k]))
		}
	}

	// Change into the target project directory (defaulting to host DefaultDir or $HOME).
	dir := strings.TrimSpace(spec.Project)
	if dir == "" {
		dir = strings.TrimSpace(spec.Host.DefaultDir)
	}
	if dir != "" && dir != "~" {
		if strings.HasPrefix(dir, "~/") {
			fmt.Fprintf(&script, "cd -- \"$HOME\"/%s && ", ShellQuote(strings.TrimLeft(dir[2:], "/")))
		} else {
			fmt.Fprintf(&script, "cd -- %s && ", ShellQuote(dir))
		}
	} else {
		script.WriteString("cd -- \"$HOME\" && ")
	}

	if spec.Agent == "" || spec.Agent == agentspec.ShellName {
		if len(remoteArgs) > 0 {
			fmt.Fprintf(&script, "exec %s", ShellJoin(remoteArgs))
		} else {
			script.WriteString(`exec "${SHELL:-/bin/sh}" -l`)
		}
	} else {
		fmt.Fprintf(&script, "exec %s", ShellJoin(remoteArgs))
	}

	argv := append([]string{SSHBinary}, base...)
	argv = append(argv, "sh -c "+ShellQuote(script.String()))

	shown = append([]string{"ssh", Target(spec.Host)}, remoteArgs...)
	if len(remoteArgs) == 0 {
		shown = append(shown, "${SHELL}")
	}
	return argv, shown, nil
}

func resolveRemoteAgentCommand(spec SessionLaunch) ([]string, error) {
	if spec.Agent == "" || spec.Agent == agentspec.ShellName {
		if len(spec.Args) > 0 {
			shell := spec.RemoteShell
			if shell == "" {
				shell = "/bin/sh"
			}
			return append([]string{shell}, spec.Args...), nil
		}
		return nil, nil
	}

	// Inherit profile/model/effort/yolo resolution from agentspec using the
	// standard remote binary name for built-in agents so local macOS paths
	// don't break Linux remote hosts.
	agentCfg := spec.LocalAgents
	launch, err := agentspec.ResolveWith(spec.Agent, spec.Profile, spec.Args, nil, agentspec.ResolveOptions{
		Model:  spec.Model,
		Effort: spec.Effort,
		Yolo:   spec.Yolo,
	}, agentCfg)
	if err != nil {
		// Fall back to invoking the agent binary name directly on the remote host.
		return append([]string{spec.Agent}, spec.Args...), nil
	}
	cmd := slices.Clone(launch.Command)
	if len(cmd) == 0 {
		return []string{spec.Agent}, nil
	}
	// If local config pointed at an absolute local path for a built-in agent,
	// use the bare command name on the remote host.
	if strings.ContainsAny(cmd[0], `/\`) {
		if _, isBuiltin := agentspec.Builtin[spec.Agent]; isBuiltin {
			cmd[0] = spec.Agent
		}
	}
	return cmd, nil
}

func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// runSSH executes a POSIX shell script on h over the multiplexed SSH control
// socket and returns stdout.
func runSSH(ctx context.Context, h config.SSHHost, controlPath, script string) ([]byte, error) {
	return runSSHStdin(ctx, h, controlPath, script, nil)
}

func runSSHStdin(ctx context.Context, h config.SSHHost, controlPath, script string, stdin io.Reader) ([]byte, error) {
	args, err := BaseArgs(h, Options{ControlPath: controlPath})
	if err != nil {
		return nil, err
	}
	args = append(args, "sh -c "+ShellQuote(script))
	cmd := exec.CommandContext(ctx, SSHBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if err := safeexec.Run(cmd); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s", CleanSSHError(stderr.String(), err))
	}
	return stdout.Bytes(), nil
}

// AddWorktree creates a new git worktree on the remote SSH host beside repoDir
// and returns the repository root and created worktree path.
func AddWorktree(ctx context.Context, h config.SSHHost, controlPath, repoDir, branch, startPoint string) (repoRoot, wtPath string, err error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", "", fmt.Errorf("worktree branch name is required")
	}
	if strings.HasPrefix(branch, "-") {
		return "", "", fmt.Errorf("invalid worktree branch name %q", branch)
	}
	startPoint = strings.TrimSpace(startPoint)
	if strings.HasPrefix(startPoint, "-") {
		return "", "", fmt.Errorf("invalid worktree start point %q", startPoint)
	}
	safeName := sanitizeWorktreeSlug(branch)
	var b strings.Builder
	b.WriteString(remotePathPrelude)
	b.WriteString("; set -e; ")
	fmt.Fprintf(&b, "_dir=%s; ", ShellQuote(repoDir))
	b.WriteString(`_root="$(git -C "$_dir" rev-parse --show-toplevel)"; `)
	b.WriteString(`_parent="$(dirname "$_root")"; _base="$(basename "$_root")"; `)
	fmt.Fprintf(&b, `_wt="$_parent/${_base}-"%s; `, ShellQuote(safeName))
	if startPoint != "" {
		fmt.Fprintf(&b, `git -C "$_root" worktree add -b %s "$_wt" %s >/dev/null; `, ShellQuote(branch), ShellQuote(startPoint))
	} else {
		fmt.Fprintf(&b, `git -C "$_root" worktree add -b %s "$_wt" >/dev/null; `, ShellQuote(branch))
	}
	b.WriteString(`printf 'ROOT=%s\nPATH=%s\n' "$_root" "$_wt"`)

	out, err := runSSH(ctx, h, controlPath, b.String())
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ROOT="); ok {
			repoRoot = v
		} else if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PATH="); ok {
			wtPath = v
		}
	}
	if wtPath == "" {
		return "", "", fmt.Errorf("failed to create remote worktree")
	}
	return repoRoot, wtPath, nil
}

// EnsureWorktree prepares req.Worktree on a direct SSH host when requested.
func EnsureWorktree(ctx context.Context, h config.SSHHost, controlPath string, req *protocol.OpenSession) error {
	w := req.GetWorktree()
	if w == nil || w.GetPath() != "" {
		return nil
	}
	start := w.GetStart()
	if start == "" {
		start = w.GetBase()
	}
	root, wtPath, err := AddWorktree(ctx, h, controlPath, req.GetProject(), w.GetBranch(), start)
	if err != nil {
		return err
	}
	w.Repo = root
	w.Path = wtPath
	req.Project = wtPath
	return nil
}

func sanitizeWorktreeSlug(branch string) string {
	var b strings.Builder
	for _, r := range branch {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-':
			b.WriteRune(r)
		case r == '.':
			// Avoid ".." path traversal segments.
			if b.Len() > 0 && b.String()[b.Len()-1] == '.' {
				b.WriteByte('-')
			} else {
				b.WriteRune(r)
			}
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), ".-")
	if s == "" {
		return "worktree"
	}
	return s
}
