package gui

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/fonts/gofont"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/iconset"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

func TestModelsAreNamedAsPeopleSayThem(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5":            "Opus 5.5",
		"claude-sonnet-4-5-20250929": "Sonnet 4.5",
		"claude-fable-5-1":           "Fable 5.1",
		"claude-haiku":               "Haiku",
		"gpt-5.1-codex":              "gpt-5.1-codex",
		"claude-":                    "claude-",
	} {
		if got := modelName(id); got != want {
			t.Errorf("modelName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestComposerBarIconsLoadAndRasterize(t *testing.T) {
	for _, name := range []string{
		"brain", "shield", "shield-off", "zap",
		"chevron-down", "plus", "lock", "pencil", "list",
		"agent-claude", "agent-codex", "agent-agy", "agent-pi", "agent-opencode",
	} {
		if !iconset.Has(name) {
			t.Fatalf("icon %q is not bundled in iconset", name)
		}
		svg, err := iconset.Load(name)
		if err != nil {
			t.Fatalf("iconset.Load(%q): %v", name, err)
		}
		img := svg.Image(32)
		opaque := 0
		for i := 3; i < len(img.Pix); i += 4 {
			if img.Pix[i] > 0 {
				opaque++
			}
		}
		if opaque == 0 {
			t.Errorf("icon %q rasterized empty", name)
		}
	}
}

func TestComposerBarIconAndLabelMappings(t *testing.T) {
	for mode, want := range map[string]string{
		"bypassPermissions": "shield-off",
		"default":           "shield",
		"auto":              "shield",
		"dontAsk":           "shield",
		"readOnly":          "lock",
		"acceptEdits":       "pencil",
		"plan":              "list",
		"custom":            "shield",
	} {
		if got := modeIcon(mode); got != want {
			t.Errorf("modeIcon(%q) = %q, want %q", mode, got, want)
		}
	}

	for effort, want := range map[string]string{
		"":        "",
		"low":     "Low",
		"medium":  "Medium",
		"high":    "High",
		"xhigh":   "XHigh",
		"XHigh":   "XHigh",
		"max":     "Max",
		"minimal": "Minimal",
	} {
		if got := effortLabel(effort); got != want {
			t.Errorf("effortLabel(%q) = %q, want %q", effort, got, want)
		}
	}

	for agentName, want := range map[string]string{
		"claude":   "agent-claude",
		"codex":    "agent-codex",
		"agy":      "agent-agy",
		"pi":       "agent-pi",
		"opencode": "agent-opencode",
		"shell":    "square-terminal",
	} {
		if got := agentIcon(agentName); got != want {
			t.Errorf("agentIcon(%q) = %q, want %q", agentName, got, want)
		}
	}
}

func useEnglish(t *testing.T) {
	t.Helper()
	old := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(old) })
}

