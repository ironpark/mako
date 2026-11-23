package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestEnsureRestoreErrorKeepsSavedAndKillsRuntime(t *testing.T) {
	t.Run("non-limit error", func(t *testing.T) {
		r := newRig(t, testLimits, nil)
		r.write("rest", map[string]string{
			ManifestName: manifest("rest", nil, ""),
			"main.js":    block(``, `ui.text("ok")`),
		})
		r.state("rest", protocol.PluginReady)
		p, ok := r.m.plugin("rest")
		if !ok {
			t.Fatal("plugin rest missing")
		}
		err := p.do(context.Background(), func() error {
			p.saved["ui"] = "{bad-json"
			p.saved["ui@project:a"] = `{"x":1}`
			return p.ensure()
		})
		if err == nil {
			t.Fatal("expected ensure() to fail on corrupt restore state")
		}
		if r.runtimeUp("rest") {
			t.Fatal("runtime should be closed after non-limit restore error")
		}
		p.mu.Lock()
		running, failErr := p.running, p.failErr
		p.mu.Unlock()
		if running || failErr == "" {
			t.Fatalf("unexpected state: running=%v failErr=%q", running, failErr)
		}
		if len(p.saved) != 2 || p.saved["ui"] != "{bad-json" || p.saved["ui@project:a"] != `{"x":1}` {
			t.Fatalf("p.saved was lost or mutated: %#v", p.saved)
		}
		r.state("rest", protocol.PluginFailed)
	})

	t.Run("limit error kills runtime", func(t *testing.T) {
		r := newRig(t, testLimits, nil)
		r.write("restlim", map[string]string{
			ManifestName: manifest("restlim", nil, ""),
			"main.js":    block(``, `ui.text("ok")`),
		})
		r.state("restlim", protocol.PluginReady)
		p, ok := r.m.plugin("restlim")
		if !ok {
			t.Fatal("plugin restlim missing")
		}
		deep := strings.Repeat("[", 20000) + strings.Repeat("]", 20000)
		err := p.do(context.Background(), func() error {
			p.saved["ui"] = deep
			p.saved["ui@project:b"] = `{"ok":true}`
			return p.ensure()
		})
		if err == nil {
			t.Fatal("expected ensure() to fail on deeply nested restore JSON")
		}
		if r.runtimeUp("restlim") {
			t.Fatal("runtime should be nil after restore limit error")
		}
		p.mu.Lock()
		running, failErr := p.running, p.failErr
		p.mu.Unlock()
		if running || failErr == "" {
			t.Fatalf("unexpected state after limit error: running=%v failErr=%q", running, failErr)
		}
		if len(p.saved) != 2 || p.saved["ui"] != deep || p.saved["ui@project:b"] != `{"ok":true}` {
			t.Fatalf("p.saved was lost on limit error: %#v", p.saved)
		}
		info := r.state("restlim", protocol.PluginFailed)
		if info.GetError() == "" {
			t.Fatal("expected non-empty error on PluginInfo")
		}
	})
}
