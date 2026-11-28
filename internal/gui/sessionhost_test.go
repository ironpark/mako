package gui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

// hostTest is a window with session s on a node that hosts it, its
// host's actions taken by a fake that answers each as answer says, and
// what the window hands its UI goroutine run as the test drains it.
type hostTest struct {
	t      *testing.T
	m      *model
	state  *chatState
	mu     sync.Mutex
	sent   []*protocol.SessionAction
	answer func(*protocol.SessionAction) (*protocol.SessionActionResult, error)
	ui     chan func()
	seq    uint64
}

func newHostTest(t *testing.T) *hostTest {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	ht := &hostTest{t: t, m: newModel(config.Default(), ""), ui: make(chan func(), 64), seq: 100}
	m := ht.m
	m.Conn.Set(connState{OK: true, Self: &protocol.Node{Id: "n", Revision: protocol.Revision}})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", NodeId: "n", Agent: "claude", Project: "/tmp"}})
	m.post = func(fn func()) { ht.ui <- fn }
	m.hostDial = func(context.Context) (hostActor, error) { return ht, nil }
	ht.state = m.chatState("s")
	ht.state.host.views = 1 // shown
	return ht
}

func (ht *hostTest) SessionAction(_ context.Context, req *protocol.SessionAction) (*protocol.SessionActionResult, error) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.sent = append(ht.sent, req)
	if ht.answer != nil {
		return ht.answer(req)
	}
	ht.seq++
	return &protocol.SessionActionResult{Seq: ht.seq}, nil
}

// drafts are the drafts sent to the host so far.
func (ht *hostTest) drafts() []string {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	var out []string
	for _, r := range ht.sent {
		if d := r.GetSetDraft(); d != nil {
			out = append(out, d.GetText())
		}
	}
	return out
}

// drain runs what the window handed its UI goroutine until it has handed
// nothing for a moment.
func (ht *hostTest) drain() {
	for {
		select {
		case fn := <-ht.ui:
			fn()
		case <-time.After(hostDraftDelay + 200*time.Millisecond):
			return
		}
	}
}

// took has the window take the host's state: whole, or an event of the
// draft by client, with its seq.
func (ht *hostTest) took(text, by string, whole bool) {
	ht.seq++
	dr := &protocol.HostDraft{Text: text, ClientId: by, At: timestamppb.Now()}
	clients := []*protocol.HostClient{{Id: hostClient.id, Name: hostClient.name}, {Id: "phone", Name: "phone"}}
	ht.m.tookHost("s", ht.state, hostSeen{draft: dr, clients: clients, seq: ht.seq, whole: whole, drafted: !whole, origin: &protocol.HostOrigin{ClientId: by}})
}

// typed is the composer written in here, as its watch sees it.
func (ht *hostTest) typed(text string) {
	ht.m.setComposerDraft("s", text)
	ht.m.draftWritten("s", ht.state, text)
}

func (ht *hostTest) composer(want string) {
	ht.t.Helper()
	if got := ht.m.composerDraft("s"); got != want {
		ht.t.Fatalf("composer %q, want %q", got, want)
	}
}

// The draft another front-end writes shows in the composer, unless it is
// being written in here, holds files, or was emptied here a moment ago;
// and who else has the session open shows over it.
func TestComposerTakesTheHostsDraft(t *testing.T) {
	ht := newHostTest(t)
	m, state := ht.m, ht.state

	ht.took("from the phone", "phone", true)
	ht.composer("from the phone")
	if others := ggui.Peek(state.host.others); len(others) != 1 || others[0].GetId() != "phone" {
		t.Fatalf("others: %v", others)
	}

	// Written in here: the phone's next draft waits.
	ht.typed("mine")
	ht.took("the phone again", "phone", false)
	ht.composer("mine")
	state.host.edited = time.Now().Add(-hostDraftTyping)
	ht.took("the phone once more", "phone", false)
	ht.composer("the phone once more")

	// Emptied here a moment ago: the empty composer stands.
	ht.typed("")
	ht.took("the phone after", "phone", false)
	ht.composer("")
	state.host.edited = time.Now().Add(-hostDraftTyping)

	// With files attached, another's text would not go with them.
	m.attachTo("s", []string{"/tmp/a.png"})
	ht.took("text without the file", "phone", false)
	ht.composer("")
	state.attached.Set(nil)

	// This window's own draft, as the host says it back, changes nothing.
	m.setComposerDraft("s", "typed on")
	ht.took("typed", hostClient.id, false)
	ht.composer("typed on")

	p := ggui.ProbeBuilder(func() ggui.Widget { return hostPresence(state) }, ggui.Sz(400, 40))
	defer p.Close()
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, Tf("Also open on %s", "phone")); !ok {
		t.Fatalf("no presence:\n%s", p.Semantics().String())
	}
}

// Followed anew, as a tab shows again or the stream comes back, the
// composer and the host settle on what was written since they last
// agreed: a draft left here is not written over a newer one there, and
// one written here while away goes there.
func TestComposerAndHostSettleWhenFollowedAgain(t *testing.T) {
	ht := newHostTest(t)
	ht.typed("first words")
	ht.drain()
	if got := ht.drafts(); len(got) != 1 || got[0] != "first words" {
		t.Fatalf("drafts sent %q", got)
	}
	ht.took("first words", hostClient.id, false)
	ht.state.host.edited = time.Now().Add(-hostDraftTyping)

	// Away, the phone wrote on: the draft left here is stale.
	ht.took("first words, and more from the phone", "phone", true)
	ht.composer("first words, and more from the phone")
	if got := ht.drafts(); len(got) != 1 {
		t.Fatalf("a stale draft went to the host: %q", got)
	}

	// Away, this window was written in, the host's draft as they left it.
	ht.m.setComposerDraft("s", "written while away")
	ht.took("first words, and more from the phone", "phone", true)
	ht.drain()
	if got := ht.drafts(); len(got) != 2 || got[1] != "written while away" {
		t.Fatalf("drafts sent %q", got)
	}
}