func TestComposerBarSeparateModelEffortAndModeMenus(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	m := newModel(config.Default(), "")
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id:      "s",
		Agent:   "claude",
		Project: "/tmp",
		State:   protocol.StateRunning,
		Controls: &protocol.AgentControls{
			Modes:   []string{"default", "acceptEdits", "plan", "bypassPermissions"},
			Models:  []string{"opus", "sonnet", "haiku"},
			Efforts: []string{"low", "medium", "high", "xhigh"},
		},
	}})
	state := m.chatState("s")
	state.inTerminal = func() {}
	state.result.Set(chatResult{
		Loaded: true,
		Chat: &protocol.Chat{
			Supported: true,
			Usage: &protocol.Usage{
				Model:          "claude-opus-5-5",
				Effort:         "medium",
				PermissionMode: "bypassPermissions",
			},
		},
	})

	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(900, 420))
	defer p.Close()
	p.Frame()

	sem := p.Semantics()
	attachBtn, ok := sem.Find(ggui.RoleButton, "Attach files")
	if !ok {
		t.Fatalf("missing Attach files button:\n%s", sem.String())
	}
	modelBtn, ok := sem.Find(ggui.RoleMenu, "Model")
	if !ok {
		t.Fatalf("missing Model menu:\n%s", sem.String())
	}
	effortBtn, ok := sem.Find(ggui.RoleButton, "Effort")
	if !ok {
		t.Fatalf("missing Effort button:\n%s", sem.String())
	}
	modeBtn, ok := sem.Find(ggui.RoleMenu, "Permission mode")
	if !ok {
		t.Fatalf("missing Permission mode menu:\n%s", sem.String())
	}
	if _, ok := sem.Find(ggui.RoleMenu, "Quick actions"); ok {
		t.Fatalf("unexpected Quick actions menu on Claude composer:\n%s", sem.String())
	}
	if _, ok := sem.Find(ggui.RoleButton, "Fast mode"); ok {
		t.Fatalf("unexpected Fast mode button on Claude composer:\n%s", sem.String())
	}
	termBtn, ok := sem.Find(ggui.RoleButton, "Type in the terminal")
	if !ok {
		t.Fatalf("missing Type in the terminal button:\n%s", sem.String())
	}
	sendBtn, ok := sem.Find(ggui.RoleButton, "Send")
	if !ok {
		t.Fatalf("missing Send button:\n%s", sem.String())
	}

	// Verify exact inline left-to-right control order.
	order := []struct {
		name string
		x    float64
	}{
		{"Attach files", attachBtn.Rect.Origin.X},
		{"Model", modelBtn.Rect.Origin.X},
		{"Effort", effortBtn.Rect.Origin.X},
		{"Permission mode", modeBtn.Rect.Origin.X},
		{"Type in the terminal", termBtn.Rect.Origin.X},
		{"Send", sendBtn.Rect.Origin.X},
	}
	for i := 1; i < len(order); i++ {
		if order[i].x <= order[i-1].x {
			t.Fatalf("expected %s (x=%.1f) to appear to the right of %s (x=%.1f)",
				order[i].name, order[i].x, order[i-1].name, order[i-1].x)
		}
	}

	// Verify chip widgets render expected display labels.
	for _, tc := range []struct {
		name string
		w    ggui.Widget
		want string
	}{
		{"model chip", modelChip("claude", "claude-opus-5-5", true), "Opus 5.5"},
		{"effort chip", effortChip("medium", true), "Medium"},
		{"permission chip", permissionChip("bypassPermissions", true), "Full access"},
	} {
		cp := ggui.ProbeBuilder(func() ggui.Widget { return tc.w }, ggui.Sz(240, 40))
		cp.Frame()
		if _, ok := cp.Semantics().Find(ggui.RoleText, tc.want); !ok {
			cp.Close()
			t.Fatalf("%s: expected chip text %q:\n%s", tc.name, tc.want, cp.Semantics().String())
		}
		cp.Close()
	}

	// Open Model menu: lists only models + default note (no efforts).
	p.Click(modelBtn.Rect.Center())
	p.Frame()
	modelSem := p.Semantics()
	opusItem, ok := modelSem.Find(ggui.RoleMenuItem, "Opus")
	if !ok || opusItem.Checked != ggui.Tri(true) {
		t.Fatalf("expected checked Opus item in Model menu, got ok=%v checked=%v:\n%s", ok, opusItem.Checked, modelSem.String())
	}
	if _, ok := modelSem.Find(ggui.RoleMenuItem, "Sonnet"); !ok {
		t.Fatalf("expected Sonnet item in Model menu:\n%s", modelSem.String())
	}
	if _, ok := modelSem.Find(ggui.RoleMenuItem, "The agent keeps these as your default"); !ok {
		t.Fatalf("expected default footer note in Model menu:\n%s", modelSem.String())
	}
	if _, ok := modelSem.Find(ggui.RoleMenuItem, "XHigh"); ok {
		t.Fatalf("effort item XHigh should not appear inside Model menu:\n%s", modelSem.String())
	}
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	p.Frame()

	// Open Effort slider popover: shows slider, tick stops, and default note (no models).
	p.Click(effortBtn.Rect.Center())
	p.Frame()
	effortSem := p.Semantics()
	sliderNode, ok := effortSem.Find(ggui.RoleSlider, "Reasoning effort")
	if !ok || sliderNode.Min != 0 || sliderNode.Max != 3 || sliderNode.Now != 1 {
		t.Fatalf("expected Reasoning effort slider [0..3] at 1, got ok=%v node=%+v:\n%s", ok, sliderNode, effortSem.String())
	}
	if _, ok := effortSem.Find(ggui.RoleButton, "XHigh"); !ok {
		t.Fatalf("expected XHigh stop button in Effort popover:\n%s", effortSem.String())
	}
	if _, ok := effortSem.Find(ggui.RoleText, "The agent keeps these as your default"); !ok {
		t.Fatalf("expected default footer note in Effort popover:\n%s", effortSem.String())
	}
	if _, ok := effortSem.Find(ggui.RoleMenuItem, "Opus"); ok {
		t.Fatalf("model item Opus should not appear inside Effort popover:\n%s", effortSem.String())
	}
	p.Type(ggui.Mods{}, ggui.KeyEscape)
	p.Frame()

	// Open Permission mode menu: lists modes with Full access checked.
	p.Click(modeBtn.Rect.Center())
	p.Frame()
	modeSem := p.Semantics()
	fullItem, ok := modeSem.Find(ggui.RoleMenuItem, "Full access")
	if !ok || fullItem.Checked != ggui.Tri(true) {
		t.Fatalf("expected checked Full access item in Permission mode menu, got ok=%v checked=%v:\n%s", ok, fullItem.Checked, modeSem.String())
	}
	if _, ok := modeSem.Find(ggui.RoleMenuItem, "Ask first"); !ok {
		t.Fatalf("expected Ask first item in Permission mode menu:\n%s", modeSem.String())
	}
}

