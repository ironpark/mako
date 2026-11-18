package plugin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// rig is a Manager over a folder of plugins a test writes, polled often,
// with limits a test can make small.
type rig struct {
	t      *testing.T
	dir    string
	data   string
	m      *Manager
	cancel context.CancelFunc
	done   chan struct{}
	logs   *logBuffer
}

type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// testLimits are short, so that a test of a limit ends soon.
var testLimits = Limits{CPU: 400 * time.Millisecond, Wall: 5 * time.Second, Memory: 16 << 20, Updates: 20 * time.Millisecond}

func newRig(t *testing.T, lim Limits, netTest map[string]string, sessions ...*protocol.SessionInfo) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{t: t, dir: filepath.Join(base, "plugins"), data: filepath.Join(base, "plugin-data"), done: make(chan struct{}), logs: &logBuffer{}}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.m = New(Config{
		Dir: r.dir, DataDir: r.data, Poll: 30 * time.Millisecond, Limits: lim, NetTest: netTest,
		Log:      slog.New(slog.NewTextHandler(io.MultiWriter(r.logs), &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sessions: func() []*protocol.SessionInfo { return sessions },
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { defer close(r.done); r.m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-r.done
		if t.Failed() {
			t.Logf("log:\n%s", r.logs.String())
		}
	})
	return r
}

// write puts a plugin's files in its folder.
func (r *rig) write(id string, files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.dir, id, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

// manifest is a manifest of the ID with permissions and one block, ui,
// and one command, go, and the rest of extra.
func manifest(id string, perms []string, extra string) string {
	q := make([]string, len(perms))
	for i, p := range perms {
		q[i] = `"` + p + `"`
	}
	return `id = "` + id + `"
version = "0.1.0"
publisher = "test"
api = 1
entry = "main.js"
permissions = [` + strings.Join(q, ", ") + `]

[[contributes.block]]
id = "ui"
title = "Test"

[[contributes.command]]
id = "go"
title = "Go"
` + extra
}

// info waits for the plugin to stand as cond says, and returns it.
func (r *rig) info(id string, cond func(*protocol.PluginInfo) bool) *protocol.PluginInfo {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *protocol.PluginInfo
	for time.Now().Before(deadline) {
		for _, p := range r.m.List() {
			if p.GetId() == id {
				last = p
				if cond(p) {
					return p
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("plugin %s never stood as wanted; last %v", id, last)
	return nil
}

func (r *rig) state(id, state string) *protocol.PluginInfo {
	r.t.Helper()
	return r.info(id, func(p *protocol.PluginInfo) bool { return p.GetState() == state })
}

func (r *rig) act(a *protocol.PluginAction) (*protocol.PluginDone, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.m.Act(ctx, a)
}

func (r *rig) grant(id string) {
	r.t.Helper()
	if _, err := r.act(&protocol.PluginAction{PluginId: id, Op: protocol.PluginGrant}); err != nil {
		r.t.Fatal(err)
	}
}

// watcher follows a block as a front-end would, applying each update.
type watcher struct {
	t       *testing.T
	mu      sync.Mutex
	tree    *protocol.UiNode
	last    *protocol.BlockUpdate
	updates int
	err     error
	cancel  context.CancelFunc
	changed chan struct{}
}

func (r *rig) watch(block string) *watcher {
	r.t.Helper()
	// A front-end watches a block its listing has.
	id, _, _ := splitBlock(block)
	r.info(id, func(*protocol.PluginInfo) bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	w := &watcher{t: r.t, cancel: cancel, changed: make(chan struct{}, 1)}
	go func() {
		err := r.m.Watch(ctx, block, func(u *protocol.BlockUpdate) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.updates++
			w.last = u
			switch {
			case u.GetTree() != nil:
				w.tree = u.GetTree()
			case len(u.GetOps()) > 0:
				next, err := protocol.ApplyUi(w.tree, u.GetOps())
				if err != nil {
					w.err = err
					return err
				}
				w.tree = next
			}
			select {
			case w.changed <- struct{}{}:
			default:
			}
			return nil
		})
		w.mu.Lock()
		if w.err == nil && err != nil && ctx.Err() == nil {
			w.err = err
		}
		w.mu.Unlock()
	}()
	r.t.Cleanup(cancel)
	return w
}

// until waits for the block to stand as cond says.
func (w *watcher) until(what string, cond func(tree *protocol.UiNode, u *protocol.BlockUpdate) bool) *protocol.UiNode {
	w.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		w.mu.Lock()
		tree, last, err := w.tree, w.last, w.err
		w.mu.Unlock()
		if err != nil {
			w.t.Fatalf("watching: %v", err)
		}
		if last != nil && cond(tree, last) {
			return tree
		}
		select {
		case <-w.changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			w.t.Fatalf("the block never showed %s; last update %v, tree %v", what, last, tree)
		}
	}
}

// texts are the text of a tree's nodes, in order.
func texts(n *protocol.UiNode) []string {
	if n == nil {
		return nil
	}
	var out []string
	if n.GetText() != "" {
		out = append(out, n.GetText())
	}
	for _, c := range n.GetChildren() {
		out = append(out, texts(c)...)
	}
	return out
}

func hasText(n *protocol.UiNode, s string) bool {
	for _, t := range texts(n) {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// find is the first node whose text has s.
func find(n *protocol.UiNode, s string) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if strings.Contains(n.GetText(), s) {
		return n
	}
	for _, c := range n.GetChildren() {
		if f := find(c, s); f != nil {
			return f
		}
	}
	return nil
}

func (r *rig) event(block, node, kind, value string) (*protocol.PluginDone, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.m.Event(ctx, &protocol.BlockEvent{Block: block, Node: node, Kind: kind, Value: value})
}
