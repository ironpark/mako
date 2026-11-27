// Package codex is mako's driver for Codex: its options, its -c overrides
// for MCP servers and trusted hooks, and its rollout logs.
package codex

import (
	"encoding/json"
	"image/color"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// Agent is Codex.
type Agent struct {
	agent.Flags
	agent.CommonHooks
}

// New is the Codex driver.
func New() *Agent {
	return &Agent{
		ResumeFlags: []string{"resume"}, ForkFlag: "--fork",
		PickerFlags: []string{"--last", "--all"},
		Inputs:      []string{"--image", "-i"},
		Values:      strings.Fields("--model -m --config -c --profile -p --sandbox -s --ask-for-approval -a --cd -C --add-dir --enable --disable --local-provider --remote --remote-auth-token-env"),
		IsolateFlag: "--no-daemon"}
}

func (*Agent) Info() agent.Info {
	return agent.Info{
		Name: "codex", Programs: []string{"codex"}, Command: []string{"codex"},
		// Its standalone installer's.
		Dirs: []string{filepath.Join(".codex", "packages", "standalone", "current", "bin")},
		Icon: "agent-codex", Color: color.NRGBA{0x7b, 0x6c, 0xf0, 0xff},
	}
}

// EffortArgs starts Codex with model_reasoning_effort overridden to effort.
func (*Agent) EffortArgs(effort string) []string {
	return []string{"-c", "model_reasoning_effort=" + jsonString(effort)}
}

// WithServers gives the servers as -c overrides of Codex's mcp_servers
// table, each value TOML, which a JSON string or array of strings also is.
// A server at a URL is given its token in Codex's environment, which only
// the user's processes read, rather than on its command line, which any
// may list.
func (*Agent) WithServers(l *agent.Launch, servers []agent.Server, _, _ string) (func(), error) {
	flags := []string{l.Command[0]}
	hasLoopbackHTTP := false
	for _, s := range servers {
		key := "mcp_servers." + tomlKey(s.ID)
		if s.URL != "" {
			flags = append(flags, "-c", key+".url="+jsonString(s.URL))
			if s.BearerToken != "" {
				env := tokenEnv(s.ID)
				l.SetEnv(env, s.BearerToken)
				flags = append(flags, "-c", key+".bearer_token_env_var="+jsonString(env))
			}
			if agent.IsLoopbackURL(s.URL) {
				hasLoopbackHTTP = true
			}
			continue
		}
		if s.Command == "" {
			continue
		}
		flags = append(flags, "-c", key+".command="+jsonString(s.Command))
		flags = append(flags, "-c", key+".args="+jsonString(s.ArgList()))
		if len(s.Env) > 0 {
			var kv []string
			for _, k := range slices.Sorted(maps.Keys(s.Env)) {
				kv = append(kv, jsonString(k)+"="+jsonString(s.Env[k]))
			}
			flags = append(flags, "-c", key+".env={"+strings.Join(kv, ",")+"}")
		}
	}
	if hasLoopbackHTTP {
		agent.EnsureLocalProxyBypass(l)
	}
	l.Command = append(flags, l.Command[1:]...)
	return func() {}, nil
}

// tokenEnv is the variable that holds the bearer token of server id.
func tokenEnv(id string) string {
	name := []byte("MAKO_MCP_TOKEN_")
	for _, c := range strings.ToUpper(id) {
		if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			name = append(name, byte(c))
		} else {
			name = append(name, '_')
		}
	}
	return string(name)
}

