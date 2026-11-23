package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
)

func TestMain(m *testing.M) {
	if os.Getenv("MAKOD_TEST_HELPER") == "1" {
		_ = os.Unsetenv("MAKOD_TEST_HELPER")
		if raw := os.Getenv("MAKOD_TEST_ARGS"); raw != "" {
			var args []string
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				_, _ = os.Stderr.WriteString("unmarshal MAKOD_TEST_ARGS: " + err.Error() + "\n")
				os.Exit(2)
			}
			os.Args = args
		}
		if os.Getenv("MAKOD_TEST_CLOSE_STDIN") == "1" {
			if r, w, err := os.Pipe(); err == nil {
				_ = w.Close()
				os.Stdin = r
			}
		}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type makodEnv struct {
	home     string
	makoHome string
	runDir   string
	sockPath string
}

func setupMakodEnv(t *testing.T, configJSON string) makodEnv {
	t.Helper()
	home := t.TempDir()
	makoHome := t.TempDir()

	if configJSON != "" {
		if err := os.WriteFile(filepath.Join(makoHome, "config.json"), []byte(configJSON), 0o600); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
	}

	runDir, err := config.RunDir(makoHome)
	if err != nil {
		t.Fatalf("config.RunDir: %v", err)
	}
	sockPath, err := config.SocketPath(makoHome)
	if err != nil {
		t.Fatalf("config.SocketPath: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(runDir)
	})

	return makodEnv{
		home:     home,
		makoHome: makoHome,
		runDir:   runDir,
		sockPath: sockPath,
	}
}

func helperEnv(env makodEnv, args []string, closeStdin bool) ([]string, error) {
	rawArgs, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{
		"HOME":                   true,
		"USERPROFILE":            true,
		"XDG_CONFIG_HOME":        true,
		"MAKO_HOME":              true,
		"SSH_AUTH_SOCK":          true,
		"TERM":                   true,
		"MAKO_SESSION":           true,
		"MAKOD_TEST_HELPER":      true,
		"MAKOD_TEST_ARGS":        true,
		"MAKOD_TEST_CLOSE_STDIN": true,
	}
	var out []string
	for _, entry := range os.Environ() {
		k, _, ok := strings.Cut(entry, "=")
		if ok && skip[k] {
			continue
		}
		out = append(out, entry)
	}
	out = append(out,
		"HOME="+env.home,
		"USERPROFILE="+env.home,
		"XDG_CONFIG_HOME="+filepath.Join(env.home, ".config"),
		"MAKO_HOME="+env.makoHome,
		"SSH_AUTH_SOCK=",
		"TERM=dumb",
		"MAKO_SESSION=",
		"MAKOD_TEST_HELPER=1",
		"MAKOD_TEST_ARGS="+string(rawArgs),
	)
	if closeStdin {
		out = append(out, "MAKOD_TEST_CLOSE_STDIN=1")
	}
	return out, nil
}

func runMakodHelper(t *testing.T, env makodEnv, args []string, closeStdin bool) (stdout, stderr string, exitCode int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmdEnv, err := helperEnv(env, args, closeStdin)
	if err != nil {
		t.Fatalf("helperEnv: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = cmdEnv
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return outBuf.String(), errBuf.String(), exitErr.ExitCode()
		}
		t.Fatalf("run helper %v: %v (stderr=%q)", args, err, errBuf.String())
	}
	return outBuf.String(), errBuf.String(), 0
}

func startMakodHelper(t *testing.T, env makodEnv, args []string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer, <-chan error, <-chan struct{}) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmdEnv, err := helperEnv(env, args, false)
	if err != nil {
		t.Fatalf("helperEnv: %v", err)
	}

	var outBuf, errBuf bytes.Buffer
	cmd := exec.Command(exe)
	cmd.Env = cmdEnv
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper %v: %v", args, err)
	}

	waitCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		waitCh <- cmd.Wait()
		close(done)
	}()

	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		_ = cmd.Process.Kill()
		<-done
	})

	return cmd, &outBuf, &errBuf, waitCh, done
}

