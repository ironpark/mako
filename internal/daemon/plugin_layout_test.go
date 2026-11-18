//go:build unix

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
)

// change runs a plugin's change in the background, its outcome to come
// on the channel it returns.
func pluginChange(l *layoutDaemon, id string, c plugin.LayoutChange) <-chan [2]string {
	done := make(chan [2]string, 1)
	go func() {
		rev, err := pluginLayout{l.d}.Change(context.Background(), id, c)
		why := ""
		if err != nil {
			why = err.Error()
		}
		done <- [2]string{rev, why}
	}()
	return done
}

// A plugin's change of the layout takes the agents' flow: staged, shown,
// asked of the user in the bar alone (no session holds it), named for the
// plugin; allowed for that plugin alone, apart from agents and other
// plugins; never writing variants.
func TestAPluginsLayoutChangeIsStagedAndAsked(t *testing.T) {
	l := newLayoutDaemon(t)
	_ = userCompact(t, l)
	want := compactOf(t, l)
	pl := pluginLayout{l.d}
	resize := `[{"op": "resize", "id": "sidebar", "size": 300}]`
	c, err := pl.Prepare("", resize, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Document != "default" || c.Summary != "resize sidebar" {
		t.Fatalf("prepared %+v", c)
	}
	done := pluginChange(l, "ci", c)
	st := l.waitStage(t, protocol.LayoutApprove)
	if st.GetPluginId() != "ci" || st.GetSessionId() != "" || st.GetAgent() != "plugin ci" || st.GetTool() != toolPatchLayout {
		t.Fatalf("the stage is %v", st)
	}
	if l.s.Info().GetApproval() != nil {
		t.Fatal("an agent's session holds the plugin's approval")
	}
	if err := l.d.layouts.answer(st.GetId(), layoutAnswer{decision: protocol.DecisionAllow, option: protocol.LayoutAllowSpace}); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out[1] != "" || out[0] == "" {
		t.Fatalf("the change: %v", out)
	}
	if c := compactOf(t, l); c != want {
		t.Fatalf("a plugin's patch changed the compact root: %s; want %s", c, want)
	}
	// Allowed for the document, for this plugin alone.
	if !l.d.layouts.allowsPlugin("ci", "default") || l.d.layouts.allowsPlugin("other", "default") || l.d.layouts.allows("default") {
		t.Fatal("the allowance reached past the plugin")
	}
	c, err = pl.Prepare("", `[{"op": "resize", "id": "sidebar", "size": 280}]`, out[0])
	if err != nil {
		t.Fatal(err)
	}
	if out := <-pluginChange(l, "ci", c); out[1] != "" {
		t.Fatalf("an allowed change: %v", out)
	}
	// Another plugin is asked, and denied, by the window's LayoutDecision
	// as no session's approval answers it; a peer may not answer it.
	c, _ = pl.Prepare("", resize, "")
	done = pluginChange(l, "other", c)
	st = l.waitStage(t, protocol.LayoutApprove)
	deny, err := protocol.Encode(protocol.TypeLayoutDecision, "1", &protocol.LayoutDecision{StageId: st.GetId(), Decision: protocol.DecisionDeny})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.d.handleLayoutDecision(t.Context(), deny, true); err == nil {
		t.Fatal("a peer answered a plugin's change")
	}
	if _, err := l.d.handleLayoutDecision(t.Context(), deny, false); err != nil {
		t.Fatal(err)
	}
	if out := <-done; !strings.Contains(out[1], errLayoutDenied.Error()) {
		t.Fatalf("a denied change: %v", out)
	}
	// Removed, a plugin asks again.
	pl.Forget("ci")
	if l.d.layouts.allowsPlugin("ci", "default") {
		t.Fatal("the plugin removed is still allowed")
	}
	// With no window to show it, a change the user is to see is refused.
	l.window()
	c, _ = pl.Prepare("", resize, "")
	if out := <-pluginChange(l, "ci", c); !strings.Contains(out[1], errLayoutNoWindow.Error()) {
		t.Fatalf("a change with no window: %v", out)
	}
}

func TestAPluginsBadPatchIsRefusedAtOnce(t *testing.T) {
	l := newLayoutDaemon(t)
	pl := pluginLayout{l.d}
	cases := map[string]struct{ document, ops, ifRev, want string }{
		"json":     {"", `{`, "", "JSON array"},
		"field":    {"", `[{"op": "resize", "id": "sidebar", "size": 300, "variants": {}}]`, "", "unknown field"},
		"none":     {"", `[]`, "", "an operation"},
		"kindless": {"", `[{"op": "insert", "parent": "details", "node": {"id": "x"}}]`, "", "x"},
		"node":     {"", `[{"op": "remove", "id": "nowhere"}]`, "", "nowhere"},
		"rev":      {"", `[{"op": "resize", "id": "sidebar", "size": 300}]`, "1.0.x", "revision"},
		"document": {"../x", `[{"op": "resize", "id": "sidebar", "size": 300}]`, "", "name"},
	}
	for name, c := range cases {
		if _, err := pl.Prepare(c.document, c.ops, c.ifRev); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if l.d.layouts.current() != nil {
		t.Fatal("a refused patch was staged")
	}
}
