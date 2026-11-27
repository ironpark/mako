package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent/codex"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

// ask sends req on one end of a pipe to handle, serving the other, and
// returns the stream it is answered on.
func ask(t *testing.T, handle func(*transport.Stream, *protocol.Envelope) error, typ protocol.Type, req proto.Message) (*transport.Stream, <-chan error) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	done := make(chan error, 1)
	go func() {
		served := transport.NewStream(b)
		env, err := served.Recv()
		if err != nil {
			done <- err
			return
		}
		done <- handle(served, env)
	}()
	stream := transport.NewStream(a)
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := stream.Send(typ, "", req); err != nil {
		t.Fatal(err)
	}
	return stream, done
}

// Send pastes as the program asked, bracketed here, then types its keys.
func TestSendPastesThenTypes(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	s, err := session.Start(session.Spec{
		ID: "s", NodeID: "test-node", Agent: "shell", Project: dir,
		Command: []string{"sh", "-c", `stty -echo; printf '\033[?2004h'; IFS= read -r line; printf %s "$line" > out`},
		Cols:    40, Rows: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	waitFor(t, "the program never asked for bracketed paste", func() bool {
		snap, err := s.Snapshot(true)
		return err == nil && strings.Contains(snap.GetText(), "\x1b[?2004h")
	})

	stream, _ := ask(t, func(st *transport.Stream, env *protocol.Envelope) error { return d.handleSend(t.Context(), st, env) },
		protocol.TypeSend, &protocol.Send{SessionId: "s", Text: "hi there", Keys: []byte("\r")})
	env, err := stream.Recv()
	if err != nil || env.Type() != protocol.TypeSessionInfo {
		t.Fatalf("reply %v, %v", env, err)
	}
	var got []byte
	waitFor(t, "the line never came", func() bool {
		got, _ = os.ReadFile(out)
		return len(got) > 0
	})
	if want := "\x1b[200~hi there\x1b[201~"; string(got) != want {
		t.Fatalf("read %q, want %q", got, want)
	}

	// A paste that would end itself early is refused.
	stream, _ = ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
		if err := d.handleSend(t.Context(), st, env); err != nil {
			return protocol.WriteError(st, env.GetId(), err)
		}
		return nil
	}, protocol.TypeSend, &protocol.Send{SessionId: "s", Text: "a\x1b[201~rm -rf"})
	if env, err := stream.Recv(); err != nil || env.Type() != protocol.TypeError {
		t.Fatalf("an early end marker was let through: %v, %v", env, err)
	}
}

// A front-end watching for approvals on its watch stream counts as
// watching for as long as the stream stays open, polling or not.
func TestAWatchHoldsApprovals(t *testing.T) {
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	stream, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
		return d.handleWatch(t.Context(), st, env, false)
	},
		protocol.TypeWatch, &protocol.Watch{Approvals: true})
	if env, err := stream.Recv(); err != nil || env.Type() != protocol.TypeChanged {
		t.Fatalf("first frame %v, %v", env, err)
	}
	d.approvals.mu.Lock()
	d.approvals.watched, d.approvals.here = time.Time{}, time.Time{} // long since any listing
	d.approvals.mu.Unlock()
	if !d.approvals.watching() || !d.approvals.watchedHere() {
		t.Fatal("an open watch is not watching")
	}
	stream.Close()
	<-done
	d.approvals.mu.Lock()
	held := d.approvals.held
	d.approvals.mu.Unlock()
	if held != 0 {
		t.Fatalf("%d watches held after the stream closed", held)
	}

	// A peer's watch holds nothing: its node says so in its state.
	stream, done = ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
		return d.handleWatch(t.Context(), st, env, true)
	},
		protocol.TypeWatch, &protocol.Watch{Approvals: true})
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	d.approvals.mu.Lock()
	held = d.approvals.held
	d.approvals.mu.Unlock()
	if held != 0 {
		t.Fatal("a peer's watch was held")
	}
	stream.Close()
	<-done
}

