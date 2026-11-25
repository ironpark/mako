package discovery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTailscaleSuppressesBrokenBinaryAndRecoversWhenFixed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell script fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tailscale")

	// A binary with a broken #! interpreter must fail preflight without fork
	// and be suppressed by the StartGate on subsequent scans until fixed.
	if err := os.WriteFile(bin, []byte("#!/nonexistent/interp\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := &Tailscale{Binary: bin}
	if _, err := ts.tailnetHosts(context.Background()); err == nil {
		t.Fatal("expected error for broken tailscale binary")
	}
	if ts.gate.Allow(bin) {
		t.Fatal("expected StartGate to suppress unchanged broken binary")
	}

	// Replace with a valid script returning valid tailscale status JSON.
	script := "#!/bin/sh\nprintf '%s' '{\"Peer\":{\"p1\":{\"DNSName\":\"peer.ts.net.\",\"Online\":true}}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	hosts, err := ts.tailnetHosts(context.Background())
	if err != nil {
		t.Fatalf("fixed tailscale binary failed: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "peer.ts.net" {
		t.Fatalf("hosts = %v, want [peer.ts.net]", hosts)
	}
}
