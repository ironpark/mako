package sshremote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/safeexec"
	"github.com/ironpark/mako/internal/transport"
)

// DaemonTunnel maintains an OpenSSH Unix-socket tunnel (`ssh -N -L`) to a
// remote makod daemon running on an SSH host.
type DaemonTunnel struct {
	Host       config.SSHHost
	LocalSock  string
	RemoteSock string
	RemoteNode *protocol.Node

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// ensureRemoteMakodScript starts makod in the background on the remote host if
// its daemon socket is not already listening, then prints the daemon socket path.
func ensureRemoteMakodScript(makodPath string) string {
	// Resolve remote config/run dir the same way internal/config does on Linux/macOS:
	// macOS: ~/Library/Application Support/mako/run/daemon.sock (or /tmp/mako-... if long)
	// Linux: ${XDG_CONFIG_HOME:-$HOME/.config}/mako/run/daemon.sock
	// We launch makod with MAKO_HOME="$HOME/.mako/remote" when no running makod
	// is found so its socket path is always short (< 100 bytes) and predictable.
	var b strings.Builder
	b.WriteString(remotePathPrelude)
	b.WriteString("; ")
	fmt.Fprintf(&b, "_bin=%s; ", ShellQuote(makodPath))
	b.WriteString(`_os="$(uname -s 2>/dev/null)"; `)
	b.WriteString(`if [ "$_os" = "Darwin" ]; then _cfg="$HOME/Library/Application Support/mako"; else _cfg="${XDG_CONFIG_HOME:-$HOME/.config}/mako"; fi; `)
	b.WriteString(`_sock="$_cfg/run/daemon.sock"; `)
	// Check if an existing makod socket is already present and responsive (or check ~/.mako-ssh/run/daemon.sock).
	b.WriteString(`if [ -S "$_sock" ]; then printf 'SOCK=%s\n' "$_sock"; exit 0; fi; `)
	b.WriteString(`_alt="$HOME/.mako-ssh"; _altsock="$_alt/run/daemon.sock"; `)
	b.WriteString(`if [ -S "$_altsock" ]; then printf 'SOCK=%s\n' "$_altsock"; exit 0; fi; `)
	b.WriteString(`mkdir -p "$_alt/run" && chmod 700 "$_alt" "$_alt/run"; `)
	b.WriteString(`MAKO_HOME="$_alt" nohup "$_bin" >/dev/null 2>"$_alt/makod.log" </dev/null & `)
	b.WriteString(`_i=0; while [ $_i -lt 50 ]; do [ -S "$_altsock" ] && { printf 'SOCK=%s\n' "$_altsock"; exit 0; }; sleep 0.1; _i=$((_i + 1)); done; `)
	b.WriteString(`echo "remote makod did not open its socket" >&2; [ -f "$_alt/makod.log" ] && tail -n 5 "$_alt/makod.log" >&2; exit 1`)
	return b.String()
}

// StartDaemonTunnel ensures makod is running on the remote host, starts an SSH
// local Unix-socket forwarder to it, and verifies the forwarded daemon responds
// to a mako protocol Hello/Welcome handshake.
func StartDaemonTunnel(ctx context.Context, h config.SSHHost, makodPath, controlPath, localSock string, hello *protocol.Hello) (*DaemonTunnel, error) {
	if makodPath == "" {
		return nil, errors.New("remote makod binary not found")
	}
	// Step 1: Ensure remote makod is running and discover its Unix socket path.
	ensureArgs, err := BaseArgs(h, Options{ControlPath: controlPath})
	if err != nil {
		return nil, err
	}
	ensureArgs = append(ensureArgs, "sh -c "+ShellQuote(ensureRemoteMakodScript(makodPath)))
	var stdout, stderr bytes.Buffer
	ensureCmd := exec.CommandContext(ctx, SSHBinary, ensureArgs...)
	ensureCmd.Stdout = &stdout
	ensureCmd.Stderr = &stderr
	if err := safeexec.Run(ensureCmd); err != nil {
		return nil, fmt.Errorf("start remote makod: %s", CleanSSHError(stderr.String(), err))
	}
	var remoteSock string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "SOCK="); ok {
			remoteSock = strings.TrimSpace(v)
			break
		}
	}
	if remoteSock == "" {
		return nil, errors.New("remote makod did not report its socket path")
	}

	// Step 2: Start `ssh -N -L localSock:remoteSock`.
	_ = os.Remove(localSock)
	fwdArgs, err := BaseArgs(h, Options{NoControlMaster: true})
	if err != nil {
		return nil, err
	}
	// Prepend tunnel options before the base args.
	fullArgs := append([]string{
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-L", localSock + ":" + remoteSock,
	}, fwdArgs...)

	tctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(tctx, SSHBinary, fullArgs...)
	var tunStderr bytes.Buffer
	cmd.Stderr = &tunStderr
	if err := safeexec.Start(cmd); err != nil {
		cancel()
		return nil, fmt.Errorf("start ssh tunnel: %w", err)
	}

	tun := &DaemonTunnel{
		Host:       h,
		LocalSock:  localSock,
		RemoteSock: remoteSock,
		cmd:        cmd,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		tun.mu.Lock()
		tun.err = err
		tun.mu.Unlock()
		_ = os.Remove(localSock)
		close(tun.done)
	}()

	// Step 3: Wait for localSock to become dialable and complete mako Hello/Welcome.
	deadline := time.Now().Add(10 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-tun.done:
			tun.mu.Lock()
			werr := tun.err
			tun.mu.Unlock()
			return nil, fmt.Errorf("ssh tunnel exited: %s", CleanSSHError(tunStderr.String(), werr))
		case <-ctx.Done():
			tun.Close()
			return nil, ctx.Err()
		default:
		}
		if _, err := os.Stat(localSock); err == nil {
			dialCtx, dialCancel := context.WithTimeout(ctx, 3*time.Second)
			s, node, err := transport.Dial(dialCtx, localSock, hello)
			dialCancel()
			if err == nil {
				_ = s.Close()
				tun.RemoteNode = node
				return tun, nil
			}
			lastErr = err
		}
		time.Sleep(80 * time.Millisecond)
	}
	tun.Close()
	if lastErr != nil {
		return nil, fmt.Errorf("ssh tunnel to remote makod failed: %w", lastErr)
	}
	return nil, errors.New("timed out waiting for ssh tunnel socket")
}

