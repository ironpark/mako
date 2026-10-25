package gui

import (
	"testing"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
)

func TestLiveProgressOverridesPollingAndWakesHiddenTabs(t *testing.T) {
	link := newFakeLink()
	woke := make(chan struct{}, 32)
	conn, err := newTermConn("s", link, 80, 24, func() { woke <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	defer close(link.in)
	// Simulate an unpainted hidden terminal whose ordinary output wake is pending.
	conn.pending.Store(true)
	m := newModel(config.Default(), "")
	m.Sessions.Set([]protocol.SessionInfo{{ID: "s", State: protocol.StateRunning, Busy: true}})
	m.conns["s"] = conn
	if !m.busy("s") {
		t.Fatal("missing metadata fallback before the first report")
	}
	for _, tc := range []struct {
		seq  string
		busy bool
	}{
		{"\x1b]9;4;3\x07", true},
		{"\x1b]9;4;0\x07", false},
		{"\x1b]9;4;1;42\x1b\\", true},
		{"\x1b]9;4;4;42\x1b\\", false},
		{"\x1b]9;4;3\x1b\\", true},
		{"\x1b]9;4;2;42\x07", false},
	} {
		// A PTY read is free to split an escape sequence at any byte.
		for _, b := range []byte(tc.seq) {
			link.output(t, string([]byte{b}))
		}
		select {
		case <-woke:
		case <-time.After(time.Second):
			t.Fatalf("no wake for %q", tc.seq)
		}
		if got := m.busy("s"); got != tc.busy {
			t.Fatalf("busy=%v after %q; stale poll=%v", got, tc.seq, ggui.Peek(m.Sessions)[0].Busy)
		}
	}
}

func TestSnapshotRestoresProgressWithoutNewOSC(t *testing.T) {
	link := newFakeLink()
	woke := make(chan struct{}, 8)
	conn, err := newTermConn("s", link, 80, 24, func() { woke <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	defer close(link.in)
	conn.pending.Store(true)
	for _, state := range []uint8{3, 0} {
		env, err := protocol.Encode(protocol.TypeSnapshot, "", protocol.Snapshot{SessionID: "s", Cols: 80, Rows: 24, Progress: &protocol.ProgressReport{State: state, Value: -1, RemainingMS: 1000}})
		if err != nil {
			t.Fatal(err)
		}
		link.in <- env
		select {
		case <-woke:
		case <-time.After(time.Second):
			t.Fatal("snapshot did not wake UI")
		}
		busy, known := conn.busyState()
		if !known || busy != (state == 3) {
			t.Fatalf("state=%d busy=%v known=%v", state, busy, known)
		}
	}
}
