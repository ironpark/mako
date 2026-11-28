package daemon

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
)

// runAgentMsg implements the `mako-msg` CLI for background message waiting and
// messaging from any agent's shell (e.g., Claude Code Bash(run_in_background)).
func runAgentMsg(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: mako-msg <wait|read|send|list|profiles|inspect> [flags]")
		return 2
	}
	socket := args[0]
	sub := args[1]
	rest := args[2:]

	sessionID := os.Getenv("AGENT_MAKO_SESSION")
	if sessionID == "" {
		sessionID = os.Getenv("MAKO_SESSION")
	}
	token := os.Getenv(agentMCPTokenEnv)
	if sessionID == "" || token == "" {
		fmt.Fprintln(stderr, "mako-msg: missing AGENT_MAKO_SESSION / MAKO_AGENT_MCP_TOKEN in environment")
		return 1
	}

	req := agentCLIRequest{Session: sessionID, Token: token, Op: sub}
	switch sub {
	case "wait":
		fs := flag.NewFlagSet("mako-msg wait", flag.ContinueOnError)
		fs.SetOutput(stderr)
		after := fs.Uint64("after", 0, "only return messages with id > after")
		from := fs.String("from", "", "only wake on messages from this session ID")
		watch := fs.String("session", "", "also wake when these child session ID(s) finish, wait, or exit (comma-separated)")
		allChildren := fs.Bool("all-children", false, "also wake when any running child session spawned by this session finishes, waits, or exits")
		timeout := fs.Int("timeout", 600, "timeout in seconds (0 waits indefinitely)")
		noAck := fs.Bool("no-ack", false, "do not acknowledge returned messages")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		req.AfterID = *after
		req.From = strings.TrimSpace(*from)
		for _, part := range strings.Split(*watch, ",") {
			if id := strings.TrimSpace(part); id != "" {
				req.WatchIDs = append(req.WatchIDs, id)
			}
		}
		req.WatchChildren = *allChildren
		req.TimeoutSec = *timeout
		req.NoTimeout = *timeout == 0
		req.Ack = !*noAck

	case "read":
		fs := flag.NewFlagSet("mako-msg read", flag.ContinueOnError)
		fs.SetOutput(stderr)
		after := fs.Uint64("after", 0, "return messages with id > after")
		ack := fs.Uint64("ack", 0, "acknowledge messages through this ID")
		limit := fs.Int("limit", 50, "maximum messages to return")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		req.AfterID = *after
		req.AckThrough = *ack
		req.Limit = *limit

	case "send":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "usage: mako-msg send <to-session-id> <text...>")
			return 2
		}
		req.To = rest[0]
		if len(rest) == 1 || (len(rest) == 2 && rest[1] == "-") {
			b, err := io.ReadAll(io.LimitReader(stdin, maxAgentMessage+1))
			if err != nil {
				fmt.Fprintln(stderr, "mako-msg send:", err)
				return 1
			}
			req.Text = string(b)
		} else {
			req.Text = strings.Join(rest[1:], " ")
		}

	case "list":
		// no extra args required

	case "inspect":
		fs := flag.NewFlagSet("mako-msg inspect", flag.ContinueOnError)
		fs.SetOutput(stderr)
		turns := fs.Int("turns", readAgentTurns, fmt.Sprintf("latest conversation turns to show, 0 to %d (0 shows the session alone)", readAgentMaxTurns))
		pos, err := parseInterleavedAgentFlags(fs, rest)
		if err != nil {
			return 2
		}
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "usage: mako-msg inspect [--turns <n>] <session-id>")
			return 2
		}
		req.Inspect = pos[0]
		req.Turns = turns

	case "profiles":
		fs := flag.NewFlagSet("mako-msg profiles", flag.ContinueOnError)
		fs.SetOutput(stderr)
		node := fs.String("node", "", "inspect spawnable profiles on another node of this project")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		req.Node = strings.TrimSpace(*node)

	default:
		fmt.Fprintf(stderr, "mako-msg: unknown subcommand %q (expected wait, read, send, list, profiles, inspect)\n", sub)
		return 2
	}

	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "mako-msg:", err)
		return 1
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		fmt.Fprintln(stderr, "mako-msg:", err)
		return 1
	}
	var resp agentCLIResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		fmt.Fprintln(stderr, "mako-msg:", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintln(stderr, "mako-msg:", resp.Error)
		return 1
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	_, _ = stdout.Write(append(out, '\n'))
	return 0
}