// Questions a terminal's dialog asks are answered by typing there, as the
// agent's driver says, once the dialog shows; not before, nor unanswered.
func TestAnswerQuestionTypesTheAnswers(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	ready := filepath.Join(dir, "ready")
	s, err := session.Start(session.Spec{
		ID: "s", NodeID: "test-node", Agent: "codex", Project: dir,
		Command: []string{"sh", "-c", `stty raw -echo; printf '\033[?2004h'; while [ ! -e ready ]; do sleep 0.05; done; ` +
			`printf '  Which color?\r\n  > 1. Red\r\n    2. Blue\r\n  tab to add notes | enter to submit all | esc to interrupt\r\n'; cat > out`},
		Cols: 80, Rows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	questions := []*protocol.Question{{Question: "Which color?", Options: []*protocol.QuestionOption{{Label: "Red"}, {Label: "Blue"}}}}
	answer := func(answers []*protocol.QuestionAnswer) *protocol.Envelope {
		stream, _ := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
			if err := d.handleAnswerQuestion(t.Context(), st, env); err != nil {
				return protocol.WriteError(st, env.GetId(), err)
			}
			return nil
		}, protocol.TypeAnswerQuestion, &protocol.AnswerQuestion{SessionId: "s", Questions: questions, Answers: answers})
		env, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	mine := []*protocol.QuestionAnswer{{Picked: []int32{1}, Text: "a darker one"}}
	if env := answer(mine); env.Type() != protocol.TypeError {
		t.Fatal("answered with no dialog shown")
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the dialog never showed", func() bool {
		snap, err := s.Snapshot(false)
		return err == nil && strings.Contains(snap.GetText(), "enter to submit all")
	})
	if env := answer([]*protocol.QuestionAnswer{{}}); env.Type() != protocol.TypeError {
		t.Fatal("typed an empty answer")
	}
	if env := answer(mine); env.Type() != protocol.TypeSessionInfo {
		t.Fatalf("reply %v", env)
	}
	want := "\x1b[B\t\x1b[200~a darker one\x1b[201~\r"
	var got []byte
	waitFor(t, "the answer never came", func() bool {
		got, _ = os.ReadFile(out)
		return len(got) >= len(want)
	})
	if string(got) != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
}

func TestAnswerQuestionTypesAgyMultiQuestionAnswers(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	s, err := session.Start(session.Spec{
		ID: "s", NodeID: "test-node", Agent: "agy", Project: dir,
		Command: []string{"sh", "-c", `stty raw -echo; printf '\033[?2004h\033[>5u'; ` +
			`printf 'Question 1/2: Pick color\r\n> 1. Red\r\n  2. Blue\r\n  3. Write-in...\r\n  ↑/↓ Navigate · → Next · enter Select · esc Skip\r\nesc to cancel\r\n'; cat > out`},
		Cols: 120, Rows: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)

	waitFor(t, "agy dialog never showed", func() bool {
		snap, err := s.Snapshot(false)
		return err == nil && strings.Contains(snap.GetText(), "esc Skip")
	})

	questions := []*protocol.Question{
		{Question: "Pick color", Options: []*protocol.QuestionOption{{Label: "Red"}, {Label: "Blue"}}},
		{Question: "Pick fruits", MultiSelect: true, Options: []*protocol.QuestionOption{{Label: "Apple"}, {Label: "Banana"}}},
	}
	answers := []*protocol.QuestionAnswer{
		{Picked: []int32{1}},
		{Picked: []int32{0}, Text: "Mango"},
	}
	stream, _ := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
		if err := d.handleAnswerQuestion(t.Context(), st, env); err != nil {
			return protocol.WriteError(st, env.GetId(), err)
		}
		return nil
	}, protocol.TypeAnswerQuestion, &protocol.AnswerQuestion{SessionId: "s", Questions: questions, Answers: answers})
	env, err := stream.Recv()
	if err != nil || env.Type() != protocol.TypeSessionInfo {
		t.Fatalf("reply %v, %v", env, err)
	}
	// Q1: Down + Enter; Q2: Space + Down + Down + Enter, then bracketed paste "Mango" + Enter.
	want := "\x1b[B\r \x1b[B\x1b[B\r\x1b[200~Mango\x1b[201~\r"
	var got []byte
	waitFor(t, "agy answers never came", func() bool {
		got, _ = os.ReadFile(out)
		return len(got) >= len(want)
	})
	if string(got) != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
}