func TestComposerBarReadOnlyAndFallbackChips(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	t.Run("read-only chips when controls are empty", func(t *testing.T) {
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{{
			Id:      "s",
			Agent:   "agy",
			Project: "/tmp",
			State:   protocol.StateRunning,
		}})
		state := m.chatState("s")
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage: &protocol.Usage{
					Model:          "gemini-4-neon",
					Effort:         "xhigh",
					PermissionMode: "default",
				},
			},
		})

		p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(800, 300))
		defer p.Close()
		p.Frame()

		sem := p.Semantics()
		for _, text := range []string{"gemini-4-neon", "XHigh", "Ask first"} {
			if _, ok := sem.Find(ggui.RoleText, text); !ok {
				t.Fatalf("expected read-only chip text %q:\n%s", text, sem.String())
			}
		}
		for _, menuName := range []string{"Model", "Permission mode"} {
			if _, ok := sem.Find(ggui.RoleMenu, menuName); ok {
				t.Fatalf("expected %q to be read-only (not a menu) when controls are empty:\n%s", menuName, sem.String())
			}
		}
		if _, ok := sem.Find(ggui.RoleButton, "Effort"); ok {
			t.Fatalf("expected Effort to be read-only when controls are empty:\n%s", sem.String())
		}
	})

	t.Run("fallback labels when selectable but unset", func(t *testing.T) {
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{{
			Id:      "s",
			Agent:   "claude",
			Project: "/tmp",
			State:   protocol.StateRunning,
			Controls: &protocol.AgentControls{
				Models:  []string{"opus"},
				Efforts: []string{"medium"},
				Modes:   []string{"default", "bypassPermissions"},
			},
		}})
		state := m.chatState("s")
		p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(800, 300))
		defer p.Close()
		p.Frame()

		sem := p.Semantics()
		for _, menuName := range []string{"Model", "Permission mode"} {
			if _, ok := sem.Find(ggui.RoleMenu, menuName); !ok {
				t.Fatalf("expected %q menu when controls are selectable:\n%s", menuName, sem.String())
			}
		}
		if _, ok := sem.Find(ggui.RoleButton, "Effort"); !ok {
			t.Fatalf("expected Effort popover trigger when efforts are selectable:\n%s", sem.String())
		}
		for _, tc := range []struct {
			w    ggui.Widget
			want string
		}{
			{modelChip("claude", "", true), "Model"},
			{effortChip("", true), "Effort"},
			{permissionChip("default", true), "Ask first"},
		} {
			cp := ggui.ProbeBuilder(func() ggui.Widget { return tc.w }, ggui.Sz(240, 40))
			cp.Frame()
			if _, ok := cp.Semantics().Find(ggui.RoleText, tc.want); !ok {
				cp.Close()
				t.Fatalf("expected fallback chip text %q:\n%s", tc.want, cp.Semantics().String())
			}
			cp.Close()
		}
	})

	t.Run("hides unset chips when controls are empty", func(t *testing.T) {
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{{
			Id:      "s",
			Agent:   "pi",
			Project: "/tmp",
			State:   protocol.StateRunning,
		}})
		state := m.chatState("s")
		p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(800, 300))
		defer p.Close()
		p.Frame()

		sem := p.Semantics()
		for _, menuName := range []string{"Model", "Permission mode"} {
			if _, ok := sem.Find(ggui.RoleMenu, menuName); ok {
				t.Fatalf("did not expect %q menu when unset and unselectable:\n%s", menuName, sem.String())
			}
		}
		if _, ok := sem.Find(ggui.RoleButton, "Effort"); ok {
			t.Fatalf("did not expect Effort button when unset and unselectable:\n%s", sem.String())
		}
		if _, ok := sem.Find(ggui.RoleMenu, "Quick actions"); ok {
			t.Fatalf("did not expect Quick actions menu:\n%s", sem.String())
		}
		if _, ok := sem.Find(ggui.RoleButton, "Fast mode"); ok {
			t.Fatalf("did not expect Fast mode button on non-Codex session:\n%s", sem.String())
		}
	})

	t.Run("chevron widens chip only when selectable", func(t *testing.T) {
		ro := ggui.ProbeBuilder(func() ggui.Widget {
			return widget.Row(effortChip("medium", false))
		}, ggui.Sz(300, 60))
		defer ro.Close()
		ro.Frame()
		sel := ggui.ProbeBuilder(func() ggui.Widget {
			return widget.Row(effortChip("medium", true))
		}, ggui.Sz(300, 60))
		defer sel.Close()
		sel.Frame()

		roText, _ := ro.Semantics().Find(ggui.RoleText, "Medium")
		selText, _ := sel.Semantics().Find(ggui.RoleText, "Medium")
		if roText.Rect.Size.W <= 0 || selText.Rect.Size.W <= 0 {
			t.Fatalf("expected Medium text in both chips")
		}
		// Layout width of selectable chip includes the trailing chevron + gap.
		env := ggui.Env{}
		roSz := effortChip("medium", false).Layout(ggui.Loose(ggui.Sz(300, 60)), env)
		selSz := effortChip("medium", true).Layout(ggui.Loose(ggui.Sz(300, 60)), env)
		if selSz.W <= roSz.W+10 {
			t.Fatalf("expected selectable chip width (%.1f) > read-only width (%.1f) by chevron + gap", selSz.W, roSz.W)
		}
	})
}

func startFakeConfigureDaemon(t *testing.T, base *protocol.SessionInfo) (sock string, gotReqs func() []*protocol.ConfigureAgent) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mk-fast")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var reqs []*protocol.ConfigureAgent
	state := proto.CloneOf(base)
	if state == nil {
		state = &protocol.SessionInfo{Agent: "codex", State: protocol.StateRunning, Controls: &protocol.AgentControls{FastMode: true}}
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					env, err := protocol.ReadFrame(c)
					if err != nil {
						return
					}
					switch env.Type() {
					case protocol.TypeHello:
						if reply, err := protocol.Encode(protocol.TypeWelcome, env.GetId(), &protocol.Welcome{
							Node: &protocol.Node{Id: "local", Name: "local", Revision: protocol.Revision},
						}); err == nil {
							_ = protocol.WriteFrame(c, reply, false)
						}
					case protocol.TypeConfigureAgent:
						req := env.GetConfigureAgent()
						mu.Lock()
						reqs = append(reqs, proto.CloneOf(req))
						cur := proto.CloneOf(state)
						cur.Id = req.GetSessionId()
						if req.FastMode != nil {
							cur.FastMode = req.GetFastMode()
						}
						state = cur
						mu.Unlock()
						if reply, err := protocol.Encode(protocol.TypeSessionInfo, env.GetId(), cur); err == nil {
							_ = protocol.WriteFrame(c, reply, false)
						}
					}
				}
			}(conn)
		}
	}()

	return sock, func() []*protocol.ConfigureAgent {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*protocol.ConfigureAgent, len(reqs))
		copy(out, reqs)
		return out
	}
}

