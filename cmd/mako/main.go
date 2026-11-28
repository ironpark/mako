// Command mako is the front-end for the mako daemon mesh: it lists nodes,
// starts terminal agents per project, and attaches to their sessions.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/protocol"
	"rsc.io/qr"
)

const usage = `mako - manage terminal agents across your computers

Usage:
  mako nodes                        list daemons on the mesh
  mako addrs                        print this daemon's libp2p addresses, to list
                                     as another's peer or relay
  mako ls                           list agent sessions
  mako run <agent>[:<profile>] [-- args...]
                                     start an agent, or one of its profiles, and attach
  mako attach <session-id>          attach to a running session
  mako kill <session-id>            terminate a session
  mako rename <session-id> [name]   name a session (no name clears it)
  mako goal <session-id> [set <objective...>|clear]
                                     view, set, or clear a session's goal
  mako agents                       list agents and profiles this node can launch

Projects:
  mako projects [list]              list workspace projects
  mako projects add <path|git-url> [-name <name>] [-dir <base-dir>] [-folder <name>] [-space <space>]
                                     add a local folder or clone a Git URL as a project
  mako projects dirs [add <path>|remove <path>|default <path>]
                                     list or configure default project storage directories

Routines (self-hosted scheduled & event-triggered agent runs):
  mako routines [list]              list configured routines
  mako routines add -name <name> -project <dir> -agent <target> (-schedule <cron> | -trigger forge_pr_opened|forge_ci_failed) -prompt <text> [-worktree] [-branch-prefix <prefix>]
                    [-allow-external] [-max-concurrent N] [-max-per-poll N] [-cooldown 10m]
                                     add a scheduled or Forge-triggered routine; the prompt is
                                     typed into the agent as it is (//template calls are not expanded).
                                     Forge triggers run for the repository's own branches and members
                                     unless -allow-external, and give the event after the prompt as
                                     untrusted data
  mako routines remove <id|name>    remove a routine
  mako routines enable|disable <id|name>
                                     enable or disable a routine
  mako routines run <id|name>       trigger a routine immediately
  mako routines history [-limit N]  show recent routine runs

Remote SSH hosts:
  mako ssh [list]                   list configured remote SSH hosts
  mako ssh add <host> [-name <name>] [-user <user>] [-port <port>] [-key <path>] [-jump <host>] [-dir <path>] [-mode auto|daemon|direct]
                                     add a remote SSH host
  mako ssh remove <id-or-name>      remove a configured SSH host
  mako ssh enable|disable <id-or-name>
                                     enable or disable an SSH host
  mako ssh test <id-or-name|host>   test connectivity, platform, and installed agents
  mako ssh install <id-or-name|host> [-binary <path>] [-dest <remote-path>]
                                     install or update makod on a remote SSH host
  mako ssh discover                 list concrete Host aliases from ~/.ssh/config

Devices (docs/devices.md: one node's devices, used from another):
  mako devices                      list the devices attached to every node
  mako borrow <device>              borrow a device for this node (or -node's),
                                     and print the environment that reaches it,
                                     as eval "$(mako borrow pixel)" takes it
  mako return <device>              give a borrowed device back
  mako leases                       list the devices this node (or -node) borrows

Access (trust runs one way: a node you let in can drive this one):
  mako access                       this node's keys, the nodes asking to be
                                     let in, and the keys let in
  mako request <node> [note]        ask another node to let this one in
  mako allow <node> [-owner]        let in a node that asked, by its own key,
                                     or with -owner by the SSH key vouching for it
  mako deny <node>                  drop a node's request
  mako pair                         show a pairing code, a QR code mako's
                                     Android app scans: the phone that reads
                                     it is let in at once
  mako join <link>                  join the node whose pairing code's link
                                     this is
  mako trust <public key line>      let a key in, as authorized_keys writes it
  mako revoke <fingerprint>         take a key out

Repository commands (a file a repository tracks that names commands for mako
to run on its own has them run only while you trust it as it reads):
  mako repo-trust [list]            list the command files trusted
  mako repo-trust show <file>       print a command file, and whether it is trusted
  mako repo-trust allow <file>      print a command file, ask, and trust it as it reads
  mako repo-trust revoke <file|repo>
                                     forget a file, or every file of a repository

Plugins (docs/plugins.md; each a folder of plugins/ in the configuration directory):
  mako plugins                      list this node's plugins and how they stand
  mako plugins grant <id>           allow what a plugin asks for
  mako plugins deny <id>            refuse it, and stop the plugin
  mako plugins enable|disable|reload <id>
  mako plugins run <id> <command>   run a plugin's command
  mako plugins tools [id]           list tools contributed by plugins
  mako plugins set <id> <key> [value] [--scope <scope>]
                                     set a plugin's setting (no value unsets it)
  mako plugins allow-agent <id> once|always
                                     let the agent a plugin asks to start start,
                                     or every one it starts from now on
  mako plugins deny-agent <id>      refuse the agent it asks to start
  mako plugins forget-agents <id>   have it ask again before every agent
  mako plugins search [query]       search this node's registries
  mako plugins install <id> [-registry <url>]
                                     install a plugin from a registry, which must be
                                     named when several list it
  mako plugins update|remove <id>   update or remove a plugin installed from one
  mako plugins registries [add <url>|remove <url>|refresh]
                                     list or change the registries this node reads

Publishing plugins (no daemon needed; docs/plugins.md, "Publishing"):
  mako plugins keygen <key-file>    make a publisher key, its private half in key-file
  mako plugins pack <folder> -key <key-file> [-o <package>]
                                     pack and sign a plugin's folder
  mako plugins sign <package> -key <key-file> [-o <package>]
                                     sign a package again with this key
  mako plugins verify <package> [-key <public key>]
                                     check a package and say what it is
  mako plugins index <registry-folder> <package> -url <download URL>
                                     list a package in a registry's index

Flags:
  -daemon <path>   the daemon's socket (default: the configuration directory's)
  -node <id>       run on / target this node instead of the local one
  -project <dir>   working directory for a new session (default: cwd)
  -worktree        with run, start the session in a new git worktree
  -branch <name>   with run, branch name for the new worktree (implies -worktree)
  -device <dev>    with run, borrow a device for the session (repeatable,
                    or comma-separated)
`