// tomlKey is id as a TOML key, bare when it can be.
func tomlKey(id string) string {
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return jsonString(id)
		}
	}
	return id
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// RemoteLaunchSplit partitions a resolved Codex CLI command line into arguments
// for a managed `codex app-server` process and arguments for the `codex --remote`
// TUI process connecting to it.
type RemoteLaunchSplit struct {
	Program        string
	ServerArgs     []string // -c, --enable, --disable, --strict-config, plus writable_roots from --add-dir
	TUIArgs        []string // TUI-safe flags for both new and resumed `codex --remote` sessions
	NewTUIArgs     []string // permission/sandbox flags for brand-new `codex --remote` sessions (rejected by `resume`)
	Model          string   // -m / --model (forwarded to thread/start & TUI)
	ApprovalPolicy string   // -a / --ask-for-approval, or "never" for YOLO
	Sandbox        string   // -s / --sandbox, or "danger-full-access" for YOLO
	Cwd            string   // -C / --cd if explicitly supplied
	ResumeID       string   // thread ID from `resume <id>` if present
	Prompt         string   // optional positional prompt
	HasPrompt      bool
	SkipRemote     bool // true for non-TUI subcommands, interactive pickers, or explicit --remote
}

// nonRemoteSubcommands are Codex subcommands that are not interactive TUI sessions.
var nonRemoteSubcommands = map[string]bool{
	"agents": true, "exec": true, "e": true, "review": true, "login": true, "logout": true,
	"mcp": true, "plugin": true, "app-server": true, "remote-control": true, "app": true,
	"completion": true, "update": true, "doctor": true, "sandbox": true, "debug": true,
	"apply": true, "a": true, "queue": true, "archive": true, "delete": true,
	"migrate-rollouts": true, "migrate-nfs-db": true, "unarchive": true, "fork": true,
	"cloud": true, "exec-server": true, "features": true, "help": true,
}

