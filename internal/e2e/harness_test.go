//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ggtest"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/daemon"
	"github.com/ironpark/mako/internal/gui"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/uiblock"
)

const (
	defaultPollInterval = 20 * time.Millisecond
	defaultWaitTimeout  = 15 * time.Second
)

func TestMain(m *testing.M) {
	ggtest.Main(m)
}

// SentMessage records a call to StubAgentHost.Send.
type SentMessage struct {
	Plugin  string
	Session string
	Text    string
	ID      uint64
}

// StartedAgent records a call to StubAgentHost.Start.
type StartedAgent struct {
	Plugin  string
	Session string
	Req     plugin.AgentStart
}

// StoppedAgent records a call to StubAgentHost.Stop.
type StoppedAgent struct {
	Plugin  string
	Session string
}

// StubAgentHost implements plugin.AgentHost in memory and records all
// Send, Start, and Stop calls for opaque-box E2E assertions.
type StubAgentHost struct {
	mu      sync.Mutex
	sent    []SentMessage
	started []StartedAgent
	stopped []StoppedAgent
	running map[string]map[string]bool

	SendErr  func(pluginID, session, text string) error
	StartErr func(pluginID string, req plugin.AgentStart) error
	StopErr  func(pluginID, session string) error
}

var _ plugin.AgentHost = (*StubAgentHost)(nil)

// Send implements plugin.AgentHost.
func (s *StubAgentHost) Send(pluginID, session, text string) (uint64, error) {
	if s.SendErr != nil {
		if err := s.SendErr(pluginID, session, text); err != nil {
			return 0, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uint64(len(s.sent) + 1)
	s.sent = append(s.sent, SentMessage{
		Plugin:  pluginID,
		Session: session,
		Text:    text,
		ID:      id,
	})
	return id, nil
}

// Start implements plugin.AgentHost.
func (s *StubAgentHost) Start(_ context.Context, pluginID string, req plugin.AgentStart) (string, error) {
	if s.StartErr != nil {
		if err := s.StartErr(pluginID, req); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == nil {
		s.running = make(map[string]map[string]bool)
	}
	if s.running[pluginID] == nil {
		s.running[pluginID] = make(map[string]bool)
	}
	sid := fmt.Sprintf("e2e-agent-%d", len(s.started)+1)
	s.running[pluginID][sid] = true
	s.started = append(s.started, StartedAgent{
		Plugin:  pluginID,
		Session: sid,
		Req:     req,
	})
	return sid, nil
}

// Stop implements plugin.AgentHost.
func (s *StubAgentHost) Stop(pluginID, session string) error {
	if s.StopErr != nil {
		if err := s.StopErr(pluginID, session); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running[pluginID][session] {
		return fmt.Errorf("session %q was not started by plugin %q", session, pluginID)
	}
	delete(s.running[pluginID], session)
	s.stopped = append(s.stopped, StoppedAgent{
		Plugin:  pluginID,
		Session: session,
	})
	return nil
}

// Running implements plugin.AgentHost.
func (s *StubAgentHost) Running(pluginID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running[pluginID])
}

// Sent returns a snapshot of all recorded Send calls.
func (s *StubAgentHost) Sent() []SentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// Started returns a snapshot of all recorded Start calls.
func (s *StubAgentHost) Started() []StartedAgent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.started)
}

// Stopped returns a snapshot of all recorded Stop calls.
func (s *StubAgentHost) Stopped() []StoppedAgent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.stopped)
}

// PluginRigOpts configures a PluginRig.
type PluginRigOpts struct {
	Sessions     []*protocol.SessionInfo
	SessionsFn   func() []*protocol.SessionInfo
	Projects     []plugin.ProjectRoots
	ProjectsFn   func() []plugin.ProjectRoots
	Agents       plugin.AgentHost
	ResolveScope func(scopeKey string) plugin.ScopeContext
	Git          plugin.GitHost
	Forge        plugin.ForgeHost
	Bundled      fs.FS
	NodeID       string
	Poll         time.Duration
	Limits       plugin.Limits
	NetTest      map[string]string
}

