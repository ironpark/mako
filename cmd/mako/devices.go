package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// deviceFlag is -device, given more than once or as a comma-separated
// list.
type deviceFlag []string

func (f *deviceFlag) String() string { return strings.Join(*f, ",") }

func (f *deviceFlag) Set(v string) error {
	for name := range strings.SplitSeq(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			*f = append(*f, name)
		}
	}
	return nil
}

// nodeNames are the mesh's nodes' names, by ID.
func nodeNames(ctx context.Context, c *client.Client) map[string]string {
	names := map[string]string{}
	nodes, _ := c.Nodes(ctx)
	for _, n := range nodes {
		names[n.GetId()] = n.GetName()
	}
	return names
}

func nodeName(names map[string]string, id string) string {
	if name := names[id]; name != "" {
		return name
	}
	return short(id)
}

func listDevices(ctx context.Context, c *client.Client) error {
	list, err := c.Devices(ctx, &protocol.ListDevices{Mesh: true})
	if err != nil {
		return err
	}
	for _, e := range list.GetErrors() {
		fmt.Fprintln(os.Stderr, "mako:", e)
	}
	if len(list.GetDevices()) == 0 {
		fmt.Println("no devices")
		return nil
	}
	names := nodeNames(ctx, c)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tKIND\tSERIAL\tNAME\tSTATE\tLENT")
	for _, d := range list.GetDevices() {
		lent := "not shared"
		if d.GetShared() {
			var to []string
			for _, h := range d.GetHolders() {
				to = append(to, h.GetName())
			}
			lent = "to " + strings.Join(to, ", ")
			if len(to) == 0 {
				lent = "-"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", nodeName(names, d.GetNodeId()), d.GetKind(), d.GetSerial(), d.GetName(), d.GetState(), lent)
	}
	return w.Flush()
}

// borrow borrows a device for nodeID, and prints the environment that
// reaches it as a POSIX shell sets it, the rest to standard error.
func borrow(ctx context.Context, c *client.Client, nodeID, ref string) error {
	lease, err := c.Borrow(ctx, &protocol.Borrow{NodeId: nodeID, Device: ref})
	if err != nil {
		return err
	}
	dev := lease.GetDevice()
	fmt.Fprintf(os.Stderr, "mako: %s %s lent at %s\n", dev.GetName(), dev.GetSerial(), lease.GetAddr())
	for _, w := range lease.GetWarnings() {
		fmt.Fprintln(os.Stderr, "mako: warning:", w)
	}
	env := lease.GetEnv()
	for _, k := range slices.Sorted(maps.Keys(env)) {
		fmt.Printf("export %s=%s\n", k, shellQuote(env[k]))
	}
	return nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func giveBack(ctx context.Context, c *client.Client, nodeID, ref string) error {
	l, err := c.GiveBack(ctx, &protocol.GiveBack{NodeId: nodeID, Ref: ref})
	if err != nil {
		return err
	}
	if l.GetState() != protocol.LeaseReturned {
		fmt.Fprintf(os.Stderr, "mako: %s stays borrowed for sessions %s, until they end\n", l.GetDevice().GetSerial(), strings.Join(l.GetSessions(), ", "))
	}
	return nil
}

func listLeases(ctx context.Context, c *client.Client, nodeID string) error {
	list, err := c.Leases(ctx, &protocol.ListLeases{NodeId: nodeID})
	if err != nil {
		return err
	}
	if len(list.GetLeases()) == 0 {
		fmt.Println("no devices borrowed")
		return nil
	}
	names := nodeNames(ctx, c)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LEASE\tFROM\tKIND\tSERIAL\tNAME\tADDRESS\tSTATE\tHELD BY")
	for _, l := range list.GetLeases() {
		d := l.GetDevice()
		var by []string
		if l.GetBorrowed() {
			by = append(by, "hand")
		}
		for _, s := range l.GetSessions() {
			by = append(by, "session "+s)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", l.GetId(), nodeName(names, d.GetNodeId()), d.GetKind(), d.GetSerial(), d.GetName(), l.GetAddr(), l.GetState(), strings.Join(by, ", "))
	}
	return w.Flush()
}
