package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/decision"
	"github.com/ironpark/mako/internal/protocol"
)

func TestProviderSettingsRendersAllFourProviders(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.providers.Asked = true
	m.providers.Info.Set(&protocol.Providers{
		Secrets:         "file",
		SecretsNote:     "test mode",
		DefaultProvider: decision.ProviderTypeSafe,
		DefaultModel:    "jev-latest",
		Providers: []*protocol.ProviderInfo{
			{Id: decision.ProviderOpenRouter, Set: true, DefaultModel: "typesafe/jev-latest"},
			{Id: decision.ProviderTypeSafe, EnvSet: true, DefaultModel: "jev-latest"},
			{Id: decision.ProviderOpenAI, DefaultModel: "gpt-4.1-mini"},
			{Id: decision.ProviderGoogle, DefaultModel: "gemini-2.5-flash"},
		},
	})
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(providerSettings(m)) }, ggui.Sz(900, 1000))
	defer p.Close()
	p.Frame()

	sem := p.Semantics().String()
	for _, want := range []string{"OpenRouter", "TypeSafe AI", "OpenAI", "Google AI Studio", "OPENROUTER_API_KEY", "TYPESAFE_API_KEY"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("missing %q in provider settings:\n%s", want, sem)
		}
	}
	if _, ok := p.Semantics().Find(ggui.RoleButton, Tf("Clear %s API key", "OpenRouter")); !ok {
		t.Fatalf("expected Clear button for configured OpenRouter key:\n%s", sem)
	}
}
