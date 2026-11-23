package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
)

var (
	repoRootDir string
	makoBin     string
	makodBin    string
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e TestMain: locate repo root: %v\n", err)
		return 1
	}
	repoRootDir = root

	binDir, err := os.MkdirTemp("", "mako-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e TestMain: mkdtemp: %v\n", err)
		return 1
	}
	defer os.RemoveAll(binDir)

	makoBin = filepath.Join(binDir, "mako")
	makodBin = filepath.Join(binDir, "makod")

	if err := buildBinary(root, "./cmd/mako", makoBin); err != nil {
		fmt.Fprintf(os.Stderr, "e2e TestMain: build mako: %v\n", err)
		return 1
	}
	if err := buildBinary(root, "./cmd/makod", makodBin); err != nil {
		fmt.Fprintf(os.Stderr, "e2e TestMain: build makod: %v\n", err)
		return 1
	}

	return m.Run()
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", wd)
		}
		dir = parent
	}
}

func buildBinary(root, pkg, out string) error {
	cmd := exec.Command("go", "build", "-tags", "makotest", "-o", out, pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if combined, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, string(combined))
	}
	return nil
}

// syncBuffer is a thread-safe bytes.Buffer for capturing daemon output while polling.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type testEnv struct {
	t        *testing.T
	makoHome string
	home     string
	tmpDir   string
	sockPath string
}

type testConfigOpts struct {
	NodeID string
	Name   string
	Agents map[string]any
}

func newTestEnv(t *testing.T, opts ...func(*testConfigOpts)) *testEnv {
	t.Helper()
	makoHome := t.TempDir()
	home := t.TempDir()
	// Keep TMPDIR short so Unix socket paths stay well under macOS sun_path limits (<= 104 bytes).
	tmpDir, err := os.MkdirTemp("", "mk-")
	if err != nil {
		t.Fatalf("create short TMPDIR: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	cfgOpts := testConfigOpts{
		NodeID: "test-node-1",
		Name:   "test-node",
	}
	for _, fn := range opts {
		fn(&cfgOpts)
	}

	e := &testEnv{
		t:        t,
		makoHome: makoHome,
		home:     home,
		tmpDir:   tmpDir,
		sockPath: filepath.Join(tmpDir, "daemon.sock"),
	}
	if len(e.sockPath) > 100 {
		t.Fatalf("daemon socket path too long (%d bytes): %s", len(e.sockPath), e.sockPath)
	}

	e.writeConfig(cfgOpts)
	return e
}

func (e *testEnv) writeConfig(opts testConfigOpts) {
	e.t.Helper()
	cfg := map[string]any{
		"node_id": opts.NodeID,
		"name":    opts.Name,
		"discovery": map[string]any{
			"mdns":      false,
			"tailscale": false,
		},
		"p2p": map[string]any{
			"disabled": true,
		},
		"ssh_key": "",
	}
	if len(opts.Agents) > 0 {
		cfg["agents"] = opts.Agents
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		e.t.Fatalf("marshal config.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(e.makoHome, "config.json"), append(data, '\n'), 0o600); err != nil {
		e.t.Fatalf("write config.json: %v", err)
	}
}

func (e *testEnv) environ() []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+5)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "MAKO_HOME", "HOME", "TMPDIR", "TERM", "SSH_AUTH_SOCK", "MAKO_PLUGIN_REGISTRY_TEST", "MAKO_PLUGIN_NET_TEST":
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"MAKO_HOME="+e.makoHome,
		"HOME="+e.home,
		"TMPDIR="+e.tmpDir,
		"TERM=dumb",
		"MAKO_PLUGIN_REGISTRY_TEST=1",
	)
	return out
}

func (e *testEnv) socketPath() string {
	prevHome := os.Getenv("MAKO_HOME")
	prevTmp := os.Getenv("TMPDIR")
	_ = os.Setenv("MAKO_HOME", e.makoHome)
	_ = os.Setenv("TMPDIR", e.tmpDir)
	defer func() {
		if prevHome == "" {
			_ = os.Unsetenv("MAKO_HOME")
		} else {
			_ = os.Setenv("MAKO_HOME", prevHome)
		}
		if prevTmp == "" {
			_ = os.Unsetenv("TMPDIR")
		} else {
			_ = os.Setenv("TMPDIR", prevTmp)
		}
	}()
	p, err := config.SocketPath("")
	if err != nil {
		e.t.Fatalf("config.SocketPath: %v", err)
	}
	return p
}

