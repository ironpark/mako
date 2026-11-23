package e2e_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
)

func TestDaemonCLIOfflineChecks(t *testing.T) {
	t.Run("makod_version", func(t *testing.T) {
		env := newTestEnv(t)
		out, errOut, code := env.runBin(makodBin, "-version")
		if code != 0 {
			t.Fatalf("makod -version exited %d: stderr=%q", code, errOut)
		}
		want := "makod " + config.Version
		if strings.TrimSpace(out) != want {
			t.Fatalf("makod -version stdout = %q, want %q", out, want)
		}
	})

	t.Run("mako_agents_offline", func(t *testing.T) {
		env := newTestEnv(t)
		out, errOut, code := env.runCLI("agents")
		if code != 0 {
			t.Fatalf("mako agents exited %d: stderr=%q", code, errOut)
		}
		for _, want := range []string{"agy", "claude", "codex"} {
			if !strings.Contains(out, want) {
				t.Fatalf("expected %q in mako agents output, got %q", want, out)
			}
		}
	})

	t.Run("mako_no_args_usage_exit_2", func(t *testing.T) {
		env := newTestEnv(t)
		_, errOut, code := env.runCLI()
		if code != 2 {
			t.Fatalf("mako (no args) exit code = %d, want 2 (stderr=%q)", code, errOut)
		}
		if !strings.Contains(strings.ToLower(errOut), "usage:") {
			t.Fatalf("expected usage message on stderr, got %q", errOut)
		}
	})

	t.Run("mako_nodes_stopped_socket_exit_1", func(t *testing.T) {
		env := newTestEnv(t)
		_, errOut, code := env.runCLI("nodes")
		if code != 1 {
			t.Fatalf("mako nodes without daemon exit code = %d, want 1 (stderr=%q)", code, errOut)
		}
		if !strings.Contains(errOut, "mako:") {
			t.Fatalf("expected error prefixed with 'mako:', got %q", errOut)
		}
	})

	t.Run("mako_custom_missing_daemon_flag_exit_1", func(t *testing.T) {
		env := newTestEnv(t)
		missingSock := filepath.Join(env.tmpDir, "missing.sock")
		_, errOut, code := env.runCLI("-daemon", missingSock, "ls")
		if code != 1 {
			t.Fatalf("mako -daemon <missing> ls exit code = %d, want 1 (stderr=%q)", code, errOut)
		}
		if errOut == "" {
			t.Fatal("expected non-empty error on stderr")
		}
	})

	t.Run("mako_unknown_flag_exit_2", func(t *testing.T) {
		env := newTestEnv(t)
		_, errOut, code := env.runCLI("-no-such-flag")
		if code != 2 {
			t.Fatalf("mako -no-such-flag exit code = %d, want 2 (stderr=%q)", code, errOut)
		}
		if !strings.Contains(strings.ToLower(errOut), "usage:") {
			t.Fatalf("expected usage message on stderr, got %q", errOut)
		}
	})

	t.Run("client_dial_canceled_context_fails", func(t *testing.T) {
		env := newTestEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := client.Dial(ctx, filepath.Join(env.tmpDir, "missing.sock")); err == nil {
			t.Fatal("expected client.Dial with canceled context to fail")
		}
	})
}

func TestDaemonCLIConnectedAndLifecycle(t *testing.T) {
	env := newTestEnv(t, func(o *testConfigOpts) {
		o.NodeID = "e2e-node-alpha"
		o.Name = "alpha-box"
	})
	d := startDaemon(t, env)

	t.Run("client_dial_handshake", func(t *testing.T) {
		c := d.dial(t)
		node := c.Node()
		if node == nil {
			t.Fatal("c.Node() returned nil after Dial")
		}
		if node.GetId() != "e2e-node-alpha" || node.GetName() != "alpha-box" {
			t.Fatalf("unexpected node identity: id=%q name=%q", node.GetId(), node.GetName())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		nodes, err := c.Nodes(ctx)
		if err != nil {
			t.Fatalf("c.Nodes: %v", err)
		}
		if len(nodes) == 0 || nodes[0].GetId() != "e2e-node-alpha" {
			t.Fatalf("unexpected nodes list: %+v", nodes)
		}
	})

	t.Run("cli_nodes", func(t *testing.T) {
		out := env.mustCLI("nodes")
		for _, want := range []string{"ID", "NAME", "PLATFORM", "alpha-box", "e2e-node"} {
			if !strings.Contains(out, want) {
				t.Fatalf("mako nodes missing %q in output:\n%s", want, out)
			}
		}
	})

	t.Run("cli_ls_empty", func(t *testing.T) {
		out := env.mustCLI("ls")
		if strings.TrimSpace(out) != "no sessions" {
			t.Fatalf("mako ls = %q, want %q", out, "no sessions")
		}
	})

	t.Run("cli_addrs_p2p_disabled", func(t *testing.T) {
		_, errOut, code := env.runCLI("addrs")
		if code != 1 {
			t.Fatalf("mako addrs with p2p.disabled exit code = %d, want 1 (stderr=%q)", code, errOut)
		}
		if !strings.Contains(errOut, "libp2p") {
			t.Fatalf("expected libp2p disabled message, got %q", errOut)
		}
	})

	t.Run("cli_plugins_empty_table", func(t *testing.T) {
		out := env.mustCLI("plugins")
		for _, col := range []string{"ID", "VERSION", "STATE", "ASKS FOR", "GRANTED"} {
			if !strings.Contains(out, col) {
				t.Fatalf("mako plugins missing column %q in:\n%s", col, out)
			}
		}
	})

	t.Run("cli_plugins_tools_empty", func(t *testing.T) {
		out := env.mustCLI("plugins", "tools")
		if !strings.Contains(out, "No plugin tools registered.") {
			t.Fatalf("mako plugins tools = %q, want 'No plugin tools registered.'", out)
		}
	})

	t.Run("clean_sigterm_shutdown", func(t *testing.T) {
		d.stop(t)
		_, _, code := env.runCLI("nodes")
		if code != 1 {
			t.Fatalf("expected mako nodes to fail after SIGTERM shutdown, got exit %d", code)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if _, err := client.Dial(ctx, d.sock); err == nil {
			t.Fatal("expected client.Dial to fail after daemon shutdown")
		}
	})
}
