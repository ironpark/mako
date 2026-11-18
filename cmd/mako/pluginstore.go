package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// storeOps are the plugins commands that ask the daemon's registries.
var storeOps = map[string]bool{"search": true, "install": true, "update": true, "remove": true, "registries": true}

// pluginStore searches the node's registries, installs from them, and
// changes which registries the node reads.
func pluginStore(ctx context.Context, c *client.Client, op string, args []string) error {
	switch op {
	case "search":
		found, err := c.SearchPlugins(ctx, &protocol.SearchPlugins{Query: strings.Join(args, " ")})
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tVERSION\tPUBLISHER (UNCHECKED)\tKEY\tREGISTRY\tASKS FOR\tHERE")
		for _, l := range found.GetListings() {
			here := l.GetInstalled()
			switch {
			case l.GetBlocked() != "":
				here = "BLOCKED: " + l.GetBlocked()
			case l.GetOtherKey():
				here += " (by another key)"
			case l.GetPreviousFingerprint() != "":
				here = "BY A DIFFERENT KEY THAN BEFORE: " + l.GetPreviousFingerprint() + " → " + l.GetFingerprint()
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", l.GetId(), l.GetVersion(), l.GetPublisher(), l.GetFingerprint(),
				l.GetRegistry(), strings.Join(l.GetPermissions(), ","), here)
		}
		return w.Flush()
	case "install", "update", "remove":
		fs := flag.NewFlagSet("plugins "+op, flag.ContinueOnError)
		reg := fs.String("registry", "", "the registry to install from")
		in, err := parseInterleaved(fs, args)
		if err != nil {
			return err
		}
		if len(in) != 1 {
			return fmt.Errorf("plugins %s takes a plugin's ID", op)
		}
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		done, err := c.InstallPlugin(ctx, &protocol.InstallPlugin{Op: op, Id: in[0], Registry: *reg})
		if err != nil {
			return err
		}
		if op == protocol.RemoveOp {
			fmt.Printf("%s removed, with what it was allowed and kept on this node\n", in[0])
			return nil
		}
		fmt.Printf("%s %s installed\n", done.GetId(), done.GetVersion())
		if prev := done.GetPreviousFingerprint(); prev != "" {
			fmt.Printf("NOTE: %s is published by a different key than the one you had before: %s → %s\n",
				done.GetId(), prev, done.GetFingerprint())
		}
		if len(done.GetAdded()) > 0 {
			fmt.Printf("It asks for %s, which it does not have: allow it with `mako plugins grant %s`, or in the window\n",
				strings.Join(done.GetAdded(), ", "), done.GetId())
		}
		return nil
	case "registries":
		var regs *protocol.Registries
		var err error
		if len(args) == 0 {
			regs, err = c.Registries(ctx)
		} else {
			req := &protocol.RegistryAction{Op: args[0]}
			switch {
			case args[0] == protocol.RegistryRefresh && len(args) == 1:
			case (args[0] == protocol.RegistryAdd || args[0] == protocol.RegistryRemove) && len(args) == 2:
				req.Url = args[1]
			default:
				return errors.New("plugins registries takes add <url>, remove <url> or refresh")
			}
			regs, err = c.RegistryAction(ctx, req)
		}
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "REGISTRY\tNAME\tPLUGINS\tBLOCKED\tREAD\tERROR")
		for _, r := range regs.GetRegistries() {
			read := "never"
			if r.GetFetchedAt() > 0 {
				read = time.Unix(r.GetFetchedAt(), 0).Format(time.DateTime)
			}
			url := r.GetUrl()
			if r.GetBuiltin() {
				url += " (built in)"
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\n", url, r.GetName(), r.GetPlugins(), r.GetBlocked(), read, r.GetError())
		}
		return w.Flush()
	}
	return fmt.Errorf("unknown plugins command %q", op)
}
