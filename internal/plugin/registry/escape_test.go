package registry

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// A server's status line is its own to write: what the fetcher says of
// it holds none of its text, which a terminal printing the error would
// otherwise take as escape sequences.
func TestAStatusTextIsNotRepeated(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			_, _ = c.Read(buf)
			fmt.Fprintf(c, "HTTP/1.1 404 \x1b[2J\x1b]0;owned\x07Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			c.Close()
		}
	}()
	_, err = NewFetcher(true).Get(context.Background(), "http://"+ln.Addr().String()+"/x.json", 1<<20, 5*time.Second)
	if err == nil || strings.ContainsFunc(err.Error(), func(r rune) bool { return r < 0x20 }) || strings.Contains(err.Error(), "owned") ||
		!strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("the error is %q", err)
	}
}

// A URL holding a reordering mark is refused, as a name holding one is.
func TestURLsHoldNoReorderingMarks(t *testing.T) {
	if err := CheckURL("https://example.com/\u202egnp.exe", false); err == nil || !strings.Contains(err.Error(), "reordering") {
		t.Fatalf("CheckURL: %v", err)
	}
}