// Emptying the composer while a draft is on its way to the host empties
// the host's too, and a message sent with it clears the draft there,
// after the draft before it: neither comes back.
func TestASentDraftDoesNotComeBack(t *testing.T) {
	ht := newHostTest(t)
	ht.m.commitDraft("s", ht.state, "foo") // on its way
	ht.typed("")
	ht.drain()
	if got := ht.drafts(); len(got) != 2 || got[1] != "" {
		t.Fatalf("drafts sent %q, want foo then empty", got)
	}

	ht.typed("send me")
	ht.m.sendingPrompt(ht.state) // Enter, before the draft was due
	ht.m.composerCleared(ht.state)
	ht.m.setComposerDraft("s", "")
	ht.m.draftWritten("s", ht.state, "")
	ht.drain()
	if got := ht.drafts(); len(got) != 2 {
		t.Fatalf("drafts sent %q after the message", got)
	}
}

// A view of a chat let go while another shows it leaves the host followed;
// the last to go sends the draft due.
func TestViewsOfAChatShareItsHost(t *testing.T) {
	ht := newHostTest(t)
	ht.state.host.views = 0
	var first, second func()
	dispose := ggui.Root(func() {
		first = ht.m.followHost("s", ht.state)
		second = ht.m.followHost("s", ht.state)
	})
	defer dispose()
	ht.state.host.others.Set([]*protocol.HostClient{{Id: "phone"}})
	ht.m.sendDraftSoon("s", ht.state, "half written")
	first()
	first()
	if ht.state.host.stop == nil || len(ggui.Peek(ht.state.host.others)) != 1 {
		t.Fatal("a view let go stopped the follow of the one still showing")
	}
	second()
	if ht.state.host.stop != nil || ggui.Peek(ht.state.host.others) != nil {
		t.Fatal("the last view let go left the host followed")
	}
	ht.drain()
	if got := ht.drafts(); len(got) != 1 || got[0] != "half written" {
		t.Fatalf("drafts sent %q", got)
	}
}

// A draft the host refuses says why over the composer.
func TestARefusedDraftSaysWhy(t *testing.T) {
	ht := newHostTest(t)
	ht.answer = func(*protocol.SessionAction) (*protocol.SessionActionResult, error) {
		return nil, &protocol.CodedError{Code: protocol.CodeNotTaken, Message: "remote: a draft is at most 65536 bytes"}
	}
	ht.m.commitDraft("s", ht.state, "too long")
	ht.drain()
	if got := ggui.Peek(ht.state.host.note); got != Tf("The draft was not shared: %s", "remote: a draft is at most 65536 bytes") {
		t.Fatalf("note %q", got)
	}
	if ht.state.host.flying {
		t.Fatal("a refused draft is still on its way")
	}
}

// An action lost on the way is sent again, the same, through the client
// as it is then; one refused is not.
func TestAHostActionIsTriedAgainOnlyWhenLost(t *testing.T) {
	var dials, tries int
	lost := errors.New("connection reset")
	var seqs []uint64
	dial := func(context.Context) (hostActor, error) {
		dials++
		return actorFunc(func(req *protocol.SessionAction) (*protocol.SessionActionResult, error) {
			tries++
			seqs = append(seqs, req.GetClientSeq())
			if tries < 3 {
				return nil, lost
			}
			return &protocol.SessionActionResult{Seq: 9}, nil
		}), nil
	}
	req := &protocol.SessionAction{ClientId: hostClient.id, ClientSeq: 42}
	res, err := sendHostAction(t.Context(), dial, req)
	if err != nil || res.GetSeq() != 9 || dials != 3 {
		t.Fatalf("res %v, err %v, %d dials", res, err, dials)
	}
	for _, s := range seqs {
		if s != 42 {
			t.Fatalf("sent again as %d", s)
		}
	}

	// A daemon that passed it on failing is no refusal of the host's.
	tries = 0
	relayed := &protocol.CodedError{Message: "remote: the peer went away"}
	res, err = sendHostAction(t.Context(), func(context.Context) (hostActor, error) {
		return actorFunc(func(*protocol.SessionAction) (*protocol.SessionActionResult, error) {
			if tries++; tries == 1 {
				return nil, relayed
			}
			return &protocol.SessionActionResult{Seq: 10}, nil
		}), nil
	}, req)
	if err != nil || res.GetSeq() != 10 || tries != 2 {
		t.Fatalf("after a relay failed: %v %v, %d tries", res, err, tries)
	}

	tries = 0
	refused := &protocol.CodedError{Code: protocol.CodeNotTaken, Message: "the text ends a paste"}
	_, err = sendHostAction(t.Context(), func(context.Context) (hostActor, error) {
		return actorFunc(func(*protocol.SessionAction) (*protocol.SessionActionResult, error) {
			tries++
			return nil, refused
		}), nil
	}, req)
	if !errors.Is(err, refused) || tries != 1 {
		t.Fatalf("a refused action: %v, %d tries", err, tries)
	}
}

type actorFunc func(*protocol.SessionAction) (*protocol.SessionActionResult, error)

func (f actorFunc) SessionAction(_ context.Context, req *protocol.SessionAction) (*protocol.SessionActionResult, error) {
	return f(req)
}
