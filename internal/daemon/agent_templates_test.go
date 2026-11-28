//go:build unix

package daemon

import (
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// templateDaemon is a daemon with an agent session whose MCP server the
// test calls, with a window watching for approvals until window is
// called.
func templateDaemon(t *testing.T) (*layoutDaemon, func()) {
	t.Helper()
	d, s := projectMCPDaemon(t)
	l := &layoutDaemon{d: d, s: s, cs: projectMCPClient(t, d, s)}
	return l, d.approvals.hold()
}

const goodTemplate = "---\ndescription: Review a file\nargs:\n  - name: file\n    required: true\n  - name: tone\n    type: choice\n    options: [kind, blunt]\n    default: kind\n---\n{#match tone}\n{:kind}\nBe kind.\n{:blunt}\nBe blunt.\n{/match}\nReview {{file}}.\n"

// What is wrong with a template is said, word for word as mako reads it,
// before the user is asked anything: bad front matter, an argument's
// unknown type, a match that misses a value, a name that is not one or
// would leave the folder, a built-in's name without replace, and a file
// too long.
func TestSaveTemplateRefusesWhatDoesNotRead(t *testing.T) {
	l, _ := templateDaemon(t)
	cases := []struct {
		args saveTemplateArgs
		want string
	}{
		{saveTemplateArgs{Name: "x", Content: "---\nargs: [\n---\nhi"}, "the template does not read: front matter:"},
		{saveTemplateArgs{Name: "x", Content: "---\nwhat: 1\n---\nhi"}, "field what not found"},
		{saveTemplateArgs{Name: "x", Content: "---\nargs: [{name: n, type: count}]\n---\n{{n}}"}, `argument "n": no type "count"`},
		{saveTemplateArgs{Name: "x", Content: "---\nargs: [{name: t, type: choice, options: [a, b], default: a}]\n---\n{#match t}{:a}A{/match}"}, `{#match t}: no arm matches "b"; add one, or {:else} for the rest`},
		{saveTemplateArgs{Name: "x", Content: "{{nope}}"}, "no argument, built-in variable or name defined before it is called nope"},
		{saveTemplateArgs{Name: "../x", Content: "hi"}, `the name "../x" is not one a template takes`},
		{saveTemplateArgs{Name: "a/b", Content: "hi"}, "is not one a template takes"},
		{saveTemplateArgs{Name: `a\b`, Content: "hi"}, "is not one a template takes"},
		{saveTemplateArgs{Name: ".hidden", Content: "hi"}, "is not one a template takes"},
		{saveTemplateArgs{Name: "a b", Content: "hi"}, "is not one a template takes"},
		{saveTemplateArgs{Name: "", Content: "hi"}, "is not one a template takes"},
		{saveTemplateArgs{Name: strings.Repeat("a", 65), Content: "hi"}, "is not one a template takes"},
		{saveTemplateArgs{Name: prompttmpl.MakerName, Content: "hi"}, "//" + prompttmpl.MakerName + " is a built-in template; choose another name, or set replace"},
		{saveTemplateArgs{Name: "big", Content: strings.Repeat("x", maxTemplate+1)}, "one is at most"},
	}
	for _, c := range cases {
		if _, why := l.call(t, toolSaveTemplate, c.args); !strings.Contains(why, c.want) {
			t.Errorf("%q: %q, want %q", c.args.Name, why, c.want)
		}
	}
	if a := l.s.Info().GetApproval(); a != nil {
		t.Fatalf("the user was asked: %v", a)
	}
}

// waitTemplate waits for the session to ask the user to save a template.
func waitTemplate(t *testing.T, l *layoutDaemon) *protocol.Approval {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a := l.s.Info().GetApproval(); a.GetKind() == protocol.ToolTemplate {
			return a
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the session asked nothing")
	return nil
}

// A template that reads is asked of the user as an approval on the
// agent's session, holding the file for the window to save; allowed by a
// window that saved it, the agent is told how to call it. Declined, or
// allowed by a front-end that cannot save it, the agent is told nothing
// was saved, with what the user said.
func TestSaveTemplateAsksTheUser(t *testing.T) {
	l, _ := templateDaemon(t)
	answer := func(a *protocol.Approval, decision, option, message string) {
		t.Helper()
		if err := l.d.templateAsks.answer(a.GetId(), templateAnswer{decision: decision, option: option, message: message}); err != nil {
			t.Fatal(err)
		}
	}

	done := l.callLater(t, toolSaveTemplate, saveTemplateArgs{Name: "review", Content: goodTemplate})
	a := waitTemplate(t, l)
	var in templateInput
	if err := json.Unmarshal([]byte(a.GetInput()), &in); err != nil || in.Name != "review" || in.Content != goodTemplate || in.Replace {
		t.Fatalf("the approval holds %q: %v", a.GetInput(), err)
	}
	if a.GetTool() != toolSaveTemplate || a.GetDetail() != "//review <file:string> [tone:kind|blunt=kind]" || l.s.Info().GetStatus() != protocol.AgentWaiting {
		t.Fatalf("approval %v, status %s", a, l.s.Info().GetStatus())
	}
	answer(a, protocol.DecisionAllow, protocol.TemplateSaved, "")
	out, why := result(t, done)
	if why != "" || out["name"] != "review" || out["call"] != "//review <file:string> [tone:kind|blunt=kind]" {
		t.Fatalf("saved: %v %s", out, why)
	}
	if l.s.Info().GetApproval() != nil || l.d.templateAsks.owns(a.GetId()) {
		t.Fatal("the approval stayed")
	}

	for _, c := range []struct {
		decision, option, message, want string
	}{
		{protocol.DecisionDeny, "", "call it lint", "the user declined to save the template: call it lint"},
		{protocol.DecisionDeny, "", "", "the user declined to save the template"},
		{protocol.DecisionTerminal, "", "", "the user declined to save the template"},
		{protocol.DecisionAllow, "", "", "cannot save templates; nothing was saved"},
	} {
		done := l.callLater(t, toolSaveTemplate, saveTemplateArgs{Name: "review", Content: goodTemplate, Replace: true})
		a := waitTemplate(t, l)
		answer(a, c.decision, c.option, c.message)
		if _, why := result(t, done); why != c.want && !strings.HasSuffix(why, c.want) {
			t.Errorf("%s %q: %q, want %q", c.decision, c.option, why, c.want)
		}
	}

	// A built-in's name, with replace, is asked as any other.
	done = l.callLater(t, toolSaveTemplate, saveTemplateArgs{Name: prompttmpl.MakerName, Content: "Mine", Replace: true})
	a = waitTemplate(t, l)
	answer(a, protocol.DecisionDeny, "", "")
	if _, why := result(t, done); why != errTemplateDeclined.Error() {
		t.Fatalf("a built-in's name: %s", why)
	}
}

// With no window watching, nothing is asked; a window that goes while
// the template waits leaves it refused.
func TestSaveTemplateWithNoWindow(t *testing.T) {
	l, window := templateDaemon(t)
	window()
	l.d.approvals.mu.Lock()
	l.d.approvals.watched, l.d.approvals.here = time.Time{}, time.Time{}
	l.d.approvals.mu.Unlock()
	if _, why := l.call(t, toolSaveTemplate, saveTemplateArgs{Name: "review", Content: goodTemplate}); why != errTemplateNoWindow.Error() {
		t.Fatalf("with no window: %q", why)
	}
	if l.s.Info().GetApproval() != nil {
		t.Fatal("the user was asked with no window")
	}

	window = l.d.approvals.hold()
	done := l.callLater(t, toolSaveTemplate, saveTemplateArgs{Name: "review", Content: goodTemplate})
	waitTemplate(t, l)
	window()
	l.d.approvals.mu.Lock()
	l.d.approvals.watched, l.d.approvals.here = time.Time{}, time.Time{}
	l.d.approvals.mu.Unlock()
	if _, why := result(t, done); why != errTemplateNoWindow.Error() {
		t.Fatalf("the window gone: %q", why)
	}
}

// A window with "Approve in the window" turned off still watches layouts and
// can show and save templates, which have no terminal fallback.
func TestSaveTemplateWorksWithLayoutWatchOnly(t *testing.T) {
	l, window := templateDaemon(t)
	window()
	l.d.approvals.mu.Lock()
	l.d.approvals.watched, l.d.approvals.here = time.Time{}, time.Time{}
	l.d.approvals.mu.Unlock()
	defer l.d.layouts.hold()()

	done := l.callLater(t, toolSaveTemplate, saveTemplateArgs{Name: "review", Content: goodTemplate})
	a := waitTemplate(t, l)
	if err := l.d.templateAsks.answer(a.GetId(), templateAnswer{decision: protocol.DecisionAllow, option: protocol.TemplateSaved}); err != nil {
		t.Fatal(err)
	}
	if out, why := result(t, done); why != "" || out["name"] != "review" {
		t.Fatalf("saved with layout watch only: %v %s", out, why)
	}
}

// Periodic statusLine updates (such as agy reporting agent_state="tool_use"
// while save_template runs) must not clear the pending template approval card.
func TestSaveTemplateSurvivesStatusLineHeartbeats(t *testing.T) {
	d := meshDaemon(t, "agy-template-statusline")
	s, err := session.Start(session.Spec{
		ID: "agy-save", NodeID: d.config().NodeID, Agent: "agy", Project: t.TempDir(),
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d.sessions.add(s)
	defer d.layouts.hold()()

	type saveRes struct {
		out saveTemplateResult
		err error
	}
	done := make(chan saveRes, 1)
	go func() {
		out, err := d.saveTemplate(t.Context(), s, saveTemplateArgs{Name: "review", Content: goodTemplate})
		done <- saveRes{out: out, err: err}
	}()

	var a *protocol.Approval
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cur := s.Info().GetApproval(); cur.GetKind() == protocol.ToolTemplate {
			a = cur
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if a == nil {
		t.Fatal("saveTemplate never posted an approval on the session")
	}

	sendStatus := func(seq int64, payload string) {
		t.Helper()
		client, server := net.Pipe()
		srvDone := make(chan struct{})
		go func() {
			defer close(srvDone)
			d.serveHook(t.Context(), server)
		}()
		if err := json.NewEncoder(client).Encode(hookMessage{Kind: statusArg, Session: s.ID(), Token: d.hookToken(s.ID()), Seq: seq, Input: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, client)
		_ = client.Close()
		<-srvDone
	}

	// Simulate agy statusLine heartbeats arriving with newer seqs while save_template blocks.
	now := time.Now().UnixNano() + int64(time.Second)
	sendStatus(now, `{"agent_state":"tool_use"}`)
	sendStatus(now+1, `{"agent_state":"idle"}`)

	if got := s.Info().GetApproval(); got == nil || got.GetId() != a.GetId() {
		t.Fatalf("approval was cleared by agy statusLine heartbeat: got %v", got)
	}
	if err := d.templateAsks.answer(a.GetId(), templateAnswer{decision: protocol.DecisionAllow, option: protocol.TemplateSaved}); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res.err != nil || res.out.Name != "review" {
			t.Fatalf("saved after heartbeat: %+v, err=%v", res.out, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("saveTemplate timed out after answer")
	}
}
