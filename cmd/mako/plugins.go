package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ironpark/mako/internal/client"
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
		fmt.Fprintln(w, "ID\tVERSION\tSTATE\tASKS FOR\tGRANTED\tERROR")
		for _, p := range list.GetPlugins() {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.GetId(), p.GetVersion(), p.GetState(),
				strings.Join(p.GetAsked(), ","), strings.Join(p.GetGranted(), ","), p.GetError())
		}
		return w.Flush()
	}
	op, rest := args[0], args[1:]
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