// toolUsableInScope reports whether a contributed tool declaring scope can be
// invoked by a session whose resolved ScopeContext is sc.
func toolUsableInScope(scope string, sc plugin.ScopeContext) bool {
	switch scope {
	case "", protocol.ScopeGlobal:
		return true
	case protocol.ScopeSession:
		return sc.SessionID != ""
	case protocol.ScopeProject:
		return sc.ProjectID != ""
	case protocol.ScopeSpace:
		return sc.SpaceID != ""
	default:
		return false
	}
}

// pluginEnvFilter parses AGENT_MAKO_PLUGINS (or MAKO_PLUGINS) into a predicate
// selecting which plugin tools are exposed to this shell environment:
//   - unset / empty: all tools usable in the session's scope
//   - "none" / "-": no plugin tools
//   - comma-separated entries of "<plugin>" or "<plugin>:<tool>"
func pluginEnvFilter() func(pluginID, toolID string) bool {
	raw, ok := os.LookupEnv("AGENT_MAKO_PLUGINS")
	if !ok {
		raw, ok = os.LookupEnv("MAKO_PLUGINS")
	}
	if !ok {
		return func(string, string) bool { return true }
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return func(string, string) bool { return true }
	}
	if strings.EqualFold(trimmed, "none") || trimmed == "-" {
		return func(string, string) bool { return false }
	}
	allowedPlugins := map[string]bool{}
	allowedTools := map[string]bool{}
	for _, entry := range strings.Split(trimmed, ",") {
		e := strings.TrimSpace(entry)
		if e == "" {
			continue
		}
		if strings.Contains(e, ":") {
			allowedTools[e] = true
		} else {
			allowedPlugins[e] = true
		}
	}
	return func(pluginID, toolID string) bool {
		return allowedPlugins[pluginID] || allowedTools[pluginID+":"+toolID]
	}
}

func filterToolsByEnv(tools []plugin.ContributedTool, allow func(pluginID, toolID string) bool) []plugin.ContributedTool {
	var out []plugin.ContributedTool
	for _, tl := range tools {
		if allow(tl.PluginID, tl.ID) {
			out = append(out, tl)
		}
	}
	return out
}

func dialAgentCLI(socket string, req agentCLIRequest) (agentCLIResponse, error) {
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return agentCLIResponse{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return agentCLIResponse{}, err
	}
	var resp agentCLIResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return agentCLIResponse{}, err
	}
	return resp, nil
}