func TestHandleSendRoutesCodexUserPromptViaBridgeWithFallback(t *testing.T) {
	dir := t.TempDir()
	ptyOut := filepath.Join(dir, "pty.out")
	s, err := session.Start(session.Spec{
		ID: "codex-gui", NodeID: "test-node", Agent: "codex", Project: dir,
		Command: []string{"sh", "-c", `stty -echo; printf '\033[?2004h'; while IFS= read -r line; do printf '%s\n' "$line" >> pty.out; done`},
		Cols:    80, Rows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitFor(t, "pty never enabled bracketed paste", func() bool {
		snap, err := s.Snapshot(true)
		return err == nil && strings.Contains(snap.GetText(), "\x1b[?2004h")
	})

	var mu sync.Mutex
	var queued []string
	failRPC := false
	srv := startMockCodexWSServer(t, func(id json.RawMessage, method string, params json.RawMessage, reply func(any)) {
		if method == "thread/queue/add" {
			mu.Lock()
			fail := failRPC
			mu.Unlock()
			if fail {
				reply(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "temporary failure"}})
				return
			}
			var p struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(params, &p)
			if len(p.Input) > 0 {
				mu.Lock()
				queued = append(queued, p.Input[0].Text)
				mu.Unlock()
			}
			reply(map[string]any{"id": id, "result": map[string]any{"queuedSubmission": map[string]any{"id": "q1"}}})
		}
	})

	client, err := codex.DialAppServerWS(t.Context(), srv.wsURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	d := New(config.Default(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.sessions.add(s)
	bctx, bcancel := context.WithCancel(t.Context())
	defer bcancel()
	d.attachCodexBridge(s, &codexBridge{
		sessionID: "codex-gui",
		client:    client,
		ctx:       bctx,
		cancel:    bcancel,
		tid:       "thread-1",
	})

	send := func(text string, keys []byte) {
		t.Helper()
		st, done := ask(t, func(st *transport.Stream, env *protocol.Envelope) error {
			return d.handleSend(t.Context(), st, env)
		}, protocol.TypeSend, &protocol.Send{SessionId: "codex-gui", Text: text, Keys: keys})
		env, err := st.Recv()
		if err != nil || env.Type() != protocol.TypeSessionInfo {
			t.Fatalf("handleSend(%q): %v, %v", text, env, err)
		}
		if err := <-done; err != nil {
			t.Fatalf("handleSend(%q) err: %v", text, err)
		}
	}

	// 1. Standard CR and Kitty CSI-u Enter route natively to thread/queue/add.
	send("hello codex", []byte("\r"))
	send("second prompt", []byte("\x1b[13u"))
	mu.Lock()
	gotQueued := append([]string(nil), queued...)
	mu.Unlock()
	if len(gotQueued) != 2 || gotQueued[0] != "hello codex" || gotQueued[1] != "second prompt" {
		t.Fatalf("queued = %v, want [hello codex, second prompt]", gotQueued)
	}
	if b, _ := os.ReadFile(ptyOut); len(b) != 0 {
		t.Fatalf("expected PTY untouched for native codex prompts, got %q", string(b))
	}

	// 2. Slash commands and !shell escapes bypass bridge and go to PTY.
	send("/model", []byte("\r"))
	send("!ls", []byte("\r"))
	waitFor(t, "slash/shell commands never reached PTY", func() bool {
		b, _ := os.ReadFile(ptyOut)
		return strings.Contains(string(b), "/model") && strings.Contains(string(b), "!ls")
	})

	// 3. AgentWaiting without approval card (interactive terminal prompt) bypasses bridge.
	s.SetStatus(protocol.AgentWaiting)
	send("manual terminal answer", []byte("\r"))
	waitFor(t, "AgentWaiting input never reached PTY", func() bool {
		b, _ := os.ReadFile(ptyOut)
		return strings.Contains(string(b), "manual terminal answer")
	})
	s.SetStatus(protocol.AgentIdle)

	// 4. Bridge RPC error falls back cleanly to PTY.
	mu.Lock()
	failRPC = true
	mu.Unlock()
	send("fallback prompt", []byte("\r"))
	waitFor(t, "failed bridge prompt never fell back to PTY", func() bool {
		b, _ := os.ReadFile(ptyOut)
		return strings.Contains(string(b), "fallback prompt")
	})
}
