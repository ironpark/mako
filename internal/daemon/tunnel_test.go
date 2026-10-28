package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// A front-end reaches a web server on a peer's loopback through its own
// daemon: the request goes in, the whole reply comes back, and the
// connection ends when the server's does.
func TestATunnelReachesAPeersLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("mako "), 200<<10) // a megabyte
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		fmt.Fprintf(w, "%s ", r.URL.Path)
		w.Write(body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	a := meshDaemon(t, "a")
	b := meshDaemon(t, "b", p2pAddr(a))
	letIn(t, a, b)
	letIn(t, b, a)
	c := dialDaemon(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitFor(t, "a linked to b", func() bool { n := nodeOf(ctx, t, c, b.cfg.NodeID); return n != nil && n.GetTrustsUs() })

	for _, node := range []string{"", b.cfg.NodeID} { // on a itself, then relayed to b
		conn, err := c.Tunnel(ctx, &protocol.Tunnel{NodeId: node, Port: int32(port)})
		if err != nil {
			t.Fatalf("tunnel to %q: %v", node, err)
		}
		fmt.Fprintf(conn, "GET /hello HTTP/1.1\r\nHost: localhost:%d\r\n\r\n", port)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("node %q: %v", node, err)
		}
		got, err := io.ReadAll(resp.Body)
		conn.Close()
		if err != nil || !bytes.Equal(got, append([]byte("/hello "), body...)) {
			t.Fatalf("node %q: %d bytes, %v", node, len(got), err)
		}
	}

	ln.Close()
	if _, err := c.Tunnel(ctx, &protocol.Tunnel{NodeId: b.cfg.NodeID, Port: int32(port)}); err == nil {
		t.Fatal("a tunnel to a port nothing answers on opened")
	}
	if _, err := c.Tunnel(ctx, &protocol.Tunnel{Port: 70000}); err == nil {
		t.Fatal("a tunnel to no port opened")
	}
}

// A file goes up and comes down in pieces, whatever its bytes; a piece out
// of place is refused.
func TestAFileMovesInPieces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.bin")
	data := make([]byte, 10_000)
	for i := range data {
		data[i] = byte(i)
	}
	if _, err := writeFile(p, data[:4000], time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := writePiece(p, 4000, data[4000:8000]); err != nil {
		t.Fatal(err)
	}
	if _, err := writePiece(p, 4000, data[8000:]); err == nil {
		t.Fatal("a piece sent twice was written")
	}
	if _, err := writePiece(p, 8000, data[8000:]); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for off := int64(0); ; {
		f, err := readPiece(p, off, 3000)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, f.GetData()...)
		off += int64(len(f.GetData()))
		if !f.GetTruncated() {
			break
		}
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read back %d bytes, unlike what went up", len(got))
	}
	if f, err := readPiece(p, 20_000, 0); err != nil || len(f.GetData()) != 0 || f.GetTruncated() {
		t.Fatalf("past the end: %v, %v", f, err)
	}
}
