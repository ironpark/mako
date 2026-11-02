package gui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A terminal whose output stood still compresses its scrollback, and
// ReadTerminalMemory counts it; a closed one is no longer counted.
func TestATerminalCompressesItsScrollbackOnceQuiet(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 120, 40, func() {})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := range 10000 {
		fmt.Fprintf(&b, "\x1b[38;5;%dmline %d of the agent's output, with some words to fill it\x1b[0m\r\n", i%200, i)
	}
	conn.mu.Lock()
	conn.feedLocked([]byte(b.String()))
	conn.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	var m TerminalMemory
	for time.Now().Before(deadline) {
		if m = terminalMemoryOf(conn); m.Compressed > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	conn.mu.Lock()
	unsupported := conn.compressUnsupported
	conn.mu.Unlock()
	if unsupported {
		t.Skip("compression is not supported here")
	}
	if m.Compressed == 0 || m.Resident >= m.Virtual {
		t.Fatalf("not compressed: %+v", m)
	}
	t.Logf("%+v", m)
	conn.close()
	if terminalMemoryOf(conn).Terminals != 0 {
		t.Fatal("a closed terminal is counted")
	}
}

// terminalMemoryOf is ReadTerminalMemory of conn alone, as other tests'
// terminals may be live.
func terminalMemoryOf(conn *termConn) TerminalMemory {
	var out TerminalMemory
	for _, m := range readTerminalMemory(func(t *termConn) bool { return t == conn }) {
		out.Terminals += m.Terminals
		out.Virtual += m.Virtual
		out.Resident += m.Resident
		out.Compressed += m.Compressed
	}
	return out
}
