package gui

import (
	"context"
	"image/color"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/decision"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

type providersState struct {
	Info    *ggui.StateValue[*protocol.Providers]
	Loading *ggui.StateValue[bool]
	Note    *ggui.StateValue[string]
	Asked   bool
}

func newProvidersState() *providersState {
	return &providersState{
		Info: ggui.State((*protocol.Providers)(nil)).WithEqual(func(a, b *protocol.Providers) bool {
			return proto.Equal(a, b)
		}),
		Loading: ggui.State(false),
		Note:    ggui.State(""),
	}
}

type providerMeta struct {
	ID, Name, Host, EnvHint string
}

var providerCatalog = []providerMeta{
	{decision.ProviderOpenRouter, "OpenRouter", "openrouter.ai", "OPENROUTER_API_KEY"},
	{decision.ProviderTypeSafe, "TypeSafe AI", "typesafe.ai", "TYPESAFE_API_KEY"},
	{decision.ProviderOpenAI, "OpenAI", "api.openai.com", "OPENAI_API_KEY"},
	{decision.ProviderGoogle, "Google AI Studio", "ai.google.dev", "GEMINI_API_KEY / GOOGLE_API_KEY"},
}

func (m *model) ensureProvidersLoaded() {
	if m.providers == nil || m.providers.Asked || ggui.Peek(m.providers.Loading) || m.post == nil {
		return
	}
	m.providers.Asked = true
	m.loadProviders()
}

func (m *model) loadProviders() {
	if m.providers == nil || m.post == nil {
		return
	}
	m.providers.Loading.Set(true)
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Providers, error) {
		return c.ListProviders(ctx)
	}, func(res *protocol.Providers, err error) {
		m.providers.Loading.Set(false)
		if err != nil {
			m.providers.Note.Set(errorText(err))
			return
		}
		m.providers.Note.Set("")
		m.providers.Info.Set(res)
	})
}

func (m *model) actOnProvider(req *protocol.ProviderAction, onOK func()) {
	if m.providers == nil || m.post == nil {
		return
	}
	m.providers.Note.Set("")
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Providers, error) {
		return c.ActProvider(ctx, req)
	}, func(res *protocol.Providers, err error) {
		if err != nil {
			m.providers.Note.Set(errorText(err))
			return
		}
		m.providers.Info.Set(res)
		if onOK != nil {
			onOK()
		}
	})
}

func providerSettings(m *model) ggui.Widget {
	m.ensureProvidersLoaded()
	content := ggui.Reactive(func() ggui.Widget {
		info := m.providers.Info.Get()
		loading := m.providers.Loading.Get()
		note := m.providers.Note.Get()
		var children []ggui.Widget

		if note != "" {
			children = append(children, card(widget.Box(warnLine("circle-slash", skin.DiffRemoved, note)).Pad(12, 16)))
		}
		if info != nil && info.GetSecrets() == "file" {
			text := T("This node has no system keychain, so API keys are kept in a file that only you can read.")
			if info.GetSecretsNote() != "" {
				text += " (" + info.GetSecretsNote() + ")"
			}
			children = append(children, card(widget.Box(warnLine("key-round", skin.DotWaiting, text)).Pad(12, 16)))
		} else if info != nil && info.GetSecrets() != "" {
			children = append(children, card(widget.Box(
				widget.Row(
					icon("key-round").Size(14).Color(skin.Muted),
					muted(widget.Text(Tf("Keys are stored securely in %s on this node.", info.GetSecrets())).Size(12)),
				).Gap(8).Align(widget.AlignCenter),
			).Pad(10, 16)))
		}

		if info == nil {
			msg := T("Loading provider status…")
			if !loading {
				msg = T("Connect to this node's daemon to manage API keys.")
			}
			children = append(children, card(widget.Box(widget.Row(
				widget.Expanded(muted(widget.Text(msg).Size(12.5))),
				ui.Button(T("Retry"), func() { m.loadProviders() }).Outline(),
			).Gap(12).Align(widget.AlignCenter)).Pad(14, 16)))
			return widget.Column(children...).Gap(sectionGap).Stretch()
		}

		children = append(children, decisionDefaultsCard(m, info))
		children = append(children, providerKeysCard(m, info))
		return widget.Column(children...).Gap(sectionGap).Stretch()
	})

	return page(T("API keys"),
		T("API keys used by decision-model calls in prompt templates (`mako.decide(...)`). Keys stay on this node and are never sent to peers."),
		content,
	)
}

