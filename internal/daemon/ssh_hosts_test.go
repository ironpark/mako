package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/sshremote"
)

func installFakeSSH(t *testing.T, extraHandler string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake ssh test")
	}
	fakeBin := filepath.Join(t.TempDir(), "fake-ssh")
	script := `#!/bin/sh
` + extraHandler + `
while [ $# -gt 0 ]; do
  if [ "$1" = "--" ]; then
    shift
    shift # target
    break
  fi
  shift
done
exec /bin/sh -c "$*"
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := sshremote.SSHBinary
	sshremote.SSHBinary = fakeBin
	t.Cleanup(func() { sshremote.SSHBinary = old })
}

func waitForSSHNode(t *testing.T, cli *client.Client, nodeID, wantLink string) *protocol.Node {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		nodes, err := cli.Nodes(context.Background())
		if err == nil {
			for _, n := range nodes {
				if n.GetId() == nodeID && n.GetLink() == wantLink {
					return n
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	nodes, _ := cli.Nodes(context.Background())
	t.Fatalf("timed out waiting for SSH node %q link=%q; nodes=%+v", nodeID, wantLink, nodes)
	return nil
}

func TestDaemonDirectSSHHostLifecycleAndOps(t *testing.T) {
	installFakeSSH(t, "")
	dir := t.TempDir()
	cfg := config.Default()
	cfg.NodeID = "local-node"
	cfg.Name = "laptop"
	cfg.P2P.Disabled = true
	cfg.SSHHosts = []config.SSHHost{
		{
			ID:   "gpu1",
			Name: "GPU Box",
			Host: "gpu.local",
			User: "dev",
			Mode: config.SSHModeDirect,
		},
	}

	d := New(cfg, nil)
	d.dir = dir
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	<-d.Ready()

	cli, err := client.Dial(ctx, d.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	nodeID := "ssh:gpu1"
	n := waitForSSHNode(t, cli, nodeID, protocol.LinkUp)
	if n.GetSource() != "ssh" || n.GetName() != "GPU Box" || !n.GetTrustsUs() || !slices.Contains(n.GetFeatures(), sshremote.FeatureSSHDirect) {
		t.Fatalf("unexpected SSH node: %+v", n)
	}

	// File ops routed to direct SSH node.
	work := t.TempDir()
	targetFile := filepath.Join(work, "note.txt")
	if _, err := cli.WriteFile(ctx, &protocol.WriteFile{
		NodeId: nodeID,
		Path:   targetFile,
		Data:   []byte("via direct ssh daemon"),
	}); err != nil {
		t.Fatalf("WriteFile via SSH node: %v", err)
	}
	rf, err := cli.ReadFile(ctx, &protocol.ReadFile{NodeId: nodeID, Path: targetFile})
	if err != nil || string(rf.GetData()) != "via direct ssh daemon" {
		t.Fatalf("ReadFile = (%q, %v)", string(rf.GetData()), err)
	}
	listing, err := cli.ListDir(ctx, &protocol.ListDir{NodeId: nodeID, Path: work})
	if err != nil || len(listing.GetEntries()) != 1 {
		t.Fatalf("ListDir = (%+v, %v)", listing, err)
	}

	// Session open, list, rename, close on direct SSH node.
	sess, err := cli.Open(ctx, &protocol.OpenSession{
		NodeId:  nodeID,
		Agent:   "shell",
		Project: work,
		Args:    []string{"-c", "sleep 30"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("Open direct SSH session: %v", err)
	}
	if sess.GetNodeId() != nodeID {
		t.Fatalf("session node_id = %q, want %q", sess.GetNodeId(), nodeID)
	}
	renamed, err := cli.Rename(ctx, &protocol.RenameSession{NodeId: nodeID, SessionId: sess.GetId(), Name: "remote-shell"})
	if err != nil || renamed.GetName() != "remote-shell" {
		t.Fatalf("Rename = (%+v, %v)", renamed, err)
	}
	if err := cli.Kill(ctx, &protocol.CloseSession{NodeId: nodeID, SessionId: sess.GetId()}); err != nil {
		t.Fatalf("Kill direct SSH session: %v", err)
	}

	// Access relink works even with P2P disabled.
	if _, err := cli.Access(ctx, &protocol.Access{Op: "relink", NodeId: nodeID}); err != nil {
		t.Fatalf("Access relink on SSH host failed: %v", err)
	}

	// Live config sync removes the SSH host cleanly.
	d.ssh.sync(nil)
	nodes, err := cli.Nodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.GetId() == nodeID {
			t.Fatalf("SSH node %q still present after removal", nodeID)
		}
	}
}

func TestDaemonTunneledSSHRemoteMakod(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket tunnel test")
	}
	// Start a second real makod acting as the "remote" daemon.
	remoteDir := t.TempDir()
	remoteCfg := config.Default()
	remoteCfg.NodeID = "remote-real-node"
	remoteCfg.Name = "remote-box"
	remoteCfg.P2P.Disabled = true
	remoteD := New(remoteCfg, nil)
	remoteD.dir = remoteDir

	ctx, cancel := context.WithCancel(context.Background())
	remoteDone := make(chan error, 1)
	go func() { remoteDone <- remoteD.Run(ctx) }()
	defer func() {
		cancel()
		<-remoteDone
	}()
	<-remoteD.Ready()

	// Fake ssh that:
	// - when invoked with `-N ... -L localSock:remoteSock`, creates a symlink
	//   from localSock -> remoteSock and sleeps until killed.
	// - when invoked with remote probe/ensure scripts, reports MAKOD=/bin/true
	//   and SOCK=<remoteD.Socket()>.
	fakeBin := filepath.Join(t.TempDir(), "fake-ssh-tun")
	script := `#!/bin/sh
