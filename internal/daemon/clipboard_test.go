//go:build unix

package daemon

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
)

// A session's program copying with its node's tool copies there as the
// tool does, and on the clipboards of the front-ends attached.
func TestACopyInASessionReachesItsViewers(t *testing.T) {
	d := New(&config.Config{NodeID: "node"}, slog.New(slog.DiscardHandler))
	d.hookBinary = "/bin/makod"
	// A socket's path is short: macOS takes 104 bytes.
	run, err := os.MkdirTemp("/tmp", "mako-clip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	d.listenHooks(t.Context(), run)
	if d.clipTools == "" {
		t.Fatal("no clipboard tools")
	}
	addSessionOf(t, d, "copier", "claude")
	s, _ := d.sessions.get("copier")
	sub := s.Subscribe()
	defer sub.Close()

	// The node's own pbcopy, which keeps what it is given.
	native := t.TempDir()
	kept := filepath.Join(native, "kept")
	if err := os.WriteFile(filepath.Join(native, "pbcopy"), []byte("#!/bin/sh\ncat > '"+kept+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join([]string{d.clipTools, native, "/bin", "/usr/bin"}, string(os.PathListSeparator)))
	t.Setenv("MAKO_SESSION", "copier")
	var stderr strings.Builder
	if code := runClip([]string{filepath.Join(run, "hooks.sock"), filepath.Join(d.clipTools, "pbcopy")}, strings.NewReader("hello, 세계"), os.Stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if b, _ := os.ReadFile(kept); string(b) != "hello, 세계" {
		t.Fatalf("the node's own tool got %q", b)
	}
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello, 세계")) + "\x07"
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var got strings.Builder
	for !strings.Contains(got.String(), want) {
		chunk, ok := sub.Next(ctx)
		if !ok {
			t.Fatalf("the viewer got %q", got.String())
		}
		got.Write(chunk)
	}

	// A tool the node lacks still copies for the viewers; reading it is not
	// found.
	t.Setenv("PATH", d.clipTools)
	if code := runClip([]string{filepath.Join(run, "hooks.sock"), filepath.Join(d.clipTools, "xclip"), "-selection", "clipboard"}, strings.NewReader("x"), os.Stdout, &stderr); code != 0 {
		t.Fatalf("copying without xclip: exit %d", code)
	}
	if code := runClip([]string{filepath.Join(run, "hooks.sock"), filepath.Join(d.clipTools, "xclip"), "-o"}, strings.NewReader(""), os.Stdout, &stderr); code != 127 {
		t.Fatalf("reading without xclip: exit %d", code)
	}
}

func TestWhatAClipboardToolCopies(t *testing.T) {
	for _, c := range []struct {
		tool string
		args []string
		want string // "" for no copy
	}{
		{"pbcopy", nil, "in"},
		{"xclip", []string{"-selection", "clipboard"}, "in"},
		{"xclip", []string{"-sel", "c", "-i"}, "in"},
		{"xclip", nil, ""}, // the primary selection
		{"xclip", []string{"-selection", "clipboard", "-o"}, ""},
		{"xclip", []string{"-selection", "clipboard", "-t", "image/png", "-out"}, ""},
		{"xsel", []string{"-bi"}, "in"},
		{"xsel", []string{"--clipboard", "--input"}, "in"},
		{"xsel", []string{"-bo"}, ""},
		{"xsel", []string{"-i"}, ""},
		{"wl-copy", nil, "in"},
		{"wl-copy", []string{"-t", "text/plain", "some", "words"}, "some words"},
		{"wl-copy", []string{"--clear"}, ""},
		{"wl-copy", []string{"--primary"}, ""},
	} {
		text, copying := clipInput(c.tool, c.args, strings.NewReader("in"))
		if got := map[bool]string{true: string(text)}[copying]; got != c.want {
			t.Errorf("%s %v: %q, want %q", c.tool, c.args, got, c.want)
		}
	}
}