// PluginRig wraps an isolated plugin.Manager running over temporary plugin
// and data directories with configurable host callbacks.
type PluginRig struct {
	t       *testing.T
	Mgr     *plugin.Manager
	Dir     string
	DataDir string
	Agents  *StubAgentHost

	mu       sync.RWMutex
	sessions []*protocol.SessionInfo
	projects []plugin.ProjectRoots
	logs     *lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// NewPluginRig creates and starts an isolated plugin.Manager backed by temp dirs.
func NewPluginRig(t *testing.T, opts PluginRigOpts) *PluginRig {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "plugins")
	dataDir := filepath.Join(base, "plugin-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plugins dir: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin-data dir: %v", err)
	}

	r := &PluginRig{
		t:        t,
		Dir:      dir,
		DataDir:  dataDir,
		sessions: slices.Clone(opts.Sessions),
		projects: slices.Clone(opts.Projects),
		logs:     &lockedBuffer{},
	}

	agents := opts.Agents
	if agents == nil {
		r.Agents = &StubAgentHost{}
		agents = r.Agents
	}

	sessionsFn := opts.SessionsFn
	if sessionsFn == nil {
		sessionsFn = func() []*protocol.SessionInfo {
			r.mu.RLock()
			defer r.mu.RUnlock()
			out := make([]*protocol.SessionInfo, len(r.sessions))
			for i, s := range r.sessions {
				out[i] = proto.CloneOf(s)
			}
			return out
		}
	}

	projectsFn := opts.ProjectsFn
	if projectsFn == nil {
		projectsFn = func() []plugin.ProjectRoots {
			r.mu.RLock()
			defer r.mu.RUnlock()
			return slices.Clone(r.projects)
		}
	}

	poll := opts.Poll
	if poll <= 0 {
		poll = 30 * time.Millisecond
	}

	lim := opts.Limits
	if lim == (plugin.Limits{}) {
		lim = plugin.Limits{
			CPU:     10 * time.Second,
			Wall:    20 * time.Second,
			Memory:  16 << 20,
			Updates: 20 * time.Millisecond,
		}
	}

	nodeID := opts.NodeID
	if nodeID == "" {
		nodeID = "e2e-node"
	}

	r.Mgr = plugin.New(plugin.Config{
		Dir:          dir,
		DataDir:      dataDir,
		Log:          slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sessions:     sessionsFn,
		Projects:     projectsFn,
		ResolveScope: opts.ResolveScope,
		Agents:       agents,
		Git:          opts.Git,
		Forge:        opts.Forge,
		Bundled:      opts.Bundled,
		NodeID:       nodeID,
		Poll:         poll,
		Limits:       lim,
		NetTest:      opts.NetTest,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Mgr.Run(ctx)
	}()
	if len(opts.Sessions) > 0 {
		r.Mgr.SessionsChanged(r.sessions)
	}
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() && r.logs.String() != "" {
			t.Logf("plugin manager log:\n%s", r.logs.String())
		}
	})
	return r
}

// Logs returns the captured plugin manager log output.
func (r *PluginRig) Logs() string {
	return r.logs.String()
}

// SetSessions replaces the in-memory session list and notifies subscribed plugins.
func (r *PluginRig) SetSessions(list []*protocol.SessionInfo) {
	r.mu.Lock()
	r.sessions = make([]*protocol.SessionInfo, len(list))
	for i, s := range list {
		r.sessions[i] = proto.CloneOf(s)
	}
	r.mu.Unlock()
	r.Mgr.SessionsChanged(list)
}

// SetProjects replaces the in-memory project roots list.
func (r *PluginRig) SetProjects(projects []plugin.ProjectRoots) {
	r.mu.Lock()
	r.projects = slices.Clone(projects)
	r.mu.Unlock()
}

// InstallJS writes plugin.toml and main.js for id and waits until listed.
func (r *PluginRig) InstallJS(t *testing.T, id, toml, js string) {
	t.Helper()
	r.InstallFiles(t, id, map[string]string{
		plugin.ManifestName: toml,
		"main.js":           js,
	})
}

// InstallFiles writes arbitrary files for plugin id and waits until listed.
func (r *PluginRig) InstallFiles(t *testing.T, id string, files map[string]string) {
	t.Helper()
	writePluginFiles(t, r.Dir, id, files)
	r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
		return p.GetState() != ""
	})
}