func TestComposerBarCodexFastMode(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	t.Run("hidden on non-Codex sessions without fast mode", func(t *testing.T) {
		for _, agentName := range []string{"claude", "agy", "shell"} {
			m := newModel(config.Default(), "")
			m.Attached.Set([]string{"s"})
			m.Sessions.Set([]*protocol.SessionInfo{{
				Id:      "s",
				Agent:   agentName,
				Project: "/tmp",
				State:   protocol.StateRunning,
			}})
			state := m.chatState("s")
			p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(800, 300))
			p.Frame()
			sem := p.Semantics()
			if _, ok := sem.Find(ggui.RoleButton, "Fast mode"); ok {
				p.Close()
				t.Fatalf("agent %q should not show Fast mode button:\n%s", agentName, sem.String())
			}
			if _, ok := sem.Find(ggui.RoleMenu, "Quick actions"); ok {
				p.Close()
				t.Fatalf("agent %q should not show Quick actions menu:\n%s", agentName, sem.String())
			}
			p.Close()
		}
		if w := fastModeButton(nil, "s", false, false, ggui.Const(false)); w != nil {
			t.Fatalf("fastModeButton(enabled=false, selectable=false) = %T, want nil", w)
		}
	})

	t.Run("codex toggle sends ConfigureAgent RPC, updates immediately, and disables when detached", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix socket fake daemon test runs on unix")
		}
		sess := &protocol.SessionInfo{
			Id:       "s",
			Agent:    "codex",
			Project:  "/tmp",
			State:    protocol.StateRunning,
			FastMode: false,
			Controls: &protocol.AgentControls{FastMode: true},
		}
		sock, gotReqs := startFakeConfigureDaemon(t, sess)

		m := newModel(config.Default(), "")
		m.link.setDaemon(sock)
		m.link.setLinked(true)
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{sess})
		state := m.chatState("s")
		state.inTerminal = func() {}
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage: &protocol.Usage{
					Model:          "gpt-5.4",
					Effort:         "high",
					PermissionMode: "bypassPermissions",
				},
			},
		})

		p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(900, 360))
		defer p.Close()
		m.post = p.Post
		p.Frame()

		sem := p.Semantics()
		modeText, ok := sem.Find(ggui.RoleText, "Full access")
		if !ok {
			t.Fatalf("missing Full access chip:\n%s", sem.String())
		}
		fastBtn, ok := sem.Find(ggui.RoleButton, "Fast mode")
		if !ok || fastBtn.Disabled {
			t.Fatalf("expected enabled Fast mode button on Codex session, ok=%v disabled=%v:\n%s", ok, fastBtn.Disabled, sem.String())
		}
		termBtn, ok := sem.Find(ggui.RoleButton, "Type in the terminal")
		if !ok {
			t.Fatalf("missing terminal button:\n%s", sem.String())
		}
		if fastBtn.Rect.Origin.X <= modeText.Rect.Origin.X || fastBtn.Rect.Origin.X >= termBtn.Rect.Origin.X {
			t.Fatalf("Fast mode button x=%.1f should sit between Full access (x=%.1f) and terminal (x=%.1f)",
				fastBtn.Rect.Origin.X, modeText.Rect.Origin.X, termBtn.Rect.Origin.X)
		}

		// Click OFF -> sends ConfigureAgent{FastMode: true} and updates session immediately.
		p.Click(fastBtn.Rect.Center())
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			p.Frame()
			if len(gotReqs()) >= 1 && m.Sessions.Get()[0].GetFastMode() {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		reqs := gotReqs()
		if len(reqs) != 1 || reqs[0].FastMode == nil || !reqs[0].GetFastMode() || reqs[0].GetSessionId() != "s" {
			t.Fatalf("first click ConfigureAgent reqs = %v, want FastMode=true", reqs)
		}
		if !m.Sessions.Get()[0].GetFastMode() {
			t.Fatal("session FastMode did not immediately flip to true after RPC reply")
		}

		// Click ON -> sends ConfigureAgent{FastMode: false} and updates session immediately.
		fastBtn, _ = p.Semantics().Find(ggui.RoleButton, "Fast mode")
		p.Click(fastBtn.Rect.Center())
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			p.Frame()
			if len(gotReqs()) >= 2 && !m.Sessions.Get()[0].GetFastMode() {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		reqs = gotReqs()
		if len(reqs) != 2 || reqs[1].FastMode == nil || reqs[1].GetFastMode() {
			t.Fatalf("second click ConfigureAgent reqs = %v, want FastMode=false", reqs)
		}
		if m.Sessions.Get()[0].GetFastMode() {
			t.Fatal("session FastMode did not immediately flip back to false after RPC reply")
		}

		// Detaching disables the Fast mode toggle button.
		m.Attached.Set(nil)
		p.Frame()
		fastBtn, ok = p.Semantics().Find(ggui.RoleButton, "Fast mode")
		if !ok || !fastBtn.Disabled {
			t.Fatalf("expected Fast mode disabled when detached, ok=%v disabled=%v", ok, fastBtn.Disabled)
		}
	})

	t.Run("read-only fallback when Usage.FastMode is true without control", func(t *testing.T) {
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{{
			Id:      "s",
			Agent:   "codex",
			Project: "/tmp",
			State:   protocol.StateRunning,
		}})
		state := m.chatState("s")
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage: &protocol.Usage{
					Model:          "gpt-5.4",
					Effort:         "high",
					PermissionMode: "bypassPermissions",
					FastMode:       proto.Bool(true),
				},
			},
		})

		p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(800, 300))
		defer p.Close()
		p.Frame()

		// Not clickable (no Fast mode button), but the read-only indicator widget is non-nil and lays out visibly.
		if _, ok := p.Semantics().Find(ggui.RoleButton, "Fast mode"); ok {
			t.Fatalf("read-only Usage.FastMode fallback should not render an interactive button:\n%s", p.Semantics().String())
		}
		ro := fastModeButton(m, "s", true, false, ggui.Const(false))
		if ro == nil {
			t.Fatal("expected non-nil read-only fastModeButton when enabled && !selectable")
		}
		sz := ro.Layout(ggui.Loose(ggui.Sz(100, 100)), ggui.Env{})
		if sz.W < 20 || sz.H < 20 {
			t.Fatalf("read-only fast mode indicator size = %v, want >= 20x20", sz)
		}
	})
}

