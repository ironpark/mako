package daemon

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

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