// CopyPluginDir copies a plugin folder from srcDir under id and waits until listed.
func (r *PluginRig) CopyPluginDir(t *testing.T, srcDir, id string) {
	t.Helper()
	copyPluginTree(t, r.Dir, srcDir, id)
	r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
		return p.GetState() != ""
	})
}

// WaitPlugin polls Manager.List until plugin id satisfies pred.
func (r *PluginRig) WaitPlugin(t *testing.T, id string, pred func(*protocol.PluginInfo) bool) *protocol.PluginInfo {
	t.Helper()
	var matched *protocol.PluginInfo
	var last *protocol.PluginInfo
	deadline := time.Now().Add(defaultWaitTimeout)
	for time.Now().Before(deadline) {
		for _, p := range r.Mgr.List() {
			if p.GetId() == id {
				last = p
				if pred == nil || pred(p) {
					matched = p
					return matched
				}
			}
		}
		time.Sleep(defaultPollInterval)
	}
	t.Fatalf("plugin %q did not satisfy condition within %v (last=%+v)\nlogs:\n%s", id, defaultWaitTimeout, last, r.logs.String())
	return nil
}

// Grant grants all manifest permissions for plugin id.
func (r *PluginRig) Grant(t *testing.T, id string) {
	t.Helper()
	info := r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
		return p.GetState() != ""
	})
	if info.GetState() == protocol.PluginFailed {
		t.Fatalf("cannot grant failed plugin %q: %s", id, info.GetError())
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	if _, err := r.Mgr.Act(ctx, &protocol.PluginAction{
		PluginId: id,
		Op:       protocol.PluginGrant,
	}); err != nil {
		t.Fatalf("grant plugin %q: %v", id, err)
	}
}

// Act runs a PluginAction on the manager.
func (r *PluginRig) Act(t *testing.T, a *protocol.PluginAction) (*protocol.PluginDone, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	return r.Mgr.Act(ctx, a)
}

// WatchTree subscribes to a block in scope with view (default locale "en")
// and returns a TreeWatcher that tracks reconstructed UiNode trees.
func (r *PluginRig) WatchTree(t *testing.T, block, scope string, view plugin.View) *TreeWatcher {
	t.Helper()
	return r.WatchTreeLocale(t, block, scope, "en", view)
}

// WatchTreeLocale subscribes to a block with an explicit BCP-47 locale.
func (r *PluginRig) WatchTreeLocale(t *testing.T, block, scope, locale string, view plugin.View) *TreeWatcher {
	t.Helper()
	if id, ok := pluginIDFromBlock(block); ok {
		r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
			return p.GetState() != ""
		})
	}
	w := newTreeWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		err := r.Mgr.WatchScopedView(ctx, block, scope, locale, view, w.handle)
		if err != nil && ctx.Err() == nil {
			w.setStreamErr(err)
		}
	}()
	return w
}

// SendEvent dispatches a BlockEvent to the plugin manager.
func (r *PluginRig) SendEvent(t *testing.T, ev *protocol.BlockEvent) (*protocol.PluginDone, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	return r.Mgr.Event(ctx, ev)
}

// TreeWatcher applies incoming full trees and incremental UiOp diffs into a
// thread-safe current UiNode snapshot.
type TreeWatcher struct {
	t       *testing.T
	mu      sync.Mutex
	tree    *protocol.UiNode
	last    *protocol.BlockUpdate
	errText string
	stream  error
	updates int
	changed chan struct{}
}

func newTreeWatcher(t *testing.T) *TreeWatcher {
	return &TreeWatcher{
		t:       t,
		changed: make(chan struct{}, 1),
	}
}

func (w *TreeWatcher) handle(u *protocol.BlockUpdate) error {
	w.mu.Lock()
	w.updates++
	w.last = u
	if u.GetError() != "" {
		w.errText = u.GetError()
	} else {
		w.errText = ""
	}
	switch {
	case u.GetTree() != nil:
		w.tree = proto.CloneOf(u.GetTree())
	case len(u.GetOps()) > 0:
		next, err := protocol.ApplyUi(w.tree, u.GetOps())
		if err != nil {
			w.stream = fmt.Errorf("apply ui ops: %w", err)
			w.mu.Unlock()
			w.signal()
			return w.stream
		}
		w.tree = next
	}
	w.mu.Unlock()
	w.signal()
	return nil
}

