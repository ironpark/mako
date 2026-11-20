//go:build windows

package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ironpark/mako/internal/agent/codex"
)

const codexWSTokenEnv = "MAKO_CODEX_WS_TOKEN"

var codexWSListenRe = regexp.MustCompile(`ws://127\.0\.0\.1:\d+`)

type codexAppServerTransport struct {
	tokPath         string
	token           string
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
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw[:])
	tokPath := filepath.Join(runDir, "codex-"+sessionID+".tok")
	if err := os.WriteFile(tokPath, []byte(token), 0o600); err != nil {
		return nil, err
	}
	return &codexAppServerTransport{
		tokPath:         tokPath,
		token:           token,
		listenArg:       "ws://127.0.0.1:0",
		extraServerArgs: []string{"--ws-auth", "capability-token", "--ws-token-file", tokPath},
		extraTUIArgs:    []string{"--remote-auth-token-env", codexWSTokenEnv},
		extraTUIEnv:     map[string]string{codexWSTokenEnv: token},
	}, nil
}

func (t *codexAppServerTransport) waitReady(ctx context.Context, procDone <-chan struct{}, stderr io.Reader) (*codex.AppServerClient, string, error) {
	if stderr == nil {
		return nil, "", errors.New("codex app-server: missing stderr pipe for ws discovery")
	}
	found := make(chan string, 1)
	go func() {
		br := bufio.NewReader(stderr)
		var sent bool
		for !sent {
			line, err := br.ReadString('\n')
			if url := codexWSListenRe.FindString(line); url != "" {
				sent = true
				found <- url
				break
			}
			if err != nil {
				break
			}
		}
		if !sent {
			close(found)
		}
		// Keep draining stderr for the server's lifetime so it can never block on a full pipe.
		_, _ = io.Copy(io.Discard, br)
	}()

	var wsURL string
	select {
	case url, ok := <-found:
		if !ok || url == "" {
			return nil, "", errors.New("codex app-server exited without printing ws listen address")
		}
		wsURL = url
	case <-procDone:
		return nil, "", errors.New("codex app-server exited before ws listener was ready")
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := codex.DialAppServerWS(dialCtx, wsURL, t.token)
	if err != nil {
		return nil, "", fmt.Errorf("codex app-server ws dial %s: %w", wsURL, err)
	}
	t.remoteArg = wsURL
	return client, wsURL, nil
}

func (t *codexAppServerTransport) cleanupFiles() {
	if t != nil && t.tokPath != "" {
		_ = os.Remove(t.tokPath)
	}
}

func configureCodexServerCmd(*exec.Cmd) {}

func stopCodexServerCmd(cmd *exec.Cmd, exited <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	if exited == nil {
		return
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
	}
}
