// Command sanbo is the front-end for the sanbo daemon mesh: it lists nodes,
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

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

const usage = `sanbo - manage terminal agents across your computers

Usage:
  sanbo nodes                        list daemons on the mesh
  sanbo addrs                        print this daemon's libp2p addresses, to list
                                     as another's peer or relay
  sanbo ls                           list agent sessions
  sanbo run <agent>[:<profile>] [-- args...]
                                     start an agent, or one of its profiles, and attach
  sanbo attach <session-id>          attach to a running session
  sanbo kill <session-id>            terminate a session
  sanbo rename <session-id> [name]   name a session (no name clears it)
  sanbo agents                       list agents and profiles this node can launch

Flags:
  -daemon <addr>   daemon to talk to (default 127.0.0.1:4242)
  -node <id>       run on / target this node instead of the local one
  -project <dir>   working directory for a new session (default: cwd)
`

func main() {
	daemonAddr := flag.String("daemon", "", "daemon address")
	nodeID := flag.String("node", "", "target node ID")
	project := flag.String("project", "", "project directory")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(args, *daemonAddr, *nodeID, *project); err != nil {
		fmt.Fprintln(os.Stderr, "sanbo:", err)
		os.Exit(1)
	}
}

func run(args []string, daemonAddr, nodeID, project string) error {
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
	c, err := client.Dial(dialCtx, daemonAddr, cfg.PSK)
	if err != nil {
		return fmt.Errorf("cannot reach daemon (is sanbod running?): %w", err)
	}
	defer c.Close()

	switch args[0] {
	case "nodes":
		return listNodes(ctx, c, cfg.PSK)
	case "addrs":
		return printAddrs(c.Node())
	case "ls":
		return listSessions(ctx, c, cfg.PSK)
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("run needs an agent name")
		}
		return runAgent(ctx, c, cfg.PSK, nodeID, project, args[1], args[2:])
	case "attach":
		if len(args) < 2 {
			return fmt.Errorf("attach needs a session ID")
		}
		if nodeID, err = sessionNode(ctx, c, cfg.PSK, nodeID, args[1]); err != nil {
			return err
		}
		return c.Attach(ctx, cfg.PSK, &protocol.Attach{SessionId: args[1], NodeId: nodeID, Replay: true})
	case "kill":
		if len(args) < 2 {
			return fmt.Errorf("kill needs a session ID")
		}
		if nodeID, err = sessionNode(ctx, c, cfg.PSK, nodeID, args[1]); err != nil {
			return err
		}
		return c.Kill(ctx, cfg.PSK, &protocol.CloseSession{SessionId: args[1], NodeId: nodeID})
	case "rename":
		if len(args) < 2 {
			return fmt.Errorf("rename needs a session ID")
		}
		if nodeID, err = sessionNode(ctx, c, cfg.PSK, nodeID, args[1]); err != nil {
			return err
		}
		_, err := c.Rename(ctx, cfg.PSK, &protocol.RenameSession{SessionId: args[1], NodeId: nodeID, Name: strings.Join(args[2:], " ")})
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// sessionNode finds the node that owns sessionID when -node was not given, so
// any session shown by ls can be attached or killed by ID alone.
func sessionNode(ctx context.Context, c *client.Client, psk, nodeID, sessionID string) (string, error) {
	if nodeID != "" {
		return nodeID, nil
	}
	sessions, err := c.Sessions(ctx, psk)
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

func listNodes(ctx context.Context, c *client.Client, psk string) error {
	nodes, err := c.Nodes(ctx, psk)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tADDRESS\tVIA\tLAST SEEN")
	for _, n := range nodes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", short(n.GetId()), n.GetName(), n.GetAddr(), n.GetSource(), ago(protocol.TimeOf(n.GetLastSeen())))
	}
	return w.Flush()
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

func listSessions(ctx context.Context, c *client.Client, psk string) error {
	sessions, err := c.Sessions(ctx, psk)
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

func runAgent(ctx context.Context, c *client.Client, psk, nodeID, project, target string, extra []string) error {
	agent, profile := agentspec.SplitTarget(target)
	// "sanbo run <agent> -- args" separates agent arguments; the separator
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

	info, err := c.Open(ctx, psk, &protocol.OpenSession{
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
	fmt.Fprintf(os.Stderr, "[sanbo] session %s on %s\r\n", info.GetId(), short(info.GetNodeId()))
	return c.Attach(ctx, psk, &protocol.Attach{SessionId: info.GetId(), NodeId: nodeID, Replay: false})
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
