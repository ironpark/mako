package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		"":              "en",
		"   ":           "en",
		"en":            "en",
		"EN":            "en",
		"ko":            "ko",
		"ko_kr":         "ko-KR",
		"KO-KR":         "ko-KR",
		"zh_hans_cn":    "zh-Hans-CN",
		"pt-br":         "pt-BR",
		"bad locale!":   "en",
		"ko\n;rm -rf /": "en",
		"1234":          "en",
		"a":             "en",
	}
	for in, want := range cases {
		if got := normalizeLocale(in); got != want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
	raw := marshalScopeContext(ScopeContext{Scope: protocol.ScopeGlobal})
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m["locale"] != "en" {
		t.Fatalf("default marshaled locale = %v, want en", m["locale"])
	}
	rawKo := marshalScopeContext(ScopeContext{Scope: protocol.ScopeGlobal, Locale: "ko_kr"})
	if err := json.Unmarshal([]byte(rawKo), &m); err != nil {
		t.Fatal(err)
	}
	if m["locale"] != "ko-KR" {
		t.Fatalf("normalized marshaled locale = %v, want ko-KR", m["locale"])
	}
}

func (r *rig) watchLocale(block, scope, locale string) *watcher {
	r.t.Helper()
	id, _, _ := splitBlock(block)
	r.info(id, func(*protocol.PluginInfo) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	w := &watcher{t: r.t, cancel: cancel, changed: make(chan struct{}, 1)}
	go func() {
		err := r.m.WatchScopedLocale(ctx, block, scope, locale, func(u *protocol.BlockUpdate) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.updates++
			w.last = u
			switch {
			case u.GetTree() != nil:
				w.tree = u.GetTree()
			case len(u.GetOps()) > 0:
				next, err := protocol.ApplyUi(w.tree, u.GetOps())
				if err != nil {
					w.err = err
					return err
				}
				w.tree = next
			}
			select {
			case w.changed <- struct{}{}:
			default:
			}
			return nil
		})
		w.mu.Lock()
		if w.err == nil && err != nil && ctx.Err() == nil {
			w.err = err
		}
		w.mu.Unlock()
	}()
	r.t.Cleanup(cancel)
	return w
}

func TestI18nCatalogFallbackAndDynamicLocaleSwitch(t *testing.T) {
	r := newRig(t, testLimits, nil)
	man := manifest("i18ndemo", nil, `
[[contributes.tool]]
id = "greet"
description = "Localized greeting"
`)
	src := `
export default {
  activate(mako) {
    mako.i18n.register({
      en: {
        greeting: "Hello, {name} ({count})",
        nav: { home: "Home", notes: "Notes" },
        enOnly: "Fallback English",
      },
      ko: {
        greeting: "안녕하세요, {name} ({count})",
        nav: { home: "홈" },
      },
      "ko-KR": {
        "nav.notes": "노트",
      },
      ja: {
        greeting: "こんにちは、{name} ({count})",
      },
    });
    mako.i18n.register({
      ko: { cmd: "명령 실행됨: {loc}" },
      en: { cmd: "Command ran: {loc}" },
    });

    mako.blocks.register("ui", {
      state: { loadedLocale: "", loadedHome: "", eventMsg: "", cmdMsg: "" },
      load(s, ctx) {
        s.loadedLocale = ctx.locale;
        s.loadedHome = ctx.t("nav.home");
      },
      render(s, ui, ctx) {
        const line = [
          "loc=" + ctx.locale,
          "greet=" + ctx.t("greeting", { name: "Ada", count: 0 }),
          "home=" + ctx.t("nav.home"),
          "notes=" + ctx.t("nav.notes"),
          "enOnly=" + ctx.t("enOnly"),
          "miss=" + ctx.t("no.such.key", { x: 1 }),
          "loaded=" + s.loadedLocale + ":" + s.loadedHome,
          "ev=" + s.eventMsg,
          "cmd=" + s.cmdMsg,
        ].join("|");
        return ui.column([
          ui.text(line, { key: "out" }),
          ui.button(ctx.t("nav.home"), { key: "btn", onTap: ["ping"] }),
        ]);
      },
      on: {
        ping(s) {
          const ctx = mako.blocks.context("ui");
          s.eventMsg = ctx.locale + ":" + mako.i18n.t("nav.home") + ":" + ctx.t("nav.notes");
        },
      },
    });

    mako.commands.register("go", (ctx) => {
      const s = mako.blocks.state("ui");
      s.cmdMsg = ctx.t("cmd", { loc: ctx.locale });
    });

    mako.tools.register("greet", (args, ctx) => ({
      locale: ctx.locale,
      text: ctx.t("greeting", { name: args.name || "World", count: 2 }),
    }));
  },
};`
	r.write("i18ndemo", map[string]string{ManifestName: man, "main.js": src})

	// 1. Initial watch with "ko-KR": load and render both see "ko-KR" with
	// exact ("ko-KR") -> primary ("ko") -> "en" -> key fallback and {var} interpolation.
	wKo := r.watchLocale("plugin:i18ndemo/ui", "", "ko_kr")
	wantKo := "loc=ko-KR|greet=안녕하세요, Ada (0)|home=홈|notes=노트|enOnly=Fallback English|miss=no.such.key|loaded=ko-KR:홈|ev=|cmd="
	wKo.until("initial ko-KR render", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, wantKo)
	})

	// 2. BlockEvent carries explicit locale "ko" into the event handler.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.m.Event(ctx, &protocol.BlockEvent{
		Block:  "plugin:i18ndemo/ui",
		Node:   "/btn",
		Kind:   protocol.UiTap,
		Locale: "ko",
	}); err != nil {
		t.Fatal(err)
	}
	wKo.until("event in ko locale", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "ev=ko:홈:Notes") && hasText(n, "loc=ko|")
	})

	// 3. PluginAction command receives Locale from PluginAction.
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "i18ndemo",
		Op:       protocol.PluginRun,
		Command:  "go",
		Locale:   "ko-KR",
	}); err != nil {
		t.Fatal(err)
	}
	wKo.until("command in ko-KR locale", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "cmd=명령 실행됨: ko-KR")
	})

	// 4. Watcher reconnects with a different locale ("en") -> immediate re-render in "en".
	wKo.cancel()
	wEn := r.watchLocale("plugin:i18ndemo/ui", "", "en")
	wEn.until("re-render in en locale", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "loc=en|greet=Hello, Ada (0)|home=Home|notes=Notes|enOnly=Fallback English|miss=no.such.key|")
	})

	// 5. Reconnect with "ja" -> immediate re-render in "ja" falling back to "en" for missing keys.
	wEn.cancel()
	wJa := r.watchLocale("plugin:i18ndemo/ui", "", "ja")
	wJa.until("re-render in ja locale", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "loc=ja|greet=こんにちは、Ada (0)|home=Home|notes=Notes|enOnly=Fallback English|")
	})

	// 6. Scoped tool call receives ScopeContext.Locale.
	out, err := r.m.CallToolScoped(ctx, "i18ndemo", "greet", []byte(`{"name":"Mako"}`), ScopeContext{Locale: "ko"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"locale":"ko"`) || !strings.Contains(out, "안녕하세요, Mako (2)") {
		t.Fatalf("unexpected tool output: %s", out)
	}
}