func main() {
	daemonAddr := flag.String("daemon", "", "the daemon's socket")
	nodeID := flag.String("node", "", "target node ID")
	project := flag.String("project", "", "project directory")
	worktree := flag.Bool("worktree", false, "start run session in a new git worktree")
	branch := flag.String("branch", "", "branch name for the new worktree (implies -worktree)")
	var devices deviceFlag
	flag.Var(&devices, "device", "a device to borrow for the session")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(args, *daemonAddr, *nodeID, *project, *worktree || strings.TrimSpace(*branch) != "", strings.TrimSpace(*branch), devices); err != nil {
		fmt.Fprintln(os.Stderr, "mako:", protocol.Printable(err.Error()))
		os.Exit(1)
	}
}

func run(args []string, daemonAddr, nodeID, project string, useWorktree bool, branch string, devices []string) error {
	if args[0] == "plugins" && len(args) > 1 && publishOps[args[1]] {
		return publish(args[1], args[2:])
	}
	if args[0] == "projects" && len(args) > 1 && args[1] == "dirs" {
		return projectsDirs(args[2:])
	}
	if args[0] == "ssh" {
		return sshCmd(args[1:])
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if args[0] == "agents" {
		for _, target := range agentspec.Targets(cfg.Agents) {
			fmt.Println(target)
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	c, err := client.Dial(dialCtx, daemonAddr)
	if err != nil {
		return fmt.Errorf("cannot reach daemon (is makod running?): %w", err)
	}
	defer c.Close()

	switch args[0] {
	case "routines":
		return routinesCmd(ctx, c, nodeID, args[1:], os.Stdout)
	case "nodes":
		return listNodes(ctx, c)
	case "addrs":
		return printAddrs(c.Node())
	case "ls":
		return listSessions(ctx, c)
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("run needs an agent name")
		}
		return runAgent(ctx, c, nodeID, project, useWorktree, branch, args[1], args[2:], devices)
	case "attach":
		if len(args) < 2 {
			return fmt.Errorf("attach needs a session ID")
		}
		if nodeID, err = sessionNode(ctx, c, nodeID, args[1]); err != nil {
			return err
		}
		return c.Attach(ctx, &protocol.Attach{SessionId: args[1], NodeId: nodeID, Replay: true})
	case "kill":
		if len(args) < 2 {
			return fmt.Errorf("kill needs a session ID")
		}
		if nodeID, err = sessionNode(ctx, c, nodeID, args[1]); err != nil {
			return err
		}
		return c.Kill(ctx, &protocol.CloseSession{SessionId: args[1], NodeId: nodeID})
	case "access":
		return access(ctx, c, &protocol.Access{})
	case "request", "allow", "deny":
		if len(args) < 2 {
			return fmt.Errorf("%s needs a node", args[0])
		}
		node, err := findNode(ctx, c, args[1])
		if err != nil {
			return err
		}
		req := &protocol.Access{Op: args[0], NodeId: node}
		rest := args[2:]
		if args[0] == "allow" && len(rest) > 0 && rest[0] == "-owner" {
			req.Owner, rest = true, rest[1:]
		}
		req.Note = strings.Join(rest, " ")
		return access(ctx, c, req)
	case "pair":
		info, err := c.Access(ctx, &protocol.Access{Op: "pair"})
		if err != nil {
			return err
		}
		return printPairing(info.GetPairing())
	case "join":
		if len(args) < 2 {
			return fmt.Errorf("join needs a pairing code's link")
		}
		info, err := c.Access(ctx, &protocol.Access{Op: "join", Key: args[1], Note: strings.Join(args[2:], " ")})
		if err != nil {
			return err
		}
		fmt.Printf("asked %s with its pairing code\n", info.GetJoined())
		return nil
	case "trust":
		if len(args) < 2 {
			return fmt.Errorf("trust needs a public key")
		}
		return access(ctx, c, &protocol.Access{Op: "add", Key: strings.Join(args[1:], " ")})
	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("revoke needs a key's fingerprint")
		}
		return access(ctx, c, &protocol.Access{Op: "revoke", Key: args[1]})
	case "repo-trust":
		return repoTrust(ctx, c.RepoTrust, nodeID, args[1:], os.Stdin, os.Stdout)
	case "devices":
		return listDevices(ctx, c)
	case "borrow":
		if len(args) < 2 {
			return fmt.Errorf("borrow needs a device")
		}
		return borrow(ctx, c, nodeID, args[1])
	case "return":
		if len(args) < 2 {
			return fmt.Errorf("return needs a device")
		}
		return giveBack(ctx, c, nodeID, args[1])
	case "leases":
		return listLeases(ctx, c, nodeID)
	case "rename":
		if len(args) < 2 {
			return fmt.Errorf("rename needs a session ID")
		}
		if nodeID, err = sessionNode(ctx, c, nodeID, args[1]); err != nil {
			return err
		}
		_, err := c.Rename(ctx, &protocol.RenameSession{SessionId: args[1], NodeId: nodeID, Name: strings.Join(args[2:], " ")})
		return err
	case "goal":
		return goalCmd(ctx, c, nodeID, args[1:])
	case "plugins":
		return plugins(ctx, c, args[1:])
	case "projects":
		return projectsCmd(ctx, c, cfg, nodeID, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// sessionNode finds the node that owns sessionID when -node was not given, so
// any session shown by ls can be attached or killed by ID alone.
func sessionNode(ctx context.Context, c *client.Client, nodeID, sessionID string) (string, error) {
	if nodeID != "" {
		return nodeID, nil
	}
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range sessions {
		if s.GetId() == sessionID {
			return s.GetNodeId(), nil
		}
	}
	return "", fmt.Errorf("unknown session %q", sessionID)
}

func listNodes(ctx context.Context, c *client.Client) error {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPLATFORM\tADDRESS\tVIA\tACCESS\tLAST SEEN")
	for _, n := range nodes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", short(n.GetId()), n.GetName(), n.Platform(), n.GetAddr(), n.GetSource(), trustLabel(n, nodes), ago(protocol.TimeOf(n.GetLastSeen())))
	}
	return w.Flush()
}

// trustLabel says how the daemon and n stand, and which of nodes it
// reaches n through.
func trustLabel(n *protocol.Node, nodes []*protocol.Node) string {
	if n.Standing() == protocol.StandingVia {
		via := short(n.GetVia())
		for _, o := range nodes {
			if o.GetId() == n.GetVia() {
				via = o.GetName()
			}
		}
		return "through " + via
	}
	return map[string]string{
		protocol.StandingSelf: "-",
		protocol.StandingBoth: "both ways",
		protocol.StandingUs:   "lets us in",
		protocol.StandingThem: "let in here",
		protocol.StandingNone: "none",
	}[n.Standing()]
}

// findNode resolves a node by its ID, the start of it, or its name.
func findNode(ctx context.Context, c *client.Client, ref string) (string, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return "", err
	}
	var found []string
	for _, n := range nodes {
		if n.GetId() == ref || n.GetName() == ref {
			return n.GetId(), nil
		}
		if strings.HasPrefix(n.GetId(), ref) {
			found = append(found, n.GetId())
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	return "", fmt.Errorf("no single node %q", ref)
}

// access makes an access change, or none, and prints where things stand.
func access(ctx context.Context, c *client.Client, req *protocol.Access) error {
	info, err := c.Access(ctx, req)
	if err != nil {
		return err
	}
	if req.GetOp() == "request" {
		fmt.Println("asked; the node's own front-end answers")
		return nil
	}
	fmt.Println("This node's key (another node lets it in with this line):")
	fmt.Println("  " + info.GetKey().GetLine())
	if o := info.GetOwner(); o != nil {
		fmt.Println("Vouched for by the SSH key", o.GetFingerprint())
	}
	if len(info.GetRequests()) > 0 {
		fmt.Println("\nAsking to be let in:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  NODE\tNAME\tKEY\tSSH KEY\tNOTE")
		for _, r := range info.GetRequests() {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", short(r.GetNode().GetId()), r.GetNode().GetName(), r.GetKey().GetFingerprint(), r.GetOwner().GetFingerprint(), r.GetNote())
		}
		w.Flush()
	}
	fmt.Println("\nLet in:")
	if len(info.GetKeys()) == 0 {
		fmt.Println("  no key yet")
	}
	for _, k := range info.GetKeys() {
		if k.GetSource() != "" {
			fmt.Printf("  %s  %s  (from %s)\n", k.GetFingerprint(), k.GetComment(), k.GetSource())
			continue
		}
		fmt.Printf("  %s  %s\n", k.GetFingerprint(), k.GetComment())
	}
	return nil
}

// printPairing prints a pairing code as a QR code a camera reads off the
// terminal, two modules a character, with its link and when it runs out.
func printPairing(p *protocol.Pairing) error {
	code, err := qr.Encode(p.GetLink(), qr.M)
	if err != nil {
		return err
	}
	// Black on white whatever the terminal's theme, as cameras read it
	// best, with a quiet zone of white around it.
	const quiet = 2
	var b strings.Builder
	for y := -quiet; y < code.Size+quiet; y += 2 {
		b.WriteString("\x1b[38;5;16;48;5;231m")
		for x := -quiet; x < code.Size+quiet; x++ {
			top, bottom := code.Black(x, y), code.Black(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	fmt.Print(b.String())
	fmt.Println("\nScan it in mako's Android app: Computers ▸ Scan QR code. Or, on another node:")
	fmt.Println("  mako join '" + p.GetLink() + "'")
	fmt.Println("Valid until", protocol.TimeOf(p.GetExpires()).Local().Format("15:04"), "and for one node.")
	return nil
}

// printAddrs prints where node is on libp2p, each address ending in its
// peer ID, as a peer or relay entry of another daemon's config takes it.
func printAddrs(node *protocol.Node) error {
	if node.GetPeerId() == "" {
		return fmt.Errorf("%s does not run libp2p", node.GetName())
	}
	for _, a := range node.DialAddrs() {
		fmt.Println(a)
	}
	return nil
}

func listSessions(ctx context.Context, c *client.Client) error {
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("no sessions")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION\tNODE\tAGENT\tNAME\tPROJECT\tSTATE\tUPTIME")
	var authSessions []*protocol.SessionInfo
	for _, s := range sessions {
		name := s.GetName()
		if name == "" {
			name = "-"
		}
		state := string(s.GetState())
		if s.GetAuthError() != nil {
			state += " (auth required)"
			authSessions = append(authSessions, s)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.GetId(), short(s.GetNodeId()), agentspec.Target(s.GetAgent(), s.GetProfile()), name, s.GetProject(), state, ago(protocol.TimeOf(s.GetStartedAt())))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(authSessions) > 0 {
		fmt.Println()
		for _, s := range authSessions {
			ae := s.GetAuthError()
			fmt.Printf("auth required [%s %s]: %s\n", s.GetId(), s.GetAgent(), ae.GetSummary())
			if ae.GetGuidance() != "" {
				fmt.Printf("  %s\n", ae.GetGuidance())
			}
		}
	}
	return nil
}

func runAgent(ctx context.Context, c *client.Client, nodeID, project string, useWorktree bool, branch, target string, extra, devices []string) error {
	agent, profile := agentspec.SplitTarget(target)
	// "mako run <agent> -- args" separates agent arguments; the separator
	// itself is not one of them.
	if len(extra) > 0 && extra[0] == "--" {
		extra = extra[1:]
	}
	if project == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		project = cwd
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return err
	}
	cols, rows := client.WindowSize()

	req := &protocol.OpenSession{
		Devices: devices,
		NodeId:  nodeID,
		Agent:   agent,
		Profile: profile,
		Project: abs,
		Args:    extra,
		Cols:    uint32(cols),
		Rows:    uint32(rows),
	}
	openCtx := ctx
	if useWorktree {
		req.Worktree = &protocol.Worktree{Branch: branch}
		// The node makes the worktree first, the repository's setup_cmd
		// included, which takes longer than a node waits for another's
		// reply to a request with no deadline.
		var cancel context.CancelFunc
		openCtx, cancel = context.WithTimeout(ctx, worktreeOpenWait)
		defer cancel()
	}
	info, err := c.Open(openCtx, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[mako] session %s on %s\r\n", info.GetId(), short(info.GetNodeId()))
	fmt.Fprint(os.Stderr, skippedHooksNotice(nodeID, info.GetWorktree()))
	return c.Attach(ctx, &protocol.Attach{SessionId: info.GetId(), NodeId: nodeID, Replay: false})
}

// worktreeOpenWait is how long mako run waits for a session that starts
// in a new worktree: what making the worktree can take, and its start.
const worktreeOpenWait = gitstat.WorktreeTimeout + 30*time.Second

// skippedHooksNotice says, for a worktree made without its repository's
// setup_cmd, which file was not trusted and how to trust it; "" for any
// other.
func skippedHooksNotice(nodeID string, w *protocol.Worktree) string {
	f := protocol.Printable(w.GetSkippedHooks())
	if f == "" {
		return ""
	}
	node := ""
	if nodeID != "" {
		node = "-node " + nodeID + " "
	}
	return fmt.Sprintf("[mako] the setup_cmd of %s did not run: the file is not trusted as it reads\r\n"+
		"[mako] review it and trust it with: mako %srepo-trust allow %s\r\n", f, node, f)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func goalCmd(ctx context.Context, c *client.Client, nodeID string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("goal needs a session ID")
	}
	sessionID := args[0]
	if len(args) == 1 {
		sessions, err := c.Sessions(ctx)
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if s.GetId() != sessionID {
				continue
			}
			printGoalStatus(s.GetGoal())
			return nil
		}
		return fmt.Errorf("unknown session %q", sessionID)
	}
	var err error
	if nodeID, err = sessionNode(ctx, c, nodeID, sessionID); err != nil {
		return err
	}
	action := strings.ToLower(args[1])
	req := &protocol.GoalAction{NodeId: nodeID, SessionId: sessionID, Action: action}
	switch action {
	case protocol.GoalActionSet, protocol.GoalActionEdit:
		if len(args) < 3 {
			return fmt.Errorf("goal %s needs an objective", action)
		}
		req.Objective = strings.Join(args[2:], " ")
	case protocol.GoalActionClear:
	default:
		return fmt.Errorf("unknown goal action %q (use set, edit, or clear)", args[1])
	}
	info, err := c.GoalAction(ctx, req)
	if err != nil {
		return err
	}
	printGoalStatus(info.GetGoal())
	return nil
}

func printGoalStatus(g *protocol.GoalStatus) {
	if g == nil {
		fmt.Println("no goal")
		return
	}
	switch g.GetStatus() {
	case protocol.GoalActive:
		if ps := g.GetProviderStatus(); ps != "" && ps != protocol.GoalActive {
			fmt.Printf("active (%s): %s\n", ps, g.GetText())
			return
		}
		fmt.Printf("active: %s\n", g.GetText())
	default:
		if r := g.GetReason(); r != "" {
			fmt.Printf("%s (%s)\n", g.GetStatus(), r)
			return
		}
		fmt.Println(g.GetStatus())
	}
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Truncate(time.Second).String()
}
