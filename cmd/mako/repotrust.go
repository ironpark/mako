package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// repoTrustAsker sends a RepoTrust to the daemon: the client's, or a
// test's.
type repoTrustAsker func(context.Context, *protocol.RepoTrust) (*protocol.RepoTrustInfo, error)

// repoTrust is `mako repo-trust`. A file is trusted only as the user was
// shown it: the daemon refuses the trust if it reads otherwise by then.
func repoTrust(ctx context.Context, ask repoTrustAsker, nodeID string, args []string, in io.Reader, out io.Writer) error {
	op := "list"
	if len(args) > 0 {
		op, args = args[0], args[1:]
	}
	path := func() (string, error) {
		if len(args) != 1 {
			return "", fmt.Errorf("repo-trust %s needs one path", op)
		}
		return filepath.Abs(args[0])
	}
	switch op {
	case "list":
		info, err := ask(ctx, &protocol.RepoTrust{NodeId: nodeID, Op: protocol.RepoTrustList})
		if err != nil {
			return err
		}
		if len(info.GetFiles()) == 0 {
			fmt.Fprintln(out, "no command file is trusted")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "REPOSITORY\tFILE\tSHA256\tTRUSTED")
		for _, f := range info.GetFiles() {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", protocol.Printable(f.GetRoot()), protocol.Printable(f.GetPath()), shortHash(f.GetSha256()), f.GetTrustedAt().AsTime().Local().Format(time.DateTime))
		}
		return w.Flush()
	case "show", "allow":
		p, err := path()
		if err != nil {
			return err
		}
		info, err := ask(ctx, &protocol.RepoTrust{NodeId: nodeID, Path: p})
		if err != nil {
			return err
		}
		if len(info.GetFiles()) != 1 {
			return fmt.Errorf("the daemon showed %d files for %s", len(info.GetFiles()), p)
		}
		f := info.GetFiles()[0]
		printTrustedFile(out, f)
		if op == "show" {
			return nil
		}
		if f.GetTrusted() {
			fmt.Fprintln(out, "already trusted as it reads")
			return nil
		}
		fmt.Fprint(out, "Trust this file, and let mako run the commands it gives while it reads as above? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "not trusted")
			return nil
		}
		if _, err := ask(ctx, &protocol.RepoTrust{NodeId: nodeID, Op: protocol.RepoTrustAllow, Path: p, Sha256: f.GetSha256()}); err != nil {
			return err
		}
		fmt.Fprintf(out, "trusted %s of %s\n", protocol.Printable(f.GetPath()), protocol.Printable(f.GetRoot()))
		return nil
	case "revoke":
		p, err := path()
		if err != nil {
			return err
		}
		info, err := ask(ctx, &protocol.RepoTrust{NodeId: nodeID, Op: protocol.RepoTrustRevoke, Path: p})
		if err != nil {
			return err
		}
		if len(info.GetFiles()) == 0 {
			fmt.Fprintf(out, "nothing at %s was trusted\n", protocol.Printable(p))
		}
		for _, f := range info.GetFiles() {
			fmt.Fprintf(out, "forgot %s of %s\n", protocol.Printable(f.GetPath()), protocol.Printable(f.GetRoot()))
		}
		return nil
	}
	return fmt.Errorf("unknown repo-trust command %q", op)
}

// printTrustedFile prints a command file as the daemon showed it: where it
// is, its hash, whether it is trusted, and all it says, every control
// character made visible, so nothing in it hides a command.
func printTrustedFile(out io.Writer, f *protocol.TrustedFile) {
	fmt.Fprintf(out, "%s\n", protocol.Printable(filepath.Join(f.GetRoot(), filepath.FromSlash(f.GetPath()))))
	fmt.Fprintf(out, "  sha256   %s\n", f.GetSha256())
	if f.GetTrusted() {
		fmt.Fprintf(out, "  trusted  yes, since %s\n", f.GetTrustedAt().AsTime().Local().Format(time.DateTime))
	} else {
		fmt.Fprintln(out, "  trusted  no")
	}
	fmt.Fprintln(out, "----")
	text := protocol.Printable(string(f.GetContent()))
	fmt.Fprint(out, text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "----")
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