func (w *TreeWatcher) setStreamErr(err error) {
	w.mu.Lock()
	if w.stream == nil {
		w.stream = err
	}
	w.mu.Unlock()
	w.signal()
}

func (w *TreeWatcher) signal() {
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

// Latest returns a clone of the most recent UiNode tree (nil if none yet).
func (w *TreeWatcher) Latest(t *testing.T) *protocol.UiNode {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	return proto.CloneOf(w.tree)
}

// LastUpdate returns the most recent BlockUpdate received.
func (w *TreeWatcher) LastUpdate(t *testing.T) *protocol.BlockUpdate {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	return proto.CloneOf(w.last)
}

// Err returns the latest block or stream error, if any.
func (w *TreeWatcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stream != nil {
		return w.stream
	}
	if w.errText != "" {
		return errors.New(w.errText)
	}
	return nil
}

// WaitFor waits up to timeout until tree != nil and pred(tree) returns true.
// It fails the test if a block/stream error occurs or timeout expires.
func (w *TreeWatcher) WaitFor(t *testing.T, timeout time.Duration, pred func(*protocol.UiNode) bool) *protocol.UiNode {
	t.Helper()
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		w.mu.Lock()
		tree := proto.CloneOf(w.tree)
		errText := w.errText
		streamErr := w.stream
		w.mu.Unlock()

		if streamErr != nil {
			t.Fatalf("watch stream failed: %v", streamErr)
		}
		if errText != "" {
			t.Fatalf("block error: %s", errText)
		}
		if tree != nil && (pred == nil || pred(tree)) {
			return tree
		}
		select {
		case <-w.changed:
		case <-deadline.C:
			t.Fatalf("timed out after %v waiting for tree condition (last texts=%v)", timeout, TreeTexts(tree))
		}
	}
}

// WaitUpdate waits up to timeout until an update arrives and pred(tree, update)
// returns true. Unlike WaitFor, it does not fail on u.GetError(), allowing tests
// to assert error states on BlockUpdate.
func (w *TreeWatcher) WaitUpdate(t *testing.T, timeout time.Duration, pred func(*protocol.UiNode, *protocol.BlockUpdate) bool) (*protocol.UiNode, *protocol.BlockUpdate) {
	t.Helper()
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		w.mu.Lock()
		tree := proto.CloneOf(w.tree)
		last := proto.CloneOf(w.last)
		streamErr := w.stream
		w.mu.Unlock()

		if streamErr != nil {
			t.Fatalf("watch stream failed: %v", streamErr)
		}
		if last != nil && (pred == nil || pred(tree, last)) {
			return tree, last
		}
		select {
		case <-w.changed:
		case <-deadline.C:
			t.Fatalf("timed out after %v waiting for block update (last=%+v)", timeout, last)
		}
	}
}

// DaemonRigOpts configures an isolated DaemonRig.
type DaemonRigOpts struct {
	Name      string
	EnableP2P bool
	Peers     []string
	Configure func(*config.Config)
}

// DaemonRig runs a real isolated daemon.Daemon over a temp MAKO_HOME and HOME
// with a connected client.Client.
type DaemonRig struct {
	t        *testing.T
	Daemon   *daemon.Daemon
	Client   *client.Client
	Home     string
	UserHome string
	logs     *lockedBuffer
}