func dialUntilReady(t *testing.T, sockPath string, done <-chan struct{}, waitCh <-chan error, stderr *bytes.Buffer) *client.Client {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			err := <-waitCh
			t.Fatalf("makod exited early (%v), stderr:\n%s", err, stderr.String())
		default:
		}
		if _, err := os.Stat(sockPath); err == nil {
			dialCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			c, err := client.Dial(dialCtx, sockPath)
			cancel()
			if err == nil {
				return c
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for daemon socket %q", sockPath)
	return nil
}

func TestMakodVersionFlag(t *testing.T) {
	env := setupMakodEnv(t, "")
	stdout, stderr, code := runMakodHelper(t, env, []string{"makod", "-version"}, false)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	want := "makod " + config.Version + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestMakodHelpAndUnknownFlag(t *testing.T) {
	env := setupMakodEnv(t, "")

	stdout, stderr, code := runMakodHelper(t, env, []string{"makod", "-h"}, false)
	if code != 0 {
		t.Fatalf("-h exit code = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	for _, wantFlag := range []string{"-listen", "-name", "-v", "-version"} {
		if !strings.Contains(stderr, wantFlag) {
			t.Fatalf("-h stderr missing %q:\n%s", wantFlag, stderr)
		}
	}

	_, stderr, code = runMakodHelper(t, env, []string{"makod", "-unknown-flag"}, false)
	if code == 0 {
		t.Fatal("-unknown-flag exited 0, want non-zero")
	}
	if !strings.Contains(stderr, "unknown-flag") {
		t.Fatalf("-unknown-flag stderr missing diagnostic:\n%s", stderr)
	}
}

func TestMakodMalformedConfig(t *testing.T) {
	env := setupMakodEnv(t, "{")
	_, stderr, code := runMakodHelper(t, env, []string{"makod"}, false)
	if code == 0 {
		t.Fatal("malformed config exited 0, want non-zero")
	}
	if !strings.Contains(stderr, "load config") {
		t.Fatalf("malformed config stderr missing %q:\n%s", "load config", stderr)
	}
}

func TestMakodStartupNameVerboseAndInterrupt(t *testing.T) {
	cfgJSON := `{"node_id":"makod-test-node","name":"orig","p2p":{"disabled":true},"discovery":{"mdns":false,"tailscale":false}}`
	env := setupMakodEnv(t, cfgJSON)

	cmd, _, errBuf, waitCh, done := startMakodHelper(t, env, []string{"makod", "-name", "override-name", "-v"})
	c := dialUntilReady(t, env.sockPath, done, waitCh, errBuf)
	node := c.Node()
	_ = c.Close()

	if node.GetId() != "makod-test-node" {
		t.Fatalf("node.Id = %q, want %q", node.GetId(), "makod-test-node")
	}
	if node.GetName() != "override-name" {
		t.Fatalf("node.Name = %q, want %q", node.GetName(), "override-name")
	}

	// Send a malformed frame so serveStream logs a debug message before closing the connection.
	uc, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: env.sockPath, Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	_, _ = uc.Write([]byte{0, 0, 0, 1, 0xff})
	_ = uc.CloseWrite()
	_, _ = io.ReadAll(uc)
	_ = uc.Close()

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal interrupt: %v", err)
	}
	select {
	case <-done:
		if err := <-waitCh; err != nil {
			t.Fatalf("makod exit after Interrupt = %v\nstderr:\n%s", err, errBuf.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for makod clean shutdown")
	}

	stderr := errBuf.String()
	if !strings.Contains(stderr, "daemon listening") {
		t.Fatalf("stderr missing %q:\n%s", "daemon listening", stderr)
	}
	if !strings.Contains(stderr, "level=DEBUG") {
		t.Fatalf("stderr missing %q:\n%s", "level=DEBUG", stderr)
	}
}

func TestMakodListenFlagStartsP2P(t *testing.T) {
	cfgJSON := `{"node_id":"makod-p2p-node","name":"p2p-node","p2p":{"disabled":false},"discovery":{"mdns":false,"tailscale":false}}`
	env := setupMakodEnv(t, cfgJSON)

	cmd, _, errBuf, waitCh, done := startMakodHelper(t, env, []string{"makod", "-listen", "127.0.0.1:0"})
	c := dialUntilReady(t, env.sockPath, done, waitCh, errBuf)
	node := c.Node()
	_ = c.Close()

	if node.GetPeerId() == "" {
		t.Fatal("node.PeerId is empty with P2P enabled")
	}
	if !strings.HasPrefix(node.GetAddr(), "127.0.0.1:") {
		t.Fatalf("node.Addr = %q, want prefix %q", node.GetAddr(), "127.0.0.1:")
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal interrupt: %v", err)
	}
	select {
	case <-done:
		if err := <-waitCh; err != nil {
			t.Fatalf("makod exit after Interrupt = %v\nstderr:\n%s", err, errBuf.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for makod P2P clean shutdown")
	}
}

func TestMakodHookDispatch(t *testing.T) {
	env := setupMakodEnv(t, "")
	stdout, stderr, code := runMakodHelper(t, env, []string{"makod", "hook"}, true)
	if code != 0 {
		t.Fatalf("makod hook exit code = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if _, err := os.Stat(env.sockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no daemon socket %q to be created, stat err = %v", env.sockPath, err)
	}
}