// runAgentPlugin implements the `mako-plugin` CLI available in every agent
// session's PATH, letting agents discover and invoke plugin tools directly
// from the shell using the session's authenticated project/space/session scope.
func runAgentPlugin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	const helpText = `usage:
  mako-plugin list [--json] [plugin-id]
  mako-plugin help <plugin-id> [tool-id]
  mako-plugin call <plugin-id> <tool-id> [--json <json|->] [--<param> <value|-> ...]
`
	if len(args) < 2 {
		fmt.Fprint(stderr, helpText)
		return 2
	}
	socket := args[0]
	sub := args[1]
	rest := args[2:]

	if sub == "-h" || sub == "--help" || (sub == "help" && len(rest) == 0) {
		fmt.Fprint(stdout, helpText)
		return 0
	}

	sessionID := os.Getenv("AGENT_MAKO_SESSION")
	if sessionID == "" {
		sessionID = os.Getenv("MAKO_SESSION")
	}
	token := os.Getenv(agentMCPTokenEnv)
	if sessionID == "" || token == "" {
		fmt.Fprintln(stderr, "mako-plugin: missing AGENT_MAKO_SESSION / MAKO_AGENT_MCP_TOKEN in environment")
		return 1
	}
	allowEnv := pluginEnvFilter()

	switch sub {
	case "list":
		asJSON := false
		pluginFilter := ""
		for _, a := range rest {
			switch a {
			case "--json", "-json":
				asJSON = true
			default:
				if strings.HasPrefix(a, "-") {
					fmt.Fprintf(stderr, "mako-plugin list: unknown flag %q\n", a)
					return 2
				}
				pluginFilter = a
			}
		}
		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session:  sessionID,
			Token:    token,
			Op:       "plugin_list",
			PluginID: pluginFilter,
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-plugin:", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(stderr, "mako-plugin:", resp.Error)
			return 1
		}
		visible := filterToolsByEnv(resp.Tools, allowEnv)
		if asJSON {
			if visible == nil {
				visible = []plugin.ContributedTool{}
			}
			out, _ := json.MarshalIndent(visible, "", "  ")
			_, _ = stdout.Write(append(out, '\n'))
			return 0
		}
		if len(visible) == 0 {
			fmt.Fprintln(stdout, "No plugin tools available.")
			return 0
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PLUGIN\tTOOL\tSCOPE\tDESCRIPTION")
		for _, tl := range visible {
			scope := tl.Scope
			if scope == "" {
				scope = "global"
			}
			desc := strings.TrimPrefix(tl.Description, fmt.Sprintf("[%s plugin] ", tl.PluginID))
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", tl.PluginID, tl.ID, scope, desc)
		}
		_ = w.Flush()
		return 0

	case "help":
		pluginID := rest[0]
		toolFilter := ""
		if len(rest) > 1 {
			toolFilter = rest[1]
		}
		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session:  sessionID,
			Token:    token,
			Op:       "plugin_list",
			PluginID: pluginID,
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-plugin:", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(stderr, "mako-plugin:", resp.Error)
			return 1
		}
		matched := 0
		for _, tl := range filterToolsByEnv(resp.Tools, allowEnv) {
			if toolFilter != "" && tl.ID != toolFilter {
				continue
			}
			matched++
			scope := tl.Scope
			if scope == "" {
				scope = "global"
			}
			desc := strings.TrimPrefix(tl.Description, fmt.Sprintf("[%s plugin] ", tl.PluginID))
			fmt.Fprintf(stdout, "%s %s (%s)\n  %s\n", tl.PluginID, tl.ID, scope, desc)
			props, _ := tl.Schema["properties"].(map[string]any)
			reqSet := map[string]bool{}
			if reqList, ok := tl.Schema["required"].([]any); ok {
				for _, r := range reqList {
					if s, ok := r.(string); ok {
						reqSet[s] = true
					}
				}
			}
			names := make([]string, 0, len(props))
			for k := range props {
				names = append(names, k)
			}
			sort.Strings(names)
			if len(names) == 0 {
				fmt.Fprintln(stdout, "  (no parameters)")
			} else {
				for _, name := range names {
					pMap, _ := props[name].(map[string]any)
					typ, _ := pMap["type"].(string)
					pDesc, _ := pMap["description"].(string)
					reqTag := ""
					if reqSet[name] {
						reqTag = ", required"
					}
					if pDesc != "" {
						fmt.Fprintf(stdout, "  --%s <%s%s>  %s\n", name, typ, reqTag, pDesc)
					} else {
						fmt.Fprintf(stdout, "  --%s <%s%s>\n", name, typ, reqTag)
					}
				}
			}
			fmt.Fprintln(stdout)
		}
		if matched == 0 {
			fmt.Fprintf(stderr, "mako-plugin: no matching tool for %s %s\n", pluginID, toolFilter)
			return 1
		}
		return 0

	case "call":
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "usage: mako-plugin call <plugin-id> <tool-id> [--json <json|->] [--<param> <value|-> ...]")
			return 2
		}
		pluginID, toolID := rest[0], rest[1]
		if !allowEnv(pluginID, toolID) {
			fmt.Fprintf(stderr, "mako-plugin: tool %s %s is not enabled in this environment\n", pluginID, toolID)
			return 1
		}
		argTokens := rest[2:]

		var argsJSON json.RawMessage
		if len(argTokens) == 0 {
			argsJSON = json.RawMessage(`{}`)
		} else if len(argTokens) >= 1 && (argTokens[0] == "--json" || strings.HasPrefix(argTokens[0], "--json=")) {
			var raw string
			if v, ok := strings.CutPrefix(argTokens[0], "--json="); ok {
				raw = v
			} else {
				if len(argTokens) < 2 {
					fmt.Fprintln(stderr, "mako-plugin call: --json requires a JSON payload or '-' for stdin")
					return 2
				}
				raw = argTokens[1]
			}
			if raw == "-" {
				b, err := io.ReadAll(io.LimitReader(stdin, 3<<20))
				if err != nil {
					fmt.Fprintln(stderr, "mako-plugin call:", err)
					return 1
				}
				raw = strings.TrimSpace(string(b))
			}
			if !json.Valid([]byte(raw)) {
				fmt.Fprintln(stderr, "mako-plugin call: invalid JSON argument")
				return 2
			}
			argsJSON = json.RawMessage(raw)
		} else {
			// Fetch schema once so flag values can be coerced to their declared JSON types.
			var paramTypes map[string]string
			if listResp, err := dialAgentCLI(socket, agentCLIRequest{
				Session:  sessionID,
				Token:    token,
				Op:       "plugin_list",
				PluginID: pluginID,
			}); err == nil && listResp.OK {
				for _, tl := range listResp.Tools {
					if tl.ID == toolID {
						if props, ok := tl.Schema["properties"].(map[string]any); ok {
							paramTypes = make(map[string]string, len(props))
							for k, v := range props {
								if m, ok := v.(map[string]any); ok {
									if t, ok := m["type"].(string); ok {
										paramTypes[k] = t
									}
								}
							}
						}
						break
					}
				}
			}
			parsed, err := parsePluginCallFlags(argTokens, paramTypes, stdin)
			if err != nil {
				fmt.Fprintln(stderr, "mako-plugin call:", err)
				return 2
			}
			b, err := json.Marshal(parsed)
			if err != nil {
				fmt.Fprintln(stderr, "mako-plugin call:", err)
				return 1
			}
			argsJSON = b
		}

		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session:  sessionID,
			Token:    token,
			Op:       "plugin_call",
			PluginID: pluginID,
			ToolID:   toolID,
			Args:     argsJSON,
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-plugin:", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(stderr, "mako-plugin:", resp.Error)
			return 1
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, resp.Output, "", "  "); err == nil {
			_, _ = stdout.Write(append(pretty.Bytes(), '\n'))
		} else {
			_, _ = stdout.Write(append(resp.Output, '\n'))
		}
		return 0

	default:
		fmt.Fprintf(stderr, "mako-plugin: unknown subcommand %q\n%s", sub, helpText)
		return 2
	}
}