// SplitRemoteLaunch splits a resolved Codex command line between `codex app-server`
// and `codex --remote`, stripping `--no-daemon` (rejected by `--remote`),
// translating `--add-dir` into `-c sandbox_workspace_write.writable_roots=[...]`
// on the server, and moving `-c`/`--config`/`--enable`/`--disable`/`--strict-config`
// to the server so MCP servers, hooks, and config overrides apply where turns execute.
func SplitRemoteLaunch(command []string) RemoteLaunchSplit {
	var out RemoteLaunchSplit
	if len(command) == 0 {
		out.SkipRemote = true
		return out
	}
	out.Program = command[0]

	var addDirs []string
	var approvalsReviewer string
	var sawResume bool
	var positional []string
	var afterDoubleDash bool

	for i := 1; i < len(command); i++ {
		arg := command[i]
		if afterDoubleDash {
			positional = append(positional, arg)
			continue
		}
		if arg == "--" {
			afterDoubleDash = true
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			if !sawResume && len(positional) == 0 {
				if arg == "resume" {
					sawResume = true
					continue
				}
				if nonRemoteSubcommands[arg] {
					out.SkipRemote = true
					return out
				}
			}
			positional = append(positional, arg)
			continue
		}

		name, val, hasEq := strings.Cut(arg, "=")
		nextVal := func() (string, bool) {
			if hasEq {
				return val, true
			}
			if i+1 < len(command) {
				i++
				return command[i], true
			}
			return "", false
		}

		switch name {
		case "--remote", "--remote-auth-token-env", "--fork", "--last", "--all", "--worktree", "-h", "--help", "-V", "--version":
			out.SkipRemote = true
			return out
		case "--no-daemon":
			// Rejected by `codex --remote`.
			continue
		case "-c", "--config":
			if v, ok := nextVal(); ok {
				out.ServerArgs = append(out.ServerArgs, "-c", v)
			}
		case "--enable", "--disable":
			if v, ok := nextVal(); ok {
				out.ServerArgs = append(out.ServerArgs, name, v)
			}
		case "--strict-config":
			out.ServerArgs = append(out.ServerArgs, name)
		case "--add-dir":
			if v, ok := nextVal(); ok && v != "" {
				addDirs = append(addDirs, v)
			}
		case "-m", "--model":
			if v, ok := nextVal(); ok {
				out.Model = v
				out.TUIArgs = append(out.TUIArgs, "--model", v)
			}
		case "-a", "--ask-for-approval":
			// Permission overrides are rejected by `codex --remote ... resume`;
			// apply them on the managed app-server and thread RPCs instead.
			if v, ok := nextVal(); ok {
				out.ApprovalPolicy = v
			}
		case "-s", "--sandbox":
			if v, ok := nextVal(); ok {
				out.Sandbox = v
			}
		case "-C", "--cd":
			if v, ok := nextVal(); ok {
				out.Cwd = v
				out.TUIArgs = append(out.TUIArgs, "-C", v)
			}
		case "--approve-for-me":
			approvalsReviewer = "guardian_subagent"
			if out.Sandbox == "" {
				out.Sandbox = "workspace-write"
			}
		case "--full-auto":
			if out.ApprovalPolicy == "" {
				out.ApprovalPolicy = "on-request"
			}
			if out.Sandbox == "" {
				out.Sandbox = "workspace-write"
			}
		case "--dangerously-bypass-approvals-and-sandbox", "--yolo":
			out.ApprovalPolicy = "never"
			out.Sandbox = "danger-full-access"
			approvalsReviewer = ""
		case "-p", "--profile", "--local-provider", "-i", "--image":
			if v, ok := nextVal(); ok {
				out.TUIArgs = append(out.TUIArgs, name, v)
			}
		default:
			out.TUIArgs = append(out.TUIArgs, arg)
		}
	}

	if out.ApprovalPolicy != "" {
		out.ServerArgs = append(out.ServerArgs, "-c", "approval_policy="+tomlString(out.ApprovalPolicy))
	}
	if out.Sandbox != "" {
		out.ServerArgs = append(out.ServerArgs, "-c", "sandbox_mode="+tomlString(out.Sandbox))
	}
	if approvalsReviewer != "" {
		out.ServerArgs = append(out.ServerArgs, "-c", "approvals_reviewer="+tomlString(approvalsReviewer))
	}
	// Brand-new `codex --remote` sessions send an explicit `sandbox` field in
	// `thread/start` from the TUI's own CLI/local config (defaulting to
	// "read-only" in untrusted or isolated homes), overriding the server's
	// `-c sandbox_mode=...`. Forward permission/sandbox flags to brand-new TUI
	// launches via NewTUIArgs, while keeping them off TUIArgs because
	// `codex --remote ... resume` rejects permission overrides.
	switch {
	case approvalsReviewer == "guardian_subagent":
		out.NewTUIArgs = append(out.NewTUIArgs, "--approve-for-me")
		if out.ApprovalPolicy != "" {
			out.NewTUIArgs = append(out.NewTUIArgs, "-a", out.ApprovalPolicy)
		}
		if out.Sandbox != "" && out.Sandbox != "workspace-write" {
			out.NewTUIArgs = append(out.NewTUIArgs, "-s", out.Sandbox)
		}
	case out.ApprovalPolicy == "never" && out.Sandbox == "danger-full-access":
		out.NewTUIArgs = append(out.NewTUIArgs, "--dangerously-bypass-approvals-and-sandbox")
	default:
		if out.ApprovalPolicy != "" {
			out.NewTUIArgs = append(out.NewTUIArgs, "-a", out.ApprovalPolicy)
		}
		if out.Sandbox != "" {
			out.NewTUIArgs = append(out.NewTUIArgs, "-s", out.Sandbox)
		}
	}
	if len(addDirs) > 0 {
		out.ServerArgs = append(out.ServerArgs, "-c", "sandbox_workspace_write.writable_roots="+jsonString(addDirs))
	}

	if sawResume {
		if len(positional) == 0 || !agent.ValidID.MatchString(positional[0]) {
			// Bare `codex resume` picker or non-UUID name: let direct TUI handle it.
			out.SkipRemote = true
			return out
		}
		out.ResumeID = positional[0]
		if len(positional) > 1 {
			out.Prompt = positional[1]
			out.HasPrompt = true
		}
	} else if len(positional) > 0 {
		out.Prompt = positional[0]
		out.HasPrompt = true
	}

	return out
}