func TestComposerBarVisualRendering(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)
	ggui.SetDefaultMonoFont(gofont.Mono())

	renderCase := func(name string, pal skin.Palette, info *protocol.SessionInfo, usage *protocol.Usage, expectFast bool) {
		t.Helper()
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{info})
		state := m.chatState("s")
		state.inTerminal = func() {}
		state.result.Set(chatResult{
			Loaded: true,
			Chat:   &protocol.Chat{Supported: true, Usage: usage},
		})

		const w, h = 760, 170
		p := ggui.ProbeBuilder(func() ggui.Widget {
			th := skin.Theme(pal)
			body := widget.Box(widget.Column(widget.Spacer(), composer(m, "s", state)).Stretch()).
				Fill(skin.Surface0).Pad(12, 20, 12, 20)
			return ggui.WithEnv(th.Apply, body)
		}, ggui.Sz(w, h))
		defer p.Close()
		p.Frame()

		_, hasFast := p.Semantics().Find(ggui.RoleButton, "Fast mode")
		if hasFast != expectFast {
			t.Fatalf("%s: Fast mode button present=%v, want %v", name, hasFast, expectFast)
		}
		img := ggfx.NewImage(w, h)
		p.Draw(img)
	}

	claudeInfo := &protocol.SessionInfo{
		Id: "s", Agent: "claude", Project: "/tmp", State: protocol.StateRunning,
		Controls: &protocol.AgentControls{
			Modes:   []string{"default", "acceptEdits", "plan", "bypassPermissions"},
			Models:  []string{"opus", "sonnet", "haiku"},
			Efforts: []string{"low", "medium", "high", "xhigh"},
		},
	}
	claudeUsage := &protocol.Usage{Model: "claude-opus-5-5", Effort: "medium", PermissionMode: "bypassPermissions"}

	codexOffInfo := &protocol.SessionInfo{
		Id: "s", Agent: "codex", Project: "/tmp", State: protocol.StateRunning,
		FastMode: false,
		Controls: &protocol.AgentControls{FastMode: true},
	}
	codexOnInfo := &protocol.SessionInfo{
		Id: "s", Agent: "codex", Project: "/tmp", State: protocol.StateRunning,
		FastMode: true,
		Controls: &protocol.AgentControls{FastMode: true},
	}
	codexUsage := &protocol.Usage{Model: "gpt-5.4", Effort: "high", PermissionMode: "bypassPermissions"}

	renderCase("composer-claude-no-zap-dark", skin.Dark, claudeInfo, claudeUsage, false)
	renderCase("composer-codex-fast-off-dark", skin.Dark, codexOffInfo, codexUsage, true)
	renderCase("composer-codex-fast-on-dark", skin.Dark, codexOnInfo, codexUsage, true)
	renderCase("composer-codex-fast-off-light", skin.Light, codexOffInfo, codexUsage, true)
	renderCase("composer-codex-fast-on-light", skin.Light, codexOnInfo, codexUsage, true)

	// Verify active Fast Mode pill wash resolves a visible non-transparent tint in both Dark and Light palettes.
	for _, pal := range []skin.Palette{skin.Dark, skin.Light} {
		env := skin.Theme(pal).Apply(ggui.Env{})
		w := &washed{child: widget.Box().Width(26).Height(26), color: skin.AccentBright, radius: 8}
		_ = w.Layout(ggui.Loose(ggui.Sz(100, 100)), env)
		_, _, _, a := w.tint.RGBA()
		if a == 0 {
			t.Fatalf("palette %s: washed active pill tint resolved transparent", pal.Name)
		}
	}
}

func sampleAgyCatalog() []*protocol.AgentModel {
	return []*protocol.AgentModel{
		{Id: "gemini-4-neon", Name: "Gemini 4 Neon", Efforts: []string{"low", "medium", "high", "xhigh"}},
		{Id: "gemini-4-carbon", Name: "Gemini 4 Carbon", Efforts: []string{"low", "medium", "high"}},
		{Id: "gemini-pro-orchestrator-neon", Name: "Orchestrator (Neon)"},
		{Id: "gemini-pro-orchestrator", Name: "Orchestrator (Carbon)"},
		{Id: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)"},
	}
}

