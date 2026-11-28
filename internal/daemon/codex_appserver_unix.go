//go:build !windows

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ironpark/mako/internal/agent/codex"
)

type codexAppServerTransport struct {
	sockPath        string
	listenArg       string
	remoteArg       string
	extraServerArgs []string
	extraTUIArgs    []string
	extraTUIEnv     map[string]string
}

func newCodexAppServerTransport(runDir, sessionID string) (*codexAppServerTransport, error) {
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, err
	}
	sockPath := codexSocketPath(runDir, sessionID)
	_ = os.Remove(sockPath)
	uri := "unix://" + sockPath
	return &codexAppServerTransport{
		sockPath:  sockPath,
		listenArg: uri,
		remoteArg: uri,
	}, nil
}

// codexSocketPath is where session sessionID's codex app-server listens.
func codexSocketPath(runDir, sessionID string) string {
	sockPath := filepath.Join(runDir, "codex-"+sessionID+".sock")
	// Unix domain socket paths are limited to 104 bytes on macOS and 108 on Linux.
	if len(sockPath) > 96 {
		sockPath = filepath.Join(os.TempDir(), "mako-codex-"+sessionID+".sock")
	}
	if len(sockPath) > 96 {
		sockPath = filepath.Join("/tmp", "mako-codex-"+sessionID+".sock")
	}
	return sockPath
}

func (t *codexAppServerTransport) waitReady(ctx context.Context, procDone <-chan struct{}, stderr io.Reader, tail *stderrTail) (*codex.AppServerClient, string, error) {
	if stderr != nil {
		go func() {
			if tail != nil {
				_, _ = io.Copy(tail, stderr)
			} else {
				_, _ = io.Copy(io.Discard, stderr)
			}
		}()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if _, err := os.Stat(t.sockPath); err == nil {
			dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			client, err := codex.DialAppServerUnix(dialCtx, t.sockPath)
			cancel()
			if err == nil {
				return client, t.remoteArg, nil
			}
			lastErr = err
		}
		select {
		case <-procDone:
			// Give stderr reader a brief moment to drain final bytes on exit.
			time.Sleep(25 * time.Millisecond)
			if lastErr != nil {
				return nil, "", fmt.Errorf("codex app-server exited before ready: %w", lastErr)
			}
			return nil, "", errors.New("codex app-server exited before socket became ready")
		case <-ctx.Done():
			if lastErr != nil {
				return nil, "", fmt.Errorf("codex app-server socket timeout: %w", lastErr)
			}
			return nil, "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func (t *codexAppServerTransport) cleanupFiles() {
	if t != nil && t.sockPath != "" {
		_ = os.Remove(t.sockPath)
	}
}

func configureCodexServerCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func stopCodexServerCmd(cmd *exec.Cmd, exited <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if exited == nil {
		return
	}
	select {
	case <-exited:
		return
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	select {
	case <-exited:
	case <-time.After(500 * time.Millisecond):
	}
}