// NewDaemonRig starts an isolated daemon and connects a client.
// MAKO_HOME and HOME are isolated via t.Setenv, and SSH_AUTH_SOCK is unset.
func NewDaemonRig(t *testing.T, opts DaemonRigOpts) *DaemonRig {
	t.Helper()
	home := t.TempDir()
	userHome := t.TempDir()
	t.Setenv("MAKO_HOME", home)
	t.Setenv("HOME", userHome)
	t.Setenv("SSH_AUTH_SOCK", "")
	_ = os.Unsetenv("SSH_AUTH_SOCK")

	cfg := config.Default()
	if opts.Name != "" {
		cfg.Name = opts.Name
	} else {
		cfg.Name = "e2e-daemon"
	}
	cfg.Discovery = config.Discovery{}
	cfg.Peers = slices.Clone(opts.Peers)
	if opts.EnableP2P {
		cfg.P2P.Disabled = false
		cfg.P2P.Listen = "127.0.0.1:0"
	} else {
		cfg.P2P.Disabled = true
	}
	if opts.Configure != nil {
		opts.Configure(cfg)
	}

	logs := &lockedBuffer{}
	d := daemon.New(cfg, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx)
	}()

	select {
	case <-d.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("daemon exited before ready: %v\nlogs:\n%s", err, logs.String())
	case <-time.After(defaultWaitTimeout):
		cancel()
		<-done
		t.Fatalf("daemon did not become ready within %v\nlogs:\n%s", defaultWaitTimeout, logs.String())
	}

	sockDir := filepath.Dir(d.Socket())
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("daemon exit error: %v", err)
		}
		// Clean up hashed run directory if RunDir placed it outside home.
		if sockDir != "" && !strings.HasPrefix(sockDir, home+string(filepath.Separator)) && sockDir != home {
			_ = os.RemoveAll(sockDir)
		}
		if t.Failed() && logs.String() != "" {
			t.Logf("daemon log:\n%s", logs.String())
		}
	})

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	c, err := client.Dial(dialCtx, d.Socket())
	if err != nil {
		t.Fatalf("client.Dial(%q): %v", d.Socket(), err)
	}
	t.Cleanup(func() { _ = c.Close() })

	return &DaemonRig{
		t:        t,
		Daemon:   d,
		Client:   c,
		Home:     home,
		UserHome: userHome,
		logs:     logs,
	}
}

// Logs returns the captured daemon log output.
func (r *DaemonRig) Logs() string {
	return r.logs.String()
}

// PluginsDir returns the daemon's plugins directory (<Home>/plugins).
func (r *DaemonRig) PluginsDir() string {
	return filepath.Join(r.Home, "plugins")
}

// InstallPlugin writes plugin.toml and main.js into <Home>/plugins/<id> and
// waits until the daemon lists the plugin.
func (r *DaemonRig) InstallPlugin(t *testing.T, id, toml, js string) {
	t.Helper()
	r.InstallFiles(t, id, map[string]string{
		plugin.ManifestName: toml,
		"main.js":           js,
	})
}

// InstallFiles writes arbitrary files for plugin id into <Home>/plugins/<id>
// and waits until the daemon lists the plugin.
func (r *DaemonRig) InstallFiles(t *testing.T, id string, files map[string]string) {
	t.Helper()
	writePluginFiles(t, r.PluginsDir(), id, files)
	r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
		return p.GetState() != ""
	})
}

// CopyPluginDir copies srcDir into <Home>/plugins/<id> and waits until listed.
func (r *DaemonRig) CopyPluginDir(t *testing.T, srcDir, id string) {
	t.Helper()
	copyPluginTree(t, r.PluginsDir(), srcDir, id)
	r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
		return p.GetState() != ""
	})
}

// WaitPlugin polls Client.Plugins until plugin id satisfies pred.
func (r *DaemonRig) WaitPlugin(t *testing.T, id string, pred func(*protocol.PluginInfo) bool) *protocol.PluginInfo {
	t.Helper()
	deadline := time.Now().Add(defaultWaitTimeout)
	var last *protocol.PluginInfo
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		list, err := r.Client.Plugins(ctx)
		cancel()
		if err == nil && list != nil {
			for _, p := range list.GetPlugins() {
				if p.GetId() == id {
					last = p
					if pred == nil || pred(p) {
						return p
					}
				}
			}
		}
		time.Sleep(defaultPollInterval)
	}
	t.Fatalf("daemon plugin %q did not satisfy condition within %v (last=%+v)\nlogs:\n%s", id, defaultWaitTimeout, last, r.logs.String())
	return nil
}

// Grant grants all manifest permissions for plugin id over the client socket.
func (r *DaemonRig) Grant(t *testing.T, id string) {
	t.Helper()
	info := r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
		return p.GetState() != ""
	})
	if info.GetState() == protocol.PluginFailed {
		t.Fatalf("cannot grant failed plugin %q: %s", id, info.GetError())
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	if _, err := r.Client.PluginAction(ctx, &protocol.PluginAction{
		PluginId: id,
		Op:       protocol.PluginGrant,
	}); err != nil {
		t.Fatalf("grant plugin %q: %v", id, err)
	}
}

