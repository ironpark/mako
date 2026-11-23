//go:build unix

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/decision"
	"github.com/ironpark/mako/internal/keychain"
	"github.com/ironpark/mako/internal/plugin"
)

func TestPluginAsksChoicesAndStripYolo(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	claudeBin := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
yolo="0"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dangerously-skip-permissions) yolo="1"; shift ;;
    *) shift ;;
  esac
done
prompt="$(cat)"
printf 'yolo=%s|prompt=%s' "$yolo" "$prompt"
`
	if err := os.WriteFile(claudeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	on := true
	off := false
	d := meshDaemonWith(t, "plugin-asks", func(cfg *config.Config) {
		cfg.Agents = map[string]agentspec.Agent{
			"claude": {
				Command: []string{claudeBin},
				Enabled: &on,
				Yolo:    true,
				Profiles: []agentspec.Profile{
					{
						Name:        "explorer",
						Description: "Explores docs",
						Model:       "sonnet",
						Yolo:        &on,
						Args:        []string{"--dangerously-skip-permissions"},
					},
					{
						Name:          "no-auto",
						AllowTemplate: &off,
					},
				},
			},
		}
	})

	host := pluginAsks{d: d}
	choices := host.Choices()
	var foundExplorer, foundNoAuto bool
	for _, c := range choices {
		if c.Agent == "claude" && c.Profile == "explorer" {
			foundExplorer = true
			if !c.Installed || !c.Askable || c.Reason != "" || c.Description != "Explores docs" || c.Model != "sonnet" {
				t.Fatalf("unexpected explorer choice: %+v", c)
			}
		}
		if c.Agent == "claude" && c.Profile == "no-auto" {
			foundNoAuto = true
			if c.Askable || !strings.Contains(c.Reason, "allow_template") {
				t.Fatalf("unexpected no-auto choice: %+v", c)
			}
		}
	}
	if !foundExplorer || !foundNoAuto {
		t.Fatalf("missing expected choices: explorer=%v no-auto=%v all=%+v", foundExplorer, foundNoAuto, choices)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	out, err := host.Ask(ctx, "okf", plugin.AgentQuestion{
		Agent:   "claude",
		Profile: "explorer",
		Root:    workDir,
		Prompt:  "Explain storage",
		Format:  "text",
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("pluginAsks.Ask: %v", err)
	}
	if !strings.HasPrefix(out, "yolo=0|prompt=[Question from the Mako plugin okf:") {
		t.Fatalf("expected yolo stripped and plugin header prepended, got: %q", out)
	}
	if !strings.Contains(out, "Explain storage") {
		t.Fatalf("prompt body missing from output: %q", out)
	}
}

func TestPluginDecisionsStatusAndUnavailable(t *testing.T) {
	// Clear decision env keys so the empty vault has no configured key.
	for _, env := range []string{"TYPESAFE_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY"} {
		t.Setenv(env, "")
	}
	d := meshDaemon(t, "plugin-dec")
	d.decisions = decision.NewStore(t.TempDir(), &keychain.Fake{}, "")

	host := pluginDecisions{d: d}
	st := host.Status()
	if st.Available || st.Provider != "" || st.Model != "" {
		t.Fatalf("expected unavailable status with empty vault, got %+v", st)
	}
	_, _, _, err := host.Evaluate(t.Context(), "okf", []byte(`{"state":"hi","type":"noul","instructions":"ok?"}`))
	if !errors.Is(err, plugin.ErrDecisionUnavailable) {
		t.Fatalf("expected ErrDecisionUnavailable, got %v", err)
	}
}
