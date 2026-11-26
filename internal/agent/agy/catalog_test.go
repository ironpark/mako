package agy

import (
	"slices"
	"strings"
	"testing"
)

func TestParseModelsOutput(t *testing.T) {
	raw := "Fetching available models...\n" +
		"gemini-4-neon-high\tGemini 4 Neon (High)\n" +
		"gemini-4-neon-xhigh\tGemini 4 Neon (XHigh)\n" +
		"gemini-4-neon-medium\tGemini 4 Neon (Medium)\n" +
		"gemini-4-neon-low\tGemini 4 Neon (Low)\n" +
		"gemini-4-carbon-medium\tGemini 4 Carbon (Medium)\n" +
		"gemini-4-carbon-high\tGemini 4 Carbon (High)\n" +
		"gemini-pro-orchestrator-neon\tOrchestrator (Neon)\n" +
		"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n" +
		"blank-name\t\n" +
		"ignored line without tab\n"
	got := parseModelsOutput(raw)
	if len(got) != 5 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	// Multi-variant model collapses into base model with exact supported efforts in canonical order.
	if got[0].ID != "gemini-4-neon" || got[0].Name != "Gemini 4 Neon" || !slices.Equal(got[0].Efforts, []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("neon = %+v", got[0])
	}
	if got[1].ID != "gemini-4-carbon" || got[1].Name != "Gemini 4 Carbon" || !slices.Equal(got[1].Efforts, []string{"medium", "high"}) {
		t.Fatalf("carbon = %+v", got[1])
	}
	// Non-effort variant stays standalone with no effort selector.
	if got[2].ID != "gemini-pro-orchestrator-neon" || got[2].Name != "Orchestrator (Neon)" || len(got[2].Efforts) != 0 {
		t.Fatalf("orchestrator = %+v", got[2])
	}
	// Single-variant model with effort suffix stays exact variant ID with no effort selector.
	if got[3].ID != "gpt-oss-120b-medium" || got[3].Name != "GPT-OSS 120B (Medium)" || len(got[3].Efforts) != 0 {
		t.Fatalf("gpt-oss = %+v", got[3])
	}
	if got[4].ID != "blank-name" || got[4].Name != "blank-name" || len(got[4].Efforts) != 0 {
		t.Fatalf("blank-name = %+v", got[4])
	}
}

func TestNormalizeModelEffort(t *testing.T) {
	a := New()
	for _, tc := range []struct {
		model, effort         string
		wantModel, wantEffort string
	}{
		{"gemini-4-neon", "low", "gemini-4-neon", "low"},
		{"gemini-4-neon", "", "gemini-4-neon", ""},
		{"gemini-4-neon-high", "low", "gemini-4-neon", "low"},
		{"gemini-4-neon-high", "high", "gemini-4-neon-high", ""},
		{"gpt-oss-120b-medium", "medium", "gpt-oss-120b-medium", ""},
		{"gemini-pro-orchestrator-neon", "high", "gemini-pro-orchestrator-neon", "high"},
	} {
		gm, ge := a.NormalizeModelEffort(tc.model, tc.effort)
		if gm != tc.wantModel || ge != tc.wantEffort {
			t.Errorf("NormalizeModelEffort(%q, %q) = (%q, %q), want (%q, %q)", tc.model, tc.effort, gm, ge, tc.wantModel, tc.wantEffort)
		}
	}
}

func TestDetectSSHSessionAndSignInDiagnostic(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")

	if v, ok := DetectSSHSession(nil); ok || v != "" {
		t.Fatalf("DetectSSHSession(empty) = (%q, %v), want (\"\", false)", v, ok)
	}
	if diag := SignInDiagnostic(nil); !strings.Contains(diag, "OS keyring") {
		t.Fatalf("local SignInDiagnostic = %q, want mention of OS keyring", diag)
	}

	if v, ok := DetectSSHSession(map[string]string{"SSH_CONNECTION": "10.0.0.1 51234 10.0.0.2 22"}); !ok || v != "SSH_CONNECTION" {
		t.Fatalf("DetectSSHSession(SSH_CONNECTION) = (%q, %v), want (SSH_CONNECTION, true)", v, ok)
	}
	if diag := SignInDiagnostic(map[string]string{"SSH_TTY": "/dev/pts/1"}); !strings.Contains(diag, "SSH_TTY") || !strings.Contains(diag, "token-file") {
		t.Fatalf("SSH SignInDiagnostic = %q, want SSH_TTY and token-file", diag)
	}

	// Explicit empty override masks inherited process env var.
	t.Setenv("SSH_CLIENT", "1.2.3.4 1234 22")
	if v, ok := DetectSSHSession(map[string]string{"SSH_CLIENT": ""}); ok || v != "" {
		t.Fatalf("explicit empty override should mask process SSH_CLIENT, got (%q, %v)", v, ok)
	}
	if v, ok := DetectSSHSession(nil); !ok || v != "SSH_CLIENT" {
		t.Fatalf("inherited process SSH_CLIENT = (%q, %v), want (SSH_CLIENT, true)", v, ok)
	}
}