// WatchBlockView watches a block over the daemon socket and decodes updates
// into a TreeWatcher.
func (r *DaemonRig) WatchBlockView(t *testing.T, block, scope, locale string, standalone bool) *TreeWatcher {
	t.Helper()
	return r.WatchBlockRequest(t, &protocol.WatchBlock{
		Block:      block,
		Scope:      scope,
		Locale:     locale,
		Standalone: standalone,
	})
}

// WatchBlockRequest watches a block with an explicit WatchBlock request.
func (r *DaemonRig) WatchBlockRequest(t *testing.T, req *protocol.WatchBlock) *TreeWatcher {
	t.Helper()
	if id, ok := pluginIDFromBlock(req.GetBlock()); ok {
		r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
			return p.GetState() != ""
		})
	}
	w := newTreeWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		err := r.Client.WatchBlockRequest(ctx, req, func(u *protocol.BlockUpdate) {
			_ = w.handle(u)
		})
		if err != nil && ctx.Err() == nil {
			w.setStreamErr(err)
		}
	}()
	return w
}

// BlockEvent sends a BlockEvent through the daemon client.
func (r *DaemonRig) BlockEvent(t *testing.T, ev *protocol.BlockEvent) (*protocol.PluginDone, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	return r.Client.BlockEvent(ctx, ev)
}

// OpenShellSession opens a real shell session in project and registers cleanup
// to kill it when the test ends.
func (r *DaemonRig) OpenShellSession(t *testing.T, project string) *protocol.SessionInfo {
	t.Helper()
	if project == "" {
		project = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	info, err := r.Client.Open(ctx, &protocol.OpenSession{
		Agent:   "shell",
		Project: project,
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("open shell session in %q: %v", project, err)
	}
	t.Cleanup(func() {
		kctx, kcancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer kcancel()
		_ = r.Client.Kill(kctx, &protocol.CloseSession{SessionId: info.GetId()})
	})
	return info
}

// ListSessions lists sessions visible to the daemon client.
func (r *DaemonRig) ListSessions(t *testing.T) []*protocol.SessionInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	sessions, err := r.Client.ListSessions(ctx, &protocol.ListSessions{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	return sessions
}

// Nodes lists mesh nodes visible to the daemon client.
func (r *DaemonRig) Nodes(t *testing.T) []*protocol.Node {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	nodes, err := r.Client.Nodes(ctx)
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	return nodes
}

// PeerAddr returns a dialable libp2p multiaddr ending in /p2p/<peerID> for
// connecting peer daemons when EnableP2P is true.
func (r *DaemonRig) PeerAddr() string {
	self := r.Daemon.Self()
	addrs := self.GetMultiaddrs()
	pid := self.GetPeerId()
	if len(addrs) == 0 || pid == "" {
		return ""
	}
	if strings.Contains(addrs[0], "/p2p/") {
		return addrs[0]
	}
	return addrs[0] + "/p2p/" + pid
}

// LetIn authorizes peer's public key on r over the client Access RPC.
func (r *DaemonRig) LetIn(t *testing.T, peer *DaemonRig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultWaitTimeout)
	defer cancel()
	info, err := peer.Client.Access(ctx, &protocol.Access{})
	if err != nil {
		t.Fatalf("read peer access key: %v", err)
	}
	keyLine := info.GetKey().GetLine()
	if keyLine == "" {
		t.Fatalf("peer %q returned empty access key", peer.Daemon.Self().GetId())
	}
	if _, err := r.Client.Access(ctx, &protocol.Access{
		Op:  "add",
		Key: keyLine,
	}); err != nil {
		t.Fatalf("add peer key: %v", err)
	}
}

// MustReadUI parses jsonBody via uiblock.Read and fails t on error.
func MustReadUI(t *testing.T, jsonBody string) *uiblock.Tree {
	t.Helper()
	tr, err := uiblock.Read([]byte(jsonBody))
	if err != nil {
		t.Fatalf("uiblock.Read failed unexpectedly: %v\njson=%s", err, jsonBody)
	}
	return tr
}

// MustRejectUI asserts that uiblock.Read rejects jsonBody and returns the error.
func MustRejectUI(t *testing.T, jsonBody string) error {
	t.Helper()
	tr, err := uiblock.Read([]byte(jsonBody))
	if err == nil {
		t.Fatalf("uiblock.Read unexpectedly succeeded (root=%+v)\njson=%s", tr.Root, jsonBody)
	}
	return err
}

// WaitFor polls pred every 20ms up to timeout and fails t if it never returns true.
func WaitFor(t *testing.T, timeout time.Duration, desc string, pred func() bool) {
	t.Helper()
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(defaultPollInterval)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, desc)
}

// OpenAppProbeBlock installs a sidebar JS plugin on an isolated DaemonRig,
// opens a headless gui.AppProbe connected to it, navigates to its sidebar page,
// and settles the initial crossfade so subsequent BlockUpdates update the same
// live treeView / blockForm / graphViewState.
func OpenAppProbeBlock(t *testing.T, id, title, js string, sz ggui.Size) (*DaemonRig, *gui.AppProbe, *TreeWatcher, string) {
	t.Helper()
	rig := NewDaemonRig(t, DaemonRigOpts{})
	toml := fmt.Sprintf(`id = %q
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = ["storage"]

[[contributes.block]]
id = "main"
title = %q
slots = ["sidebar"]
`, id, title)
	rig.InstallPlugin(t, id, toml, js)
	rig.Grant(t, id)
	rig.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
		st := p.GetState()
		return st == protocol.PluginRunning || st == protocol.PluginReady
	})

	blockName := "plugin:" + id + "/main"
	w := rig.WatchBlockView(t, blockName, "", "en", true)
	w.WaitFor(t, 10*time.Second, func(n *protocol.UiNode) bool {
		return n != nil
	})

	cfg := config.Default()
	cfg.Discovery = config.Discovery{}
	cfg.P2P.Disabled = true
	ap := gui.NewAppProbe(cfg, rig.Daemon.Socket(), sz)
	t.Cleanup(ap.Close)
	i18n.Set("en")
	ap.Probe().Frame()

	WaitFor(t, 10*time.Second, "sidebar entry "+title, func() bool {
		i18n.Set("en")
		ap.Probe().Frame()
		return strings.Contains(ap.Probe().Semantics().String(), title)
	})
	i18n.Set("en")
	ap.OpenSidebarPage(blockName)
	for range 3 {
		ap.Probe().Advance(70 * time.Millisecond)
	}
	return rig, ap, w, blockName
}