func (e *testEnv) runBin(bin string, args ...string) (stdout, stderr string, exitCode int) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = e.environ()
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return outBuf.String(), errBuf.String(), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return outBuf.String(), errBuf.String(), ee.ExitCode()
	}
	e.t.Fatalf("run %s %v: %v", bin, args, err)
	return "", "", -1
}

func (e *testEnv) runCLI(args ...string) (stdout, stderr string, exitCode int) {
	e.t.Helper()
	return e.runBin(makoBin, args...)
}

func (e *testEnv) mustCLI(args ...string) string {
	e.t.Helper()
	out, errOut, code := e.runCLI(args...)
	if code != 0 {
		e.t.Fatalf("mako %v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
	}
	return out
}

type daemonHandle struct {
	t      *testing.T
	env    *testEnv
	cmd    *exec.Cmd
	output *syncBuffer
	sock   string

	mu     sync.Mutex
	doneCh chan error
	exited bool
	exitEr error
}

func startDaemon(t *testing.T, env *testEnv) *daemonHandle {
	t.Helper()
	sock := env.socketPath()
	_ = os.Remove(sock)

	cmd := exec.Command(makodBin, "-v")
	cmd.Env = env.environ()
	buf := &syncBuffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start makod: %v", err)
	}

	h := &daemonHandle{
		t:      t,
		env:    env,
		cmd:    cmd,
		output: buf,
		sock:   sock,
		doneCh: make(chan error, 1),
	}
	go func() {
		h.doneCh <- cmd.Wait()
	}()

	t.Cleanup(func() {
		h.cleanup()
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-h.doneCh:
			h.mu.Lock()
			h.exited = true
			h.exitEr = err
			h.mu.Unlock()
			t.Fatalf("makod exited before becoming ready (%v):\n%s", err, buf.String())
		default:
		}
		if _, err := os.Stat(sock); err == nil {
			dialCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			c, err := client.Dial(dialCtx, sock)
			cancel()
			if err == nil {
				_ = c.Close()
				return h
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for makod readiness at %s:\n%s", sock, buf.String())
	return nil
}

func (h *daemonHandle) stop(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	already := h.exited
	h.mu.Unlock()
	if already {
		return
	}
	if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("signal makod SIGTERM: %v", err)
	}
	select {
	case err := <-h.doneCh:
		h.mu.Lock()
		h.exited = true
		h.exitEr = err
		h.mu.Unlock()
		if err != nil {
			t.Fatalf("makod did not exit cleanly on SIGTERM: %v\nlogs:\n%s", err, h.output.String())
		}
	case <-time.After(10 * time.Second):
		_ = h.cmd.Process.Kill()
		<-h.doneCh
		h.mu.Lock()
		h.exited = true
		h.mu.Unlock()
		t.Fatalf("makod hung after SIGTERM and had to be killed\nlogs:\n%s", h.output.String())
	}
	if _, err := os.Stat(h.sock); err == nil {
		// Wait briefly in case socket removal happens right before process exits, else report.
		time.Sleep(50 * time.Millisecond)
		if _, err2 := os.Stat(h.sock); err2 == nil {
			t.Fatalf("daemon socket %s still exists after clean exit", h.sock)
		}
	}
}

func (h *daemonHandle) cleanup() {
	h.mu.Lock()
	already := h.exited
	h.mu.Unlock()
	if !already && h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-h.doneCh:
			h.mu.Lock()
			h.exited = true
			h.exitEr = err
			h.mu.Unlock()
		case <-time.After(5 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.doneCh
			h.mu.Lock()
			h.exited = true
			h.mu.Unlock()
		}
	}
	_ = os.Remove(h.sock)
}

func (h *daemonHandle) dial(t *testing.T) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, h.sock)
	if err != nil {
		t.Fatalf("client.Dial(%s): %v\nlogs:\n%s", h.sock, err, h.output.String())
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("readdir %s: %v", src, err)
	}
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		sp := filepath.Join(src, ent.Name())
		dp := filepath.Join(dst, ent.Name())
		if ent.IsDir() {
			copyDir(t, sp, dp)
			continue
		}
		in, err := os.Open(sp)
		if err != nil {
			t.Fatalf("open %s: %v", sp, err)
		}
		out, err := os.Create(dp)
		if err != nil {
			_ = in.Close()
			t.Fatalf("create %s: %v", dp, err)
		}
		_, err = io.Copy(out, in)
		_ = in.Close()
		if closeErr := out.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("copy %s -> %s: %v", sp, dp, err)
		}
	}
}
