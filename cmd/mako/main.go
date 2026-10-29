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
	"github.com/ironpark/mako/internal/protocol"
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
  mako agents                       list agents and profiles this node can launch

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
  mako trust <public key line>      let a key in, as authorized_keys writes it
  mako revoke <fingerprint>         take a key out

Flags:
  -daemon <path>   the daemon's socket (default: the configuration directory's)
  -node <id>       run on / target this node instead of the local one
  -project <dir>   working directory for a new session (default: cwd)
  -device <dev>    with run, borrow a device for the session (repeatable,
                    or comma-separated)
`

func main() {
	daemonAddr := flag.String("daemon", "", "the daemon's socket")
	nodeID := flag.String("node", "", "target node ID")
	project := flag.String("project", "", "project directory")
	var devices deviceFlag
	flag.Var(&devices, "device", "a device to borrow for the session")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(args, *daemonAddr, *nodeID, *project, devices); err != nil {
		fmt.Fprintln(os.Stderr, "mako:", err)
		os.Exit(1)
	}
}

func run(args []string, daemonAddr, nodeID, project string, devices []string) error {
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
		return runAgent(ctx, c, nodeID, project, args[1], args[2:], devices)
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
	for _, s := range sessions {
		name := s.GetName()
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.GetId(), short(s.GetNodeId()), agentspec.Target(s.GetAgent(), s.GetProfile()), name, s.GetProject(), s.GetState(), ago(protocol.TimeOf(s.GetStartedAt())))
	}
	return w.Flush()
}

func runAgent(ctx context.Context, c *client.Client, nodeID, project, target string, extra, devices []string) error {
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

	info, err := c.Open(ctx, &protocol.OpenSession{
		Devices: devices,
		NodeId:  nodeID,
		Agent:   agent,
		Profile: profile,
		Project: abs,
		Args:    extra,
		Cols:    uint32(cols),
		Rows:    uint32(rows),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[mako] session %s on %s\r\n", info.GetId(), short(info.GetNodeId()))
	return c.Attach(ctx, &protocol.Attach{SessionId: info.GetId(), NodeId: nodeID, Replay: false})
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Truncate(time.Second).String()
}
