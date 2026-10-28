package gui

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
)

// daemonApp is the window with a daemon of its own, for what goes through
// one.
func daemonApp(t *testing.T) (*model, *ggui.Probe) {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	cfg := config.Default()
	cfg.P2P.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded", Language: "en"}.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(context.Background())
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1200, 800))
	p.Setup(func() {
		setup(m)
		m.start(ctx, p.Post)
	})
	t.Cleanup(func() {
		cancel()
		m.shutdown()
		p.Close()
	})
	probeUntil(t, p, "the app's daemon", func() bool { return ggui.Peek(m.Conn).OK })
	return m, p
}

// A folder goes up and comes back whole: a file larger than a piece, an
// empty one and a dotfile, beside a name already taken, which it does not
// replace.
func TestAFolderGoesUpAndComesBack(t *testing.T) {
	m, _ := daemonApp(t)
	src, dst, back := t.TempDir(), t.TempDir(), t.TempDir()
	big := bytes.Repeat([]byte{0, 1, 2, 3, 0xff}, (stageChunk*2+1000)/5)
	files := map[string][]byte{"big.bin": big, "empty": nil, ".env": []byte("A=1\n"), "sub/deeper/note.md": []byte("# hi\n")}
	for name, data := range files {
		p := filepath.Join(src, "proj", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dst, "proj"), 0o755); err != nil { // taken
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := m.client(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tr := &transfer{}
	if err := upload(ctx, c, "", dst, []string{filepath.Join(src, "proj")}, tr); err != nil {
		t.Fatal(err)
	}
	if want := int64(len(big) + 4 + 5); tr.done.Load() != want {
		t.Fatalf("%d bytes went up, not %d", tr.done.Load(), want)
	}
	got, err := download(ctx, c, "", filepath.Join(dst, "proj 2"), true, back, &transfer{})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(got, name))
		if err != nil || !bytes.Equal(data, want) {
			t.Fatalf("%s came back as %d bytes, %v", name, len(data), err)
		}
	}
}

// A tunnel listens here and carries what reaches it to the port on the
// node, as long as it is open.
func TestATunnelCarriesConnections(t *testing.T) {
	m, p := daemonApp(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "served "+r.URL.Path) })}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	var tun tunnelInfo
	m.forward("", port, func(t tunnelInfo) { tun = t })
	probeUntil(t, p, "the tunnel", func() bool { return tun.Local != 0 })
	if tun.Local == port {
		t.Fatal("the tunnel took a port that was taken")
	}
	if got := ggui.Peek(m.Tunnels.List); len(got) != 1 || got[0] != tun {
		t.Fatalf("tunnels listed: %v", got)
	}
	resp, err := http.Get("http://localhost:" + strconv.Itoa(tun.Local) + "/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "served /x" {
		t.Fatalf("through the tunnel: %q", body)
	}
	m.stopTunnel(tun)
	if conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(tun.Local)); err == nil {
		conn.Close()
		t.Fatal("a stopped tunnel still listens")
	}
	ln.Close()
	if _, err := m.listen("", port); err == nil {
		t.Fatal("a tunnel opened to a port nothing answers on")
	}
}

func TestLoopbackHosts(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "app.localhost": true, "127.0.0.1": true, "127.1.2.3": true, "::1": true, "0.0.0.0": true,
		"example.com": false, "192.168.0.2": false, "localhost.example.com": false,
	} {
		if loopbackHost(host) != want {
			t.Errorf("loopbackHost(%q) = %v", host, !want)
		}
	}
}

func TestFreeName(t *testing.T) {
	taken := map[string]bool{"a.txt": true, "a 2.txt": true, ".env": true, "dir": true}
	for name, want := range map[string]string{"a.txt": "a 3.txt", ".env": ".env 2", "dir": "dir 2", "b": "b"} {
		if got := freeName(name, func(n string) bool { return taken[n] }); got != want {
			t.Errorf("freeName(%q) = %q, want %q", name, got, want)
		}
	}
}