func TestComposerBarAgyModelPickerAndPerModelEfforts(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)
	cat := sampleAgyCatalog()

	t.Run("model chip label stripping and active matching", func(t *testing.T) {
		for _, tc := range []struct {
			current, effort string
			catalog         []*protocol.AgentModel
			wantLabel       string
		}{
			{"Gemini 4 Neon (High)", "high", cat, "Gemini 4 Neon"},
			{"gemini-4-neon-high", "high", cat, "Gemini 4 Neon"},
			{"Orchestrator (Neon)", "", cat, "Orchestrator (Neon)"},
			{"GPT-OSS 120B (Medium)", "", cat, "GPT-OSS 120B (Medium)"},
			{"Gemini 4 Neon (High)", "high", nil, "Gemini 4 Neon"},
			{"Orchestrator (Neon)", "high", nil, "Orchestrator (Neon)"},
			{"GPT-OSS 120B (Medium)", "", nil, "GPT-OSS 120B (Medium)"},
			{"claude-opus-5-5", "medium", nil, "Opus 5.5"},
		} {
			if got := modelChipLabel(tc.current, tc.effort, tc.catalog); got != tc.wantLabel {
				t.Errorf("modelChipLabel(%q, %q) = %q, want %q", tc.current, tc.effort, got, tc.wantLabel)
			}
		}

		for _, tc := range []struct {
			current, option string
			catalog         []*protocol.AgentModel
			want            bool
		}{
			{"Gemini 4 Neon (High)", "gemini-4-neon", cat, true},
			{"gemini-4-neon-high", "gemini-4-neon", cat, true},
			{"Gemini 4 Neon (High)", "gemini-4-carbon", cat, false},
			{"Orchestrator (Neon)", "gemini-pro-orchestrator-neon", cat, true},
			{"Orchestrator (Neon)", "gemini-pro-orchestrator", cat, false},
			{"Orchestrator (Carbon)", "gemini-pro-orchestrator", cat, true},
			{"gemini-pro-orchestrator-neon", "gemini-pro-orchestrator", cat, false},
			{"GPT-OSS 120B (Medium)", "gpt-oss-120b-medium", cat, true},
			{"claude-opus-5-5", "opus", nil, true},
			{"claude-sonnet-4-5-20250929", "sonnet", nil, true},
			{"claude-opus-5-5", "sonnet", nil, false},
		} {
			if got := modelMatches(tc.current, tc.option, tc.catalog); got != tc.want {
				t.Errorf("modelMatches(%q, %q) = %v, want %v", tc.current, tc.option, got, tc.want)
			}
		}
	})

	t.Run("agy catalog populates model picker, sends ConfigureAgent, and scopes efforts per model", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix socket fake daemon test runs on unix")
		}
		sess := &protocol.SessionInfo{
			Id:      "s",
			Agent:   "agy",
			Project: "/tmp",
			State:   protocol.StateRunning,
			Controls: &protocol.AgentControls{
				Modes:   []string{"auto", "acceptEdits", "plan"},
				Efforts: []string{"low", "medium", "high", "xhigh", "max"},
			},
		}
		sock, gotReqs := startFakeConfigureDaemon(t, sess)

		m := newModel(config.Default(), "")
		m.link.setDaemon(sock)
		m.link.setLinked(true)
		m.AgentCatalogs.Set(map[string][]*protocol.AgentModel{"agy": cat})
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{sess})
		state := m.chatState("s")
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage: &protocol.Usage{
					Model:          "Gemini 4 Neon (High)",
					Effort:         "high",
					PermissionMode: "auto",
				},
			},
		})

		p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(900, 460))
		defer p.Close()
		m.post = p.Post
		p.Frame()

		// Model chip displays stripped "Gemini 4 Neon" and opens catalog menu.
		modelBtn, ok := p.Semantics().Find(ggui.RoleMenu, "Model")
		if !ok {
			t.Fatalf("missing agy Model menu:\n%s", p.Semantics().String())
		}
		cp := ggui.ProbeBuilder(func() ggui.Widget { return modelChipFor("agy", "Gemini 4 Neon (High)", "high", true, cat) }, ggui.Sz(260, 40))
		cp.Frame()
		if _, ok := cp.Semantics().Find(ggui.RoleText, "Gemini 4 Neon"); !ok {
			cp.Close()
			t.Fatalf("expected stripped chip label Gemini 4 Neon:\n%s", cp.Semantics().String())
		}
		cp.Close()

		p.Click(modelBtn.Rect.Center())
		p.Frame()
		sem := p.Semantics()
		neonItem, ok := sem.Find(ggui.RoleMenuItem, "Gemini 4 Neon")
		if !ok || neonItem.Checked != ggui.Tri(true) {
			t.Fatalf("expected checked Gemini 4 Neon item, ok=%v checked=%v:\n%s", ok, neonItem.Checked, sem.String())
		}
		if _, ok := sem.Find(ggui.RoleMenuItem, "Orchestrator (Neon)"); !ok {
			t.Fatalf("expected Orchestrator (Neon) in agy model menu:\n%s", sem.String())
		}
		if _, ok := sem.Find(ggui.RoleMenuItem, "GPT-OSS 120B (Medium)"); !ok {
			t.Fatalf("expected GPT-OSS 120B (Medium) in agy model menu:\n%s", sem.String())
		}
		carbonItem, ok := sem.Find(ggui.RoleMenuItem, "Gemini 4 Carbon")
		if !ok {
			t.Fatalf("expected Gemini 4 Carbon in agy model menu:\n%s", sem.String())
		}
		p.Click(carbonItem.Rect.Center())
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			p.Frame()
			if len(gotReqs()) >= 1 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		reqs := gotReqs()
		if len(reqs) != 1 || reqs[0].GetModel() != "gemini-4-carbon" || reqs[0].GetSessionId() != "s" {
			t.Fatalf("ConfigureAgent reqs = %v, want Model=gemini-4-carbon", reqs)
		}

		// Per-model efforts: Neon has 4 tiers [0..3].
		effortBtn, ok := p.Semantics().Find(ggui.RoleButton, "Effort")
		if !ok {
			t.Fatalf("expected Effort button for Gemini 4 Neon")
		}
		p.Click(effortBtn.Rect.Center())
		p.Frame()
		if sl, ok := p.Semantics().Find(ggui.RoleSlider, "Reasoning effort"); !ok || sl.Max != 3 {
			t.Fatalf("expected 4-tier slider (Max=3) for Neon, got ok=%v sl=%+v", ok, sl)
		}
		p.Type(ggui.Mods{}, ggui.KeyEscape)
		p.Frame()

		// Carbon has 3 tiers [0..2].
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage:     &protocol.Usage{Model: "Gemini 4 Carbon (Medium)", Effort: "medium", PermissionMode: "auto"},
			},
		})
		p.Frame()
		effortBtn, _ = p.Semantics().Find(ggui.RoleButton, "Effort")
		p.Click(effortBtn.Rect.Center())
		p.Frame()
		if sl, ok := p.Semantics().Find(ggui.RoleSlider, "Reasoning effort"); !ok || sl.Max != 2 {
			t.Fatalf("expected 3-tier slider (Max=2) for Carbon, got ok=%v sl=%+v", ok, sl)
		}
		p.Type(ggui.Mods{}, ggui.KeyEscape)
		p.Frame()

		// Fixed single-tier GPT-OSS 120B (Medium) with no efforts hides Effort button.
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage:     &protocol.Usage{Model: "GPT-OSS 120B (Medium)", PermissionMode: "auto"},
			},
		})
		p.Frame()
		if _, ok := p.Semantics().Find(ggui.RoleButton, "Effort"); ok {
			t.Fatalf("expected Effort button hidden for single-tier GPT-OSS 120B (Medium):\n%s", p.Semantics().String())
		}
	})

	t.Run("codex with populated AgentCatalogs keeps effort chip read-only", func(t *testing.T) {
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.AgentCatalogs.Set(map[string][]*protocol.AgentModel{
			"codex": {
				{Id: "gpt-5.4", Name: "gpt-5.4", Efforts: []string{"low", "medium", "high", "xhigh"}},
			},
		})
		m.Sessions.Set([]*protocol.SessionInfo{{
			Id:       "s",
			Agent:    "codex",
			Project:  "/tmp",
			State:    protocol.StateRunning,
			Controls: &protocol.AgentControls{FastMode: true},
		}})
		state := m.chatState("s")
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage: &protocol.Usage{
					Model:          "gpt-5.4",
					Effort:         "high",
					PermissionMode: "bypassPermissions",
				},
			},
		})

		p := ggui.ProbeBuilder(func() ggui.Widget {
			return widget.Column(widget.Spacer(), composer(m, "s", state)).Stretch()
		}, ggui.Sz(800, 360))
		defer p.Close()
		p.Frame()

		sem := p.Semantics()
		if _, ok := sem.Find(ggui.RoleText, "High"); !ok {
			t.Fatalf("expected read-only High effort chip on Codex:\n%s", sem.String())
		}
		if _, ok := sem.Find(ggui.RoleButton, "Effort"); ok {
			t.Fatalf("Codex effort chip must remain read-only (not interactive) even when AgentCatalogs[codex] is loaded:\n%s", sem.String())
		}
		if _, ok := sem.Find(ggui.RoleSlider, "Reasoning effort"); ok {
			t.Fatalf("Codex must not render an Effort slider:\n%s", sem.String())
		}
	})

	t.Run("catalog effort scoping intersects with session Controls.Efforts", func(t *testing.T) {
		m := newModel(config.Default(), "")
		m.Attached.Set([]string{"s"})
		m.AgentCatalogs.Set(map[string][]*protocol.AgentModel{
			"agy": {
				{Id: "gemini-4-neon", Name: "Gemini 4 Neon", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
			},
		})
		m.Sessions.Set([]*protocol.SessionInfo{{
			Id:      "s",
			Agent:   "agy",
			Project: "/tmp",
			State:   protocol.StateRunning,
			Controls: &protocol.AgentControls{
				Models:  []string{"gemini-4-neon"},
				Efforts: []string{"low", "medium", "high"},
			},
		}})
		state := m.chatState("s")
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage:     &protocol.Usage{Model: "Gemini 4 Neon (High)", Effort: "high"},
			},
		})

		p := ggui.ProbeBuilder(func() ggui.Widget {
			return widget.Column(widget.Spacer(), composer(m, "s", state)).Stretch()
		}, ggui.Sz(800, 360))
		defer p.Close()
		p.Frame()

		effortBtn, ok := p.Semantics().Find(ggui.RoleButton, "Effort")
		if !ok {
			t.Fatalf("missing Effort button:\n%s", p.Semantics().String())
		}
		p.Click(effortBtn.Rect.Center())
		p.Frame()
		sem := p.Semantics()
		if sl, ok := sem.Find(ggui.RoleSlider, "Reasoning effort"); !ok || sl.Max != 2 {
			t.Fatalf("expected intersected 3-tier slider (Max=2), got ok=%v sl=%+v:\n%s", ok, sl, sem.String())
		}
		if _, ok := sem.Find(ggui.RoleButton, "XHigh"); ok {
			t.Fatalf("XHigh should be excluded when absent from Controls.Efforts:\n%s", sem.String())
		}
		if _, ok := sem.Find(ggui.RoleButton, "Max"); ok {
			t.Fatalf("Max should be excluded when absent from Controls.Efforts:\n%s", sem.String())
		}
	})

	t.Run("modelOptionLabel capitalizes Claude fallback catalog aliases and preserves real display names", func(t *testing.T) {
		fallback := []*protocol.AgentModel{
			{Id: "opus", Name: "opus"},
			{Id: "sonnet", Name: "sonnet"},
			{Id: "haiku", Name: "haiku"},
		}
		real := []*protocol.AgentModel{
			{Id: "gemini-4-neon", Name: "Gemini 4 Neon"},
			{Id: "claude-opus-5-5", Name: "Claude Opus 5.5"},
		}
		if got := modelOptionLabel("opus", fallback); got != "Opus" {
			t.Fatalf("fallback opus label = %q, want %q", got, "Opus")
		}
		if got := modelOptionLabel("sonnet", fallback); got != "Sonnet" {
			t.Fatalf("fallback sonnet label = %q, want %q", got, "Sonnet")
		}
		if got := modelOptionLabel("gemini-4-neon", real); got != "Gemini 4 Neon" {
			t.Fatalf("real agy label = %q, want %q", got, "Gemini 4 Neon")
		}
		if got := modelOptionLabel("claude-opus-5-5", real); got != "Claude Opus 5.5" {
			t.Fatalf("real claude label = %q, want %q", got, "Claude Opus 5.5")
		}
	})
}