if [ "$1" = "-N" ]; then
  # Parse -L local:remote
  while [ $# -gt 0 ]; do
    if [ "$1" = "-L" ]; then
      shift
      _l="${1%%:*}"
      _r="${1#*:}"
      ln -sf "$_r" "$_l"
      trap 'rm -f "$_l"; exit 0' TERM INT
      while true; do sleep 1; done
    fi
    shift
  done
  exit 1
fi
case "$*" in
  *MAKOD=*)
    printf 'OS=Linux\nARCH=x86_64\nHOSTNAME=remote-box\nHOME=%s\nSHELL=/bin/sh\nMAKOD=/bin/true\nAGENTS=claude\n' "` + remoteDir + `"
    exit 0
    ;;
  *SOCK=*)
    printf 'SOCK=%s\n' "` + remoteD.Socket() + `"
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := sshremote.SSHBinary
	sshremote.SSHBinary = fakeBin
	t.Cleanup(func() { sshremote.SSHBinary = old })

	localDir := t.TempDir()
	localCfg := config.Default()
	localCfg.NodeID = "local-laptop"
	localCfg.Name = "laptop"
	localCfg.P2P.Disabled = true
	localCfg.SSHHosts = []config.SSHHost{
		{
			ID:   "tun1",
			Name: "Remote Makod",
			Host: "remote.example",
			Mode: config.SSHModeAuto,
		},
	}
	localD := New(localCfg, nil)
	localD.dir = localDir
	localDone := make(chan error, 1)
	go func() { localDone <- localD.Run(ctx) }()
	defer func() {
		cancel()
		<-localDone
	}()
	<-localD.Ready()

	cli, err := client.Dial(ctx, localD.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	nodeID := "ssh:tun1"
	n := waitForSSHNode(t, cli, nodeID, protocol.LinkUp)
	if !slices.Contains(n.GetFeatures(), sshremote.FeatureSSHDaemon) {
		t.Fatalf("expected ssh:daemon feature, got %v", n.GetFeatures())
	}

	// Verify RPC forwarding clears node_id so remoteD serves ListDir locally.
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "from-remote.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing, err := cli.ListDir(ctx, &protocol.ListDir{NodeId: nodeID, Path: work})
	if err != nil || len(listing.GetEntries()) != 1 || listing.GetEntries()[0].GetName() != "from-remote.txt" {
		t.Fatalf("forwarded ListDir via SSH daemon tunnel = (%+v, %v)", listing, err)
	}
}