func decisionDefaultsCard(m *model, info *protocol.Providers) ggui.Widget {
	prov := ggui.State(info.GetDefaultProvider())
	modelName := ggui.State(info.GetDefaultModel())
	save := func() {
		m.actOnProvider(&protocol.ProviderAction{
			Op:              "set_defaults",
			DefaultProvider: strings.TrimSpace(ggui.Peek(prov)),
			DefaultModel:    strings.TrimSpace(ggui.Peek(modelName)),
		}, func() {
			m.toaster.Push(ui.Toast(T("Saved"), T("Default decision model updated.")))
		})
	}
	providerOptions := []string{"", decision.ProviderTypeSafe, decision.ProviderOpenRouter, decision.ProviderOpenAI, decision.ProviderGoogle}
	providerSelect := widget.Box(ui.Select(prov).Options(providerOptions).Format(func(id string) string {
		switch id {
		case "":
			return T("Automatic")
		case decision.ProviderTypeSafe:
			return "TypeSafe AI"
		case decision.ProviderOpenRouter:
			return "OpenRouter"
		case decision.ProviderOpenAI:
			return "OpenAI"
		case decision.ProviderGoogle:
			return "Google AI Studio"
		}
		return id
	}).Name(T("Default decision provider"))).Width(200)

	placeholder := ggui.Map(prov, func(id string) string {
		if def := decision.DefaultModels[id]; def != "" {
			return def
		}
		return "jev-latest / gpt-6-luna / gemini-decision-1"
	})

	modelField := ggui.View(placeholder, func(ph string) ggui.Widget {
		return widget.Box(ui.TextField(modelName).
			Style(ggui.TextStyle{Font: ggui.DefaultMonoFont()}).
			Placeholder(ph).
			Name(T("Default decision model")).
			OnSubmit(func(string) { save() })).Width(240)
	})

	return section(T("Decision model"),
		card(
			row(T("Default provider"), T("Used when `mako.decide(...)` leaves `provider` unspecified. Automatic prefers the first configured key."),
				providerSelect),
			row(T("Default model"), T("Leave empty to use each provider's built-in default model."),
				widget.Row(modelField, ui.Button(T("Save"), save).Outline().Name(T("Save decision defaults"))).Gap(8).Align(widget.AlignCenter)),
		),
	)
}

func providerKeysCard(m *model, info *protocol.Providers) ggui.Widget {
	byID := make(map[string]*protocol.ProviderInfo, len(info.GetProviders()))
	for _, p := range info.GetProviders() {
		byID[p.GetId()] = p
	}
	rows := make([]ggui.Widget, 0, len(providerCatalog))
	for _, meta := range providerCatalog {
		rows = append(rows, providerKeyRow(m, meta, byID[meta.ID]))
	}
	return section(T("Providers"), card(rows...))
}

func providerKeyRow(m *model, meta providerMeta, p *protocol.ProviderInfo) ggui.Widget {
	draft := ggui.State("")
	set := p.GetSet()
	envSet := p.GetEnvSet()

	var badgeText string
	var badgeColor color.Color
	switch {
	case set:
		badgeText, badgeColor = T("Saved"), skin.DiffAdded
	case envSet:
		badgeText, badgeColor = T("Environment"), skin.DotWaiting
	default:
		badgeText, badgeColor = T("Not set"), skin.ExtraMuted
	}

	saveVal := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		m.actOnProvider(&protocol.ProviderAction{
			Op:       "set_key",
			Provider: meta.ID,
			Value:    v,
		}, func() {
			draft.Set("")
		})
	}

	placeholder := pick(set, T("Saved; type to replace it"), T("Paste API key"))
	field := widget.Box(ui.TextField(draft).
		Password().
		Placeholder(placeholder).
		Name(Tf("%s API key", meta.Name)).
		OnSubmit(saveVal)).Width(220)

	controls := []ggui.Widget{
		field,
		ui.Button(T("Save"), func() { saveVal(ggui.Peek(draft)) }).
			Outline().
			Name(Tf("Save %s API key", meta.Name)),
	}
	if set {
		controls = append(controls, ui.Button(T("Clear"), func() {
			m.actOnProvider(&protocol.ProviderAction{
				Op:       "clear_key",
				Provider: meta.ID,
			}, func() { draft.Set("") })
		}).Ghost().Name(Tf("Clear %s API key", meta.Name)))
	}

	hint := meta.Host + " · " + meta.EnvHint
	if p.GetDefaultModel() != "" {
		hint += " · " + p.GetDefaultModel()
	}

	return widget.Box(widget.Row(
		widget.Expanded(widget.Column(
			widget.Row(
				widget.Text(meta.Name).Size(13.5).Weight(ggui.WeightMedium),
				pill(badgeText, badgeColor),
			).Gap(8).Align(widget.AlignCenter),
			muted(widget.Text(hint).Size(11.5).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()),
		).Gap(3).Stretch()),
		widget.Row(controls...).Gap(8).Align(widget.AlignCenter),
	).Gap(16).Align(widget.AlignCenter)).Pad(14, 16)
}
