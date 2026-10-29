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

// findDevices resolves devices on the mesh, each by its ref
// (node/kind/serial), its node's name and its serial (node/serial), its
// serial, or its name, from one listing of the mesh.
func findDevices(ctx context.Context, c *client.Client, refs []string) ([]*protocol.Device, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	list, err := c.Devices(ctx, &protocol.ListDevices{Mesh: true})
	if err != nil {
		return nil, err
	}
	names := nodeNames(ctx, c)
	out := make([]*protocol.Device, 0, len(refs))
	for _, ref := range refs {
		d, err := matchDevice(list.GetDevices(), names, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func matchDevice(devices []*protocol.Device, names map[string]string, ref string) (*protocol.Device, error) {
	var found []*protocol.Device
	for _, d := range devices {
		switch {
		case d.Ref() == ref:
			return d, nil
		case ref == names[d.GetNodeId()]+"/"+d.GetSerial(), ref == d.GetSerial(), strings.EqualFold(ref, d.GetName()):
			found = append(found, d)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, fmt.Errorf("no device %q; mako devices lists them", ref)
	}
	var refs []string
	for _, d := range found {
		refs = append(refs, nodeName(names, d.GetNodeId())+"/"+d.GetSerial())
	}
	return nil, fmt.Errorf("%q could be %s", ref, strings.Join(refs, " or "))
}

// borrow borrows a device for nodeID, and prints the environment that
// reaches it as a POSIX shell sets it, the rest to standard error.
func borrow(ctx context.Context, c *client.Client, nodeID, ref string) error {
	found, err := findDevices(ctx, c, []string{ref})
	if err != nil {
		return err
	}
	dev := found[0]
	lease, err := c.Borrow(ctx, &protocol.Borrow{NodeId: nodeID, DeviceNodeId: dev.GetNodeId(), Kind: dev.GetKind(), Serial: dev.GetSerial()})
	if err != nil {
		return err
	}
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

// findLease resolves a device nodeID borrows by its lease's ID, its ref,
// its serial or its name.
func findLease(ctx context.Context, c *client.Client, nodeID, ref string) (*protocol.Lease, error) {
	list, err := c.Leases(ctx, &protocol.ListLeases{NodeId: nodeID})
	if err != nil {
		return nil, err
	}
	var found []*protocol.Lease
	for _, l := range list.GetLeases() {
		d := l.GetDevice()
		if l.GetId() == ref || d.Ref() == ref || d.GetSerial() == ref || strings.EqualFold(d.GetName(), ref) {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("no single device borrowed as %q; mako leases lists them", ref)
	}
	return found[0], nil
}

func giveBack(ctx context.Context, c *client.Client, nodeID, ref string) error {
	l, err := findLease(ctx, c, nodeID, ref)
	if err != nil {
		return err
	}
	_, err = c.GiveBack(ctx, &protocol.GiveBack{NodeId: nodeID, LeaseId: l.GetId()})
	return err
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
	fmt.Fprintln(w, "LEASE\tFROM\tKIND\tSERIAL\tNAME\tADDRESS\tSTATE")
	for _, l := range list.GetLeases() {
		d := l.GetDevice()
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", l.GetId(), nodeName(names, d.GetNodeId()), d.GetKind(), d.GetSerial(), d.GetName(), l.GetAddr(), l.GetState())
	}
	return w.Flush()
}
