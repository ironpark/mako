//go:build darwin

package usage

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/safeexec"
)

const claudeKeychainTimeout = 2 * time.Second

func readClaudeKeychainPlatform(ctx context.Context, service, account string) (string, error) {
	lookups := [][]string{
		{"find-generic-password", "-a", account, "-w", "-s", service},
		{"find-generic-password", "-w", "-s", service},
	}
	for _, args := range lookups {
		tctx, cancel := context.WithTimeout(ctx, claudeKeychainTimeout)
		cmd := exec.CommandContext(tctx, "security", args...)
		out, err := safeexec.Output(cmd)
		cancel()
		if err != nil {
			continue
		}
		if raw := strings.TrimSpace(string(out)); raw != "" {
			return raw, nil
		}
	}
	return "", nil
}
