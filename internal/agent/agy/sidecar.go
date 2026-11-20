package agy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/pkg/fsutil"
)

const deliverTimeout = 10 * time.Second

var sidecarMu sync.Mutex

// DeliverMessage pushes a message natively into an active agy conversation via
// `agy agentapi send-message`, waking the conversation reactively without
// typing into its PTY.
func (*Agent) DeliverMessage(ctx context.Context, conversationID, title, text string, env map[string]string) error {
	if strings.TrimSpace(conversationID) == "" {
		return errors.New("agy: missing conversation ID")
	}
	bin := resolveAgentAPI(env)
	var args []string
	if filepath.Base(bin) == "agentapi" {
		args = append(args, "send-message")
	} else {
		args = append(args, "agentapi", "send-message")
	}
	if title != "" {
		args = append(args, "--title="+title)
	}
	args = append(args, conversationID, text)

	tctx, cancel := context.WithTimeout(ctx, deliverTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, bin, args...)
	if len(env) > 0 {
		cmd.Env = mergeEnv(os.Environ(), env)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("agy agentapi send-message: %w: %s", err, msg)
		}
		return fmt.Errorf("agy agentapi send-message: %w", err)
	}
	return nil
}

func resolveAgentAPI(env map[string]string) string {
	home := env["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		wrapper := filepath.Join(home, ".gemini", "antigravity-cli", "bin", "agentapi")
		if fi, err := os.Stat(wrapper); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return wrapper
		}
	}
	if p, err := agent.Find("agy"); err == nil {
		return p
	}
	return "agy"
}

// sidecarFile is Mako's managed agy sidecar manifest path, honoring HOME in env.
func sidecarFile(env map[string]string) (string, error) {
	if home := env["HOME"]; home != "" {
		return filepath.Join(home, ".gemini", "config", "sidecars", "mako-bridge", "sidecar.json"), nil
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "config", "sidecars", "mako-bridge", "sidecar.json"), err
}

// EnsureSidecar writes Mako's sidecar.json manifest under agy's sidecars
// directory so Antigravity's sidecar supervisor can run Mako's native
// message/event bridge alongside agy sessions.
func EnsureSidecar(env map[string]string, command string, args []string) error {
	if command == "" {
		return nil
	}
	path, err := sidecarFile(env)
	if err != nil {
		return err
	}
	sidecarMu.Lock()
	defer sidecarMu.Unlock()

	doc := map[string]any{
		"display_name":   "Mako Agent Bridge",
		"description":    "Bridges Mako multi-agent messages and notifications directly into Antigravity conversations via agentapi.",
		"command":        command,
		"args":           args,
		"restart_policy": "always",
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		var was bytes.Buffer
		if json.Compact(&was, bytes.TrimPrefix(existing, []byte("\xef\xbb\xbf"))) == nil && bytes.Equal(was.Bytes(), encoded) {
			return enableSidecarConfig(filepath.Dir(filepath.Dir(filepath.Dir(path))), "mako-bridge")
		}
	}
	pretty, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := fsutil.Write(path, append(pretty, '\n')); err != nil {
		return err
	}
	return enableSidecarConfig(filepath.Dir(filepath.Dir(filepath.Dir(path))), "mako-bridge")
}

// enableSidecarConfig ensures ~/.gemini/config/config.json enables sidecarID
// unless the user already configured an entry for it.
func enableSidecarConfig(configDir, sidecarID string) error {
	path := filepath.Join(configDir, "config.json")
	doc := map[string]any{}
	if existing, err := os.ReadFile(path); err == nil {
		trimmed := bytes.TrimSpace(bytes.TrimPrefix(existing, []byte("\xef\xbb\xbf")))
		if len(trimmed) > 0 {
			if err := json.Unmarshal(trimmed, &doc); err != nil {
				return nil // leave user's malformed config untouched
			}
		}
	}
	sidecars, ok := doc["sidecars"].(map[string]any)
	if !ok || sidecars == nil {
		sidecars = map[string]any{}
		doc["sidecars"] = sidecars
	}
	if _, exists := sidecars[sidecarID]; exists {
		return nil
	}
	sidecars[sidecarID] = map[string]any{"enabled": true}
	pretty, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.Write(path, append(pretty, '\n'))
}