// WaitProbeSemantics pumps ap.Probe().Frame() until pred(ap.Probe().Semantics())
// returns true or timeout expires.
func WaitProbeSemantics(t *testing.T, ap *gui.AppProbe, timeout time.Duration, desc string, pred func(*ggui.SemTree) bool) {
	t.Helper()
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	WaitFor(t, timeout, desc, func() bool {
		i18n.Set("en")
		ap.Probe().Frame()
		return pred(ap.Probe().Semantics())
	})
}

// CropRGBA extracts pixel bytes within r from img for region-scoped frame diffs.
func CropRGBA(img *image.RGBA, r ggui.Rect) []byte {
	b := img.Bounds()
	x0 := max(int(r.Origin.X), b.Min.X)
	y0 := max(int(r.Origin.Y), b.Min.Y)
	x1 := min(int(r.Origin.X+r.Size.W), b.Max.X)
	y1 := min(int(r.Origin.Y+r.Size.H), b.Max.Y)
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	rowBytes := (x1 - x0) * 4
	out := make([]byte, 0, rowBytes*(y1-y0))
	for y := y0; y < y1; y++ {
		off := img.PixOffset(x0, y)
		out = append(out, img.Pix[off:off+rowBytes]...)
	}
	return out
}

// FindNodeByKind returns the first node in n whose Kind equals kind.
func FindNodeByKind(n *protocol.UiNode, kind string) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if n.GetKind() == kind {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := FindNodeByKind(c, kind); got != nil {
			return got
		}
	}
	return nil
}

// FindAllNodesByKind returns all nodes in n whose Kind equals kind.
func FindAllNodesByKind(n *protocol.UiNode, kind string) []*protocol.UiNode {
	if n == nil {
		return nil
	}
	var out []*protocol.UiNode
	if n.GetKind() == kind {
		out = append(out, n)
	}
	for _, c := range n.GetChildren() {
		out = append(out, FindAllNodesByKind(c, kind)...)
	}
	return out
}