// Dial opens a raw Unix-socket stream to the forwarded remote makod.
func (t *DaemonTunnel) Dial(ctx context.Context) (*transport.Stream, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", t.LocalSock)
	if err != nil {
		return nil, err
	}
	return transport.NewPeerStream(conn), nil
}

// Done is closed when the SSH tunnel process exits.
func (t *DaemonTunnel) Done() <-chan struct{} { return t.done }

// Err returns the tunnel exit error after Done is closed.
func (t *DaemonTunnel) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// Close terminates the SSH tunnel process and removes the local Unix socket.
func (t *DaemonTunnel) Close() error {
	t.cancel()
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
	}
	_ = os.Remove(t.LocalSock)
	return nil
}

// stdioConn adapts an exec.Cmd's stdin/stdout (such as `ssh -W host:port`)
// into an io.ReadWriteCloser for port-forwarding tunnels.
type stdioConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	once   sync.Once
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *stdioConn) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.cmd.Wait()
	})
	return nil
}

// OpenPortTunnel opens a TCP forwarding stream to 127.0.0.1:<port> on the SSH
// host using OpenSSH's `-W` stdio forwarding.
func OpenPortTunnel(ctx context.Context, h config.SSHHost, controlPath string, port int) (io.ReadWriteCloser, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("%d is not a TCP port", port)
	}
	base, err := BaseArgs(h, Options{ControlPath: controlPath})
	if err != nil {
		return nil, err
	}
	args := append([]string{"-W", fmt.Sprintf("127.0.0.1:%d", port)}, base...)
	cmd := exec.CommandContext(ctx, SSHBinary, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := safeexec.Start(cmd); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return &stdioConn{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}