// parsePluginCallFlags parses `--key value` and `--key=value` arguments into a
// JSON-serializable map, reading stdin when a value is `-` and coercing values
// to the tool parameter's declared JSON Schema type when known.
func parsePluginCallFlags(tokens []string, paramTypes map[string]string, stdin io.Reader) (map[string]any, error) {
	out := map[string]any{}
	stdinUsed := false
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !strings.HasPrefix(tok, "--") || len(tok) <= 2 {
			return nil, fmt.Errorf("unexpected argument %q (expected --param value or --json '{...}')", tok)
		}
		body := tok[2:]
		key, val, hasEq := strings.Cut(body, "=")
		if !hasEq {
			if paramTypes[key] == "boolean" && (i+1 >= len(tokens) || strings.HasPrefix(tokens[i+1], "--")) {
				out[key] = true
				continue
			}
			if i+1 >= len(tokens) {
				return nil, fmt.Errorf("flag --%s requires a value", key)
			}
			i++
			val = tokens[i]
		}
		if val == "-" {
			if stdinUsed {
				return nil, fmt.Errorf("stdin ('-') can only be used once per call")
			}
			stdinUsed = true
			b, err := io.ReadAll(io.LimitReader(stdin, 3<<20))
			if err != nil {
				return nil, err
			}
			val = string(b)
		}
		coerced, err := coercePluginParam(val, paramTypes[key])
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", key, err)
		}
		out[key] = coerced
	}
	return out, nil
}

func coercePluginParam(raw, typ string) (any, error) {
	switch typ {
	case "boolean":
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("expected boolean, got %q", raw)
		}
		return b, nil
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected integer, got %q", raw)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, fmt.Errorf("expected number, got %q", raw)
		}
		return f, nil
	case "array", "object":
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("expected JSON %s: %w", typ, err)
		}
		return v, nil
	default:
		return raw, nil
	}
}