func TestComposerBarEffortSliderPopover(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	t.Run("drag previews live without RPC spam, release commits, and tick click commits", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix socket fake daemon test runs on unix")
		}
		sess := &protocol.SessionInfo{
			Id:      "s",
			Agent:   "claude",
			Project: "/tmp",
			State:   protocol.StateRunning,
			Controls: &protocol.AgentControls{
				Modes:   []string{"default", "bypassPermissions"},
				Models:  []string{"opus", "sonnet"},
				Efforts: []string{"low", "medium", "high", "xhigh"},
			},
		}
		sock, gotReqs := startFakeConfigureDaemon(t, sess)

		m := newModel(config.Default(), "")
		m.link.setDaemon(sock)
		m.link.setLinked(true)
		m.Attached.Set([]string{"s"})
		m.Sessions.Set([]*protocol.SessionInfo{sess})
		state := m.chatState("s")
		state.result.Set(chatResult{
			Loaded: true,
			Chat: &protocol.Chat{
				Supported: true,
				Usage:     &protocol.Usage{Model: "claude-opus-5-5", Effort: "medium", PermissionMode: "bypassPermissions"},
			},
		})

		p := ggui.ProbeBuilder(func() ggui.Widget {
			return widget.Column(widget.Spacer(), composer(m, "s", state)).Stretch()
		}, ggui.Sz(900, 420))
		defer p.Close()
		m.post = p.Post
		p.Frame()

		effortBtn, ok := p.Semantics().Find(ggui.RoleButton, "Effort")
		if !ok {
			t.Fatalf("missing Effort button:\n%s", p.Semantics().String())
		}
		p.Click(effortBtn.Rect.Center())
		p.Frame()

		sliderNode, ok := p.Semantics().Find(ggui.RoleSlider, "Reasoning effort")
		if !ok || sliderNode.Min != 0 || sliderNode.Max != 3 || sliderNode.Now != 1 {
			t.Fatalf("unexpected slider state: ok=%v node=%+v", ok, sliderNode)
		}

		// Press and drag to right end ("XHigh") without releasing yet: live preview updates, 0 RPCs sent.
		midY := sliderNode.Rect.Origin.Y + sliderNode.Rect.Size.H/2
		startX := sliderNode.Rect.Origin.X + sliderNode.Rect.Size.W*0.35
		endX := sliderNode.Rect.Origin.X + sliderNode.Rect.Size.W - 4
		p.Press(ggui.Pt(startX, midY))
		p.Move(ggui.Pt(endX, midY))
		p.Frame()

		sliderNow, _ := p.Semantics().Find(ggui.RoleSlider, "Reasoning effort")
		if sliderNow.Now != 3 {
			t.Fatalf("slider Now during drag = %v, want 3", sliderNow.Now)
		}
		if len(gotReqs()) != 0 {
			t.Fatalf("drag preview should not send ConfigureAgent before commit, got %v", gotReqs())
		}

		// Releasing commits once with Effort="xhigh".
		p.Release(ggui.Pt(endX, midY))
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			p.Frame()
			if len(gotReqs()) >= 1 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		reqs := gotReqs()
		if len(reqs) != 1 || reqs[0].GetEffort() != "xhigh" {
			t.Fatalf("after drag commit, ConfigureAgent reqs = %v, want Effort=xhigh", reqs)
		}

		// Clicking tick label "Low" moves slider to 0 and commits Effort="low".
		lowTick, ok := p.Semantics().Find(ggui.RoleButton, "Low")
		if !ok {
			t.Fatalf("missing Low tick button:\n%s", p.Semantics().String())
		}
		p.Click(lowTick.Rect.Center())
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			p.Frame()
			if len(gotReqs()) >= 2 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		reqs = gotReqs()
		if len(reqs) != 2 || reqs[1].GetEffort() != "low" {
			t.Fatalf("after Low tick click, ConfigureAgent reqs = %v, want Effort=low", reqs)
		}
		sliderNow, _ = p.Semantics().Find(ggui.RoleSlider, "Reasoning effort")
		if sliderNow.Now != 0 {
			t.Fatalf("slider Now after Low tick click = %v, want 0", sliderNow.Now)
		}
	})

	t.Run("1, 3, 4, and 5-tier slider layouts have non-overlapping stops", func(t *testing.T) {
		tierSets := [][]string{
			{"high"},
			{"low", "medium", "high"},
			{"low", "medium", "high", "xhigh"},
			{"low", "medium", "high", "xhigh", "max"},
		}
		for _, tiers := range tierSets {
			m := newModel(config.Default(), "")
			m.Attached.Set([]string{"s"})
			m.Sessions.Set([]*protocol.SessionInfo{{
				Id: "s", Agent: "claude", Project: "/tmp", State: protocol.StateRunning,
				Controls: &protocol.AgentControls{Efforts: tiers},
			}})
			state := m.chatState("s")
			state.result.Set(chatResult{
				Loaded: true,
				Chat:   &protocol.Chat{Supported: true, Usage: &protocol.Usage{Effort: tiers[0]}},
			})

			p := ggui.ProbeBuilder(func() ggui.Widget {
				return widget.Column(widget.Spacer(), composer(m, "s", state)).Stretch()
			}, ggui.Sz(800, 360))
			p.Frame()
			effortBtn, ok := p.Semantics().Find(ggui.RoleButton, "Effort")
			if !ok {
				p.Close()
				t.Fatalf("tiers=%v: missing Effort button", tiers)
			}
			p.Click(effortBtn.Rect.Center())
			p.Frame()
			sem := p.Semantics()
			sl, ok := sem.Find(ggui.RoleSlider, "Reasoning effort")
			if !ok {
				p.Close()
				t.Fatalf("tiers=%v: missing slider", tiers)
			}
			if len(tiers) == 1 && !sl.Disabled {
				p.Close()
				t.Fatalf("1-tier slider should be disabled")
			}
			var prevRight float64
			for i, tier := range tiers {
				tick, ok := sem.Find(ggui.RoleButton, effortLabel(tier))
				if !ok {
					p.Close()
					t.Fatalf("tiers=%v: missing tick %q:\n%s", tiers, effortLabel(tier), sem.String())
				}
				if i > 0 && tick.Rect.Origin.X < prevRight-0.5 {
					p.Close()
					t.Fatalf("tiers=%v: tick %q (x=%.1f) overlapped previous right (%.1f)", tiers, tier, tick.Rect.Origin.X, prevRight)
				}
				prevRight = tick.Rect.Origin.X + tick.Rect.Size.W
			}
			p.Close()
		}
	})
}