// FindNodeByKey returns the first node whose ID ends with "/"+key.
func FindNodeByKey(n *protocol.UiNode, key string) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if n.GetId() == "/"+key || strings.HasSuffix(n.GetId(), "/"+key) {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := FindNodeByKey(c, key); got != nil {
			return got
		}
	}
	return nil
}

// FindNodeByID returns the first node whose ID equals id.
func FindNodeByID(n *protocol.UiNode, id string) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if n.GetId() == id {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := FindNodeByID(c, id); got != nil {
			return got
		}
	}
	return nil
}

// FindNodeByText returns the first node whose Text contains substr.
func FindNodeByText(n *protocol.UiNode, substr string) *protocol.UiNode {
	if n == nil {
		return nil
	}
	if strings.Contains(n.GetText(), substr) {
		return n
	}
	for _, c := range n.GetChildren() {
		if got := FindNodeByText(c, substr); got != nil {
			return got
		}
	}
	return nil
}

// TreeTexts returns all non-empty Text fields in n in pre-order.
func TreeTexts(n *protocol.UiNode) []string {
	if n == nil {
		return nil
	}
	var out []string
	if n.GetText() != "" {
		out = append(out, n.GetText())
	}
	for _, c := range n.GetChildren() {
		out = append(out, TreeTexts(c)...)
	}
	return out
}

// HasTreeText reports whether any node in n has Text containing substr.
func HasTreeText(n *protocol.UiNode, substr string) bool {
	for _, txt := range TreeTexts(n) {
		if strings.Contains(txt, substr) {
			return true
		}
	}
	return false
}

// RepoRoot walks up from the working directory to locate the repository root
// containing go.mod.
func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod above %q", dir)
		}
		dir = parent
	}
}

// DemoPluginDir returns <repo>/examples/plugins/<id>.
func DemoPluginDir(t *testing.T, id string) string {
	t.Helper()
	return filepath.Join(RepoRoot(t), "examples", "plugins", id)
}

// AppSupportPluginsDir returns the user's deployed plugins directory
// (e.g. ~/Library/Application Support/mako/plugins on macOS).
func AppSupportPluginsDir() (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfgDir, "mako", "plugins"), nil
}

func pluginIDFromBlock(block string) (string, bool) {
	rest, ok := strings.CutPrefix(block, "plugin:")
	if !ok {
		return "", false
	}
	id, _, ok := strings.Cut(rest, "/")
	return id, ok && id != ""
}

func writePluginFiles(t *testing.T, pluginsDir, id string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", pluginsDir, err)
	}
	dest := filepath.Join(pluginsDir, id)
	if _, err := os.Lstat(dest); os.IsNotExist(err) {
		stage, err := os.MkdirTemp(filepath.Dir(pluginsDir), ".stage-"+id+"-")
		if err != nil {
			t.Fatalf("mkdtemp stage: %v", err)
		}
		writeAtomicFiles(t, stage, files)
		if err := os.Rename(stage, dest); err != nil {
			_ = os.RemoveAll(stage)
			t.Fatalf("rename stage to %s: %v", dest, err)
		}
		return
	}
	writeAtomicFiles(t, dest, files)
}

func writeAtomicFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(target), err)
		}
		tmp := filepath.Join(filepath.Dir(target), ".saving-"+filepath.Base(target))
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", tmp, err)
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			t.Fatalf("rename %s -> %s: %v", tmp, target, err)
		}
	}
}

func copyPluginTree(t *testing.T, pluginsDir, srcDir, id string) {
	t.Helper()
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", pluginsDir, err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(pluginsDir), ".stage-"+id+"-")
	if err != nil {
		t.Fatalf("mkdtemp stage: %v", err)
	}
	cleanStage := true
	defer func() {
		if cleanStage {
			_ = os.RemoveAll(stage)
		}
	}()

	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip symlinks because plugin.Manager rejects them via openPluginRoot.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		target := filepath.Join(stage, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target)
	})
	if err != nil {
		t.Fatalf("copy plugin dir %q: %v", srcDir, err)
	}

	dest := filepath.Join(pluginsDir, id)
	_ = os.RemoveAll(dest)
	if err := os.Rename(stage, dest); err != nil {
		t.Fatalf("rename stage to %s: %v", dest, err)
	}
	cleanStage = false
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
