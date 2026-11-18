package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/protocol"
)

// plugins lists the node's plugins, or acts on one as the window would
// for the user: allowing what it asks for is the user's to do, from the
// window or from here.
func plugins(ctx context.Context, c *client.Client, args []string) error {
	if len(args) == 0 {
		list, err := c.Plugins(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tVERSION\tSTATE\tASKS FOR\tGRANTED\tFROM\tUPDATE\tERROR")
		for _, p := range list.GetPlugins() {
			from := "folder"
			if p.GetInstalled() {
				from = p.GetSource() + " " + pack.Fingerprint(p.GetKey())
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.GetId(), p.GetVersion(), p.GetState(),
				strings.Join(p.GetAsked(), ","), strings.Join(p.GetGranted(), ","), from, p.GetUpdate(), p.GetError())
		}
		if err := w.Flush(); err != nil {
			return err
		}
		for _, p := range list.GetPlugins() {
			if a := p.GetAgentAsk(); a != nil {
				fmt.Printf("\n%s asks to start %s in %s (%s): %s\n  mako plugins allow-agent %s once|always, or deny-agent %s\n",
					p.GetId(), a.GetAgent(), a.GetProject(), a.GetRoot(), oneLine(a.GetPrompt(), 200), p.GetId(), p.GetId())
			}
		}
		switch store := list.GetSecrets(); store {
		case "":
		case "file":
			fmt.Printf("\nSecrets: kept in a file of each plugin's that only you can read; this node has no keychain (%s).\n", list.GetSecretsNote())
		default:
			fmt.Printf("\nSecrets: kept in %s.\n", store)
		}
		return nil
	}
	op, rest := args[0], args[1:]
	if storeOps[op] {
		return pluginStore(ctx, c, op, rest)
	}
	if len(rest) == 0 {
		return fmt.Errorf("plugins %s needs a plugin's ID", op)
	}
	req := &protocol.PluginAction{PluginId: rest[0], Op: op}
	switch op {
	case protocol.PluginGrant, protocol.PluginDeny, protocol.PluginEnable, protocol.PluginDisable, protocol.PluginReload:
	case protocol.PluginRun:
		if len(rest) < 2 {
			return fmt.Errorf("plugins run needs a command")
		}
		req.Command = rest[1]
	case protocol.PluginSet:
		if len(rest) < 2 {
			return fmt.Errorf("plugins set needs a setting's key")
		}
		req.Key, req.Value = rest[1], strings.Join(rest[2:], " ")
	case "allow-agent", "deny-agent":
		// The ask answered is the one the plugin has waiting now.
		ask, err := waitingAsk(ctx, c, rest[0])
		if err != nil {
			return err
		}
		req.Op, req.Key = protocol.PluginDenyAgent, ask
		if op == "allow-agent" {
			if len(rest) < 2 || (rest[1] != protocol.AgentOnce && rest[1] != protocol.AgentAlways) {
				return fmt.Errorf("plugins allow-agent <id> once|always")
			}
			req.Op, req.Value = protocol.PluginAllowAgent, rest[1]
		}
	case "forget-agents":
		req.Op = protocol.PluginForgetAgents
	default:
		return fmt.Errorf("unknown plugins command %q", op)
	}
	done, err := c.PluginAction(ctx, req)
	if err != nil {
		return err
	}
	if done.GetOpenUrl() != "" {
		fmt.Println(done.GetOpenUrl())
	}
	return nil
}

// waitingAsk is the ID of the agent the plugin asks to start now.
func waitingAsk(ctx context.Context, c *client.Client, id string) (string, error) {
	list, err := c.Plugins(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range list.GetPlugins() {
		if p.GetId() == id && p.GetAgentAsk() != nil {
			return p.GetAgentAsk().GetId(), nil
		}
	}
	return "", fmt.Errorf("the plugin %s asks to start no agent now", id)
}

// oneLine is s on one line, cut at n bytes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