// runAgentProject implements the `mako-project` CLI available in every agent
// session's PATH, letting agents list projects/default storage locations and
// register local folders or clone Git URLs into Mako's workspace.
func runAgentProject(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	const helpText = `usage:
  mako-project list [--json]
  mako-project dirs [--json]
  mako-project add <path-or-git-url> [--name <name>] [--dir <base-dir>] [--folder <folder>] [--space <space>] [--json]
`
	if len(args) < 2 {
		fmt.Fprint(stderr, helpText)
		return 2
	}
	socket := args[0]
	sub := args[1]
	rest := args[2:]

	if sub == "-h" || sub == "--help" || sub == "help" {
		fmt.Fprint(stdout, helpText)
		return 0
	}

	sessionID := os.Getenv("AGENT_MAKO_SESSION")
	if sessionID == "" {
		sessionID = os.Getenv("MAKO_SESSION")
	}
	token := os.Getenv(agentMCPTokenEnv)
	if sessionID == "" || token == "" {
		fmt.Fprintln(stderr, "mako-project: missing AGENT_MAKO_SESSION / MAKO_AGENT_MCP_TOKEN in environment")
		return 1
	}

	switch sub {
	case "list":
		fs := flag.NewFlagSet("mako-project list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "output JSON")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session: sessionID,
			Token:   token,
			Op:      "project_list",
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-project:", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(stderr, "mako-project:", resp.Error)
			return 1
		}
		projs := resp.Projects
		if projs == nil {
			projs = []agentProjectSummary{}
		}
		if *asJSON {
			out, _ := json.MarshalIndent(projs, "", "  ")
			_, _ = stdout.Write(append(out, '\n'))
			return 0
		}
		if len(projs) == 0 {
			fmt.Fprintln(stdout, "No projects registered.")
			return 0
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tPATHS\tREMOTE")
		for _, p := range projs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Name, strings.Join(p.Paths, ", "), p.Remote)
		}
		_ = w.Flush()
		return 0

	case "dirs":
		fs := flag.NewFlagSet("mako-project dirs", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "output JSON")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session: sessionID,
			Token:   token,
			Op:      "project_dirs",
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-project:", err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(stderr, "mako-project:", resp.Error)
			return 1
		}
		dirs := resp.ProjectDirs
		if dirs == nil {
			dirs = []string{}
		}
		if *asJSON {
			out, _ := json.MarshalIndent(dirs, "", "  ")
			_, _ = stdout.Write(append(out, '\n'))
			return 0
		}
		if len(dirs) == 0 {
			fmt.Fprintln(stdout, "No default project storage directories configured.")
			return 0
		}
		for i, dir := range dirs {
			if i == 0 {
				fmt.Fprintf(stdout, "%s (default)\n", dir)
			} else {
				fmt.Fprintln(stdout, dir)
			}
		}
		return 0

	case "add":
		fs := flag.NewFlagSet("mako-project add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		name := fs.String("name", "", "optional project display name")
		baseDir := fs.String("dir", "", "base directory to clone into (for git URLs; defaults to first configured project_dirs)")
		folder := fs.String("folder", "", "optional subfolder name when cloning a git URL")
		space := fs.String("space", "", "optional target space ID or name")
		asJSON := fs.Bool("json", false, "output JSON")
		pos, err := parseInterleavedAgentFlags(fs, rest)
		if err != nil {
			return 2
		}
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "usage: mako-project add <path-or-git-url> [--name <name>] [--dir <base-dir>] [--folder <folder>] [--space <space>] [--json]")
			return 2
		}
		resp, err := dialAgentCLI(socket, agentCLIRequest{
			Session: sessionID,
			Token:   token,
			Op:      "project_add",
			Target:  pos[0],
			Name:    *name,
			BaseDir: *baseDir,
			Folder:  *folder,
			Space:   *space,
		})
		if err != nil {
			fmt.Fprintln(stderr, "mako-project:", err)
			return 1
		}
		if !resp.OK || resp.Added == nil {
			fmt.Fprintln(stderr, "mako-project:", resp.Error)
			return 1
		}
		if *asJSON {
			out, _ := json.MarshalIndent(resp.Added, "", "  ")
			_, _ = stdout.Write(append(out, '\n'))
			return 0
		}
		action := "Added"
		if resp.Added.Cloned {
			action = "Cloned and added"
		} else if resp.Added.AlreadyExisted {
			action = "Already registered"
		}
		fmt.Fprintf(stdout, "%s project %s (%s) at %s\n", action, resp.Added.Name, resp.Added.ID, resp.Added.Path)
		return 0

	default:
		fmt.Fprintf(stderr, "mako-project: unknown subcommand %q\n%s", sub, helpText)
		return 2
	}
}

// parseInterleavedAgentFlags allows flags before or after positional arguments.
func parseInterleavedAgentFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rem := fs.Args()
		if len(rem) == 0 {
			break
		}
		if rem[0] == "--" {
			pos = append(pos, rem[1:]...)
			break
		}
		pos = append(pos, rem[0])
		args = rem[1:]
	}
	return pos, nil
}