func TestComposerBarNarrowAndWideLayout(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	useEnglish(t)

	m := newModel(config.Default(), "")
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{
		Id:      "s",
		Agent:   "claude",
		Project: "/tmp",
		State:   protocol.StateRunning,
		Controls: &protocol.AgentControls{
			Modes:   []string{"default", "acceptEdits", "plan", "bypassPermissions"},
			Models:  []string{"opus", "sonnet", "haiku"},
			Efforts: []string{"low", "medium", "high", "xhigh"},
		},
	}})
	state := m.chatState("s")
	state.inTerminal = func() {}
	state.result.Set(chatResult{
		Loaded: true,
		Chat: &protocol.Chat{
			Supported: true,
			Usage: &protocol.Usage{
				Model:          "claude-opus-5-5",
				Effort:         "medium",
				PermissionMode: "bypassPermissions",
			},
		},
	})

	for _, width := range []float64{420, 600, 900} {
		p := ggui.ProbeBuilder(func() ggui.Widget {
			return widget.Column(widget.Expanded(widget.Text("FEED")), composer(m, "s", state)).Stretch()
		}, ggui.Sz(width, 600))
		p.Frame()
		sem := p.Semantics()

		feed, ok := sem.Find(ggui.RoleText, "FEED")
		if !ok || feed.Rect.Size.H < 400 {
			p.Close()
			t.Fatalf("width %.0f: composer stole feed height (feed=%v):\n%s", width, feed.Rect, sem.String())
		}
		sendBtn, ok := sem.Find(ggui.RoleButton, "Send")
		if !ok {
			p.Close()
			t.Fatalf("width %.0f: missing Send button:\n%s", width, sem.String())
		}
		termBtn, ok := sem.Find(ggui.RoleButton, "Type in the terminal")
		if !ok {
			p.Close()
			t.Fatalf("width %.0f: missing Type in the terminal button:\n%s", width, sem.String())
		}
		if sendBtn.Rect.Size.W < 24 || sendBtn.Rect.Size.H < 24 {
			p.Close()
			t.Fatalf("width %.0f: Send button collapsed to %v", width, sendBtn.Rect.Size)
		}
		if sendBtn.Rect.Origin.X+sendBtn.Rect.Size.W > width {
			p.Close()
			t.Fatalf("width %.0f: Send button overflowed right edge: %v", width, sendBtn.Rect)
		}
		if termBtn.Rect.Origin.X+termBtn.Rect.Size.W > sendBtn.Rect.Origin.X+0.5 {
			p.Close()
			t.Fatalf("width %.0f: terminal button %v overlapped Send button %v", width, termBtn.Rect, sendBtn.Rect)
		}
		attachBtn, ok := sem.Find(ggui.RoleButton, "Attach files")
		if !ok || attachBtn.Rect.Origin.X < 0 {
			p.Close()
			t.Fatalf("width %.0f: Attach files button invalid: ok=%v rect=%v", width, ok, attachBtn.Rect)
		}
		p.Close()
	}
}
