package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/sshremote"
)

// sshCmd handles `mako ssh [list|add|remove|enable|disable|test|discover]`.
func sshCmd(args []string) error {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		return sshList()
	}
	switch args[0] {
	case "add":
		return sshAdd(args[1:])
	case "remove", "rm":
		return sshRemove(args[1:])
	case "enable":
		return sshSetDisabled(args[1:], false)
	case "disable":
		return sshSetDisabled(args[1:], true)
	case "test":
		return sshTest(args[1:])
	case "discover":
		return sshDiscover()
	default:
		return fmt.Errorf("unknown ssh subcommand %q (want list, add, remove, enable, disable, test, or discover)", args[0])
	}
}

func sshList() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.SSHHosts) == 0 {
		fmt.Println("No SSH hosts configured.")
		fmt.Println("Add one with: mako ssh add <user@host>")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tNAME\tTARGET\tMODE\tSTATE\tDEFAULT DIR")
	for _, h := range cfg.SSHHosts {
		state := "enabled"
		if h.Disabled {
			state = "disabled"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			h.NodeID(), h.DisplayName(), h.DisplayAddr(), h.EffectiveMode(), state, h.DefaultDir)
	}
	return w.Flush()
}

func sshAdd(args []string) error {
	fs := flag.NewFlagSet("mako ssh add", flag.ContinueOnError)
	name := fs.String("name", "", "display name (defaults to host)")
	user := fs.String("user", "", "SSH username")
	port := fs.Int("port", 0, "SSH port (0 uses ~/.ssh/config or 22)")
	key := fs.String("key", "", "SSH identity file path")
	jump := fs.String("jump", "", "ProxyJump host")
	dir := fs.String("dir", "", "default remote working directory")
	mode := fs.String("mode", config.SSHModeAuto, "connection mode: auto, daemon, or direct")
	remoteBin := fs.String("remote-binary", "", "path to makod on the remote host")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) != 1 {
		return errors.New("ssh add takes one host argument: mako ssh add <host> [-name ...] [-port ...] [-key ...]")
	}
	h := config.SSHHost{
		ID:           config.NewSSHHostID(),
		Name:         strings.TrimSpace(*name),
		Host:         strings.TrimSpace(pos[0]),
		User:         strings.TrimSpace(*user),
		Port:         *port,
		IdentityFile: strings.TrimSpace(*key),
		ProxyJump:    strings.TrimSpace(*jump),
		DefaultDir:   strings.TrimSpace(*dir),
		Mode:         strings.TrimSpace(*mode),
		RemoteBinary: strings.TrimSpace(*remoteBin),
	}
	if h.Name == "" {
		h.Name = h.Host
	}
	if err := h.Validate(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.SSHHosts = config.NormalizeSSHHosts(append(cfg.SSHHosts, h))
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("Added SSH host %s (%s) as %s\n", h.DisplayName(), h.DisplayAddr(), h.NodeID())
	return nil
}

func matchSSHHost(hosts []config.SSHHost, query string) (int, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return -1, errors.New("SSH host ID or name is required")
	}
	qID := strings.TrimPrefix(q, config.SSHNodePrefix)
	for i, h := range hosts {
		if h.ID == qID || h.NodeID() == q || strings.EqualFold(h.Name, q) || strings.EqualFold(h.Host, q) {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no SSH host matching %q", query)
}

func sshRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("ssh remove takes one host ID or name")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	idx, err := matchSSHHost(cfg.SSHHosts, args[0])
	if err != nil {
		return err
	}
	removed := cfg.SSHHosts[idx]
	cfg.SSHHosts = slices.Delete(cfg.SSHHosts, idx, idx+1)
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("Removed SSH host %s (%s)\n", removed.DisplayName(), removed.NodeID())
	return nil
}

func sshSetDisabled(args []string, disabled bool) error {
	if len(args) != 1 {
		return errors.New("expected one host ID or name")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	idx, err := matchSSHHost(cfg.SSHHosts, args[0])
	if err != nil {
		return err
	}
	cfg.SSHHosts[idx].Disabled = disabled
	if err := config.Save(cfg); err != nil {
		return err
	}
	verb := "Enabled"
	if disabled {
		verb = "Disabled"
	}
	fmt.Printf("%s SSH host %s (%s)\n", verb, cfg.SSHHosts[idx].DisplayName(), cfg.SSHHosts[idx].NodeID())
	return nil
}

func sshTest(args []string) error {
	if len(args) != 1 {
		return errors.New("ssh test takes one configured host ID/name or an SSH target")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var host config.SSHHost
	if idx, err := matchSSHHost(cfg.SSHHosts, args[0]); err == nil {
		host = cfg.SSHHosts[idx]
	} else {
		host = config.SSHHost{Host: strings.TrimSpace(args[0])}
		if err := host.Validate(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := sshremote.TestConnection(ctx, host)
	if err != nil {
		return err
	}
	platform := (&protocol.Node{Os: res.OS, Arch: res.Arch}).Platform()
	mode := "direct SSH"
	if res.MakodPath != "" && host.EffectiveMode() != config.SSHModeDirect {
		mode = "remote makod (" + res.MakodPath + ")"
	}
	fmt.Printf("Connected to %s in %dms\n", host.DisplayAddr(), max(res.Latency.Milliseconds(), 1))
	fmt.Printf("  Platform: %s (%s)\n", platform, res.Hostname)
	fmt.Printf("  Home:     %s\n", res.Home)
	fmt.Printf("  Mode:     %s\n", mode)
	if len(res.Agents) > 0 {
		fmt.Printf("  Agents:   %s\n", strings.Join(res.Agents, ", "))
	} else {
		fmt.Printf("  Agents:   shell only\n")
	}
	return nil
}

func sshDiscover() error {
	hosts := sshremote.DiscoverConfigHosts()
	if len(hosts) == 0 {
		fmt.Println("No concrete Host entries found in ~/.ssh/config.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ALIAS\tTARGET\tIDENTITY\tJUMP")
	for _, h := range hosts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", h.Alias, h.Summary(), h.IdentityFile, h.ProxyJump)
	}
	return w.Flush()
}
