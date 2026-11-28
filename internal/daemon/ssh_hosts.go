package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/sshremote"
	"github.com/ironpark/mako/internal/transport"
)

// sshManager tracks configured SSH remote hosts, maintains their SSH
// connections (either tunneled to a remote makod or direct agentless SSH),
// exposes them as first-class mesh nodes, and handles direct-SSH operations.
type sshManager struct {
	d *Daemon

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	runDir  string
	started bool
	entries map[string]*sshEntry // keyed by nodeID ("ssh:<id>")
	order   []string
	wg      sync.WaitGroup
}

type cachedSSHHead struct {
	head     sshremote.RemoteGitHead
	at       time.Time
	fetching bool
}

type sshEntry struct {
	mu             sync.Mutex
	host           config.SSHHost
	nodeID         string
	controlPath    string
	tunnelSock     string
	activeMode     string // sshremote.FeatureSSHDaemon or sshremote.FeatureSSHDirect
	link           string // protocol.LinkUp, LinkLinking, LinkDown
	linkErr        string
	relinkAt       time.Time
	fails          int
	lastSeen       time.Time
	probe          sshremote.ProbeResult
	tunnel         *sshremote.DaemonTunnel
	remoteNode     *protocol.Node
	remoteSessions []*protocol.SessionInfo
	heads          map[string]cachedSSHHead

	reconnectCh chan struct{}
	cancel      context.CancelFunc
}

func newSSHManager(d *Daemon) *sshManager {
	return &sshManager{
		d:       d,
		entries: map[string]*sshEntry{},
	}
}

func (m *sshManager) start(ctx context.Context, runDir string, hosts []config.SSHHost) {
	m.mu.Lock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.runDir = runDir
	m.started = true
	m.mu.Unlock()
	m.sync(hosts)
}

func (m *sshManager) close() {
	m.mu.Lock()
	cancel := m.cancel
	entries := make([]*sshEntry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	clear(m.entries)
	m.order = nil
	m.started = false
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, e := range entries {
		e.stop()
	}
	m.wg.Wait()
}

// sync reconciles running SSH host workers against the configured host list.
func (m *sshManager) sync(hosts []config.SSHHost) {
	hosts = config.NormalizeSSHHosts(hosts)
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	want := make(map[string]config.SSHHost, len(hosts))
	order := make([]string, 0, len(hosts))
	for _, h := range hosts {
		id := h.NodeID()
		want[id] = h
		order = append(order, id)
	}

	var toStop []*sshEntry
	changed := false

	// Phase 1: Detach removed or modified entries and stop them BEFORE starting
	// replacements, since replaced entries share the same controlPath/tunnelSock.
	for id, e := range m.entries {
		next, ok := want[id]
		if !ok {
			toStop = append(toStop, e)
			delete(m.entries, id)
			changed = true
			continue
		}
		e.mu.Lock()
		same := e.host.Equal(next)
		e.mu.Unlock()
		if !same {
			toStop = append(toStop, e)
			delete(m.entries, id)
			changed = true
		}
	}
	m.mu.Unlock()

	for _, e := range toStop {
		e.stop()
	}

	// Phase 2: Start new or replaced entries now that old sockets are cleaned up.
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	for _, id := range order {
		if _, ok := m.entries[id]; ok {
			continue
		}
		h := want[id]
		ectx, ecancel := context.WithCancel(m.ctx)
		e := &sshEntry{
			host:        h.Clone(),
			nodeID:      id,
			controlPath: sshremote.ControlPath(m.runDir, h.ID),
			tunnelSock:  sshremote.TunnelSocketPath(m.runDir, h.ID),
			link:        protocol.LinkDown,
			reconnectCh: make(chan struct{}, 1),
			cancel:      ecancel,
		}
		if !h.Disabled {
			e.link = protocol.LinkLinking
		}
		m.entries[id] = e
		changed = true
		m.wg.Add(1)
		go func(entry *sshEntry, ctx context.Context) {
			defer m.wg.Done()
			m.runEntry(ctx, entry)
		}(e, ectx)
	}
	if !slices.Equal(m.order, order) {
		m.order = order
		changed = true
	}
	m.mu.Unlock()

	if changed {
		m.d.changes.fire(true, true)
	}
}

func (e *sshEntry) stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.mu.Lock()
	tun := e.tunnel
	e.tunnel = nil
	host := e.host
	ctrl := e.controlPath
	e.mu.Unlock()
	if tun != nil {
		_ = tun.Close()
	}
	sshremote.CloseControlMaster(host, ctrl)
}

func (e *sshEntry) triggerReconnect() {
	select {
	case e.reconnectCh <- struct{}{}:
	default:
	}
}

// reconnect immediately retries connecting to nodeID.
func (m *sshManager) reconnect(nodeID string) error {
	m.mu.Lock()
	e, ok := m.entries[nodeID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown SSH host %q", nodeID)
	}
	e.mu.Lock()
	if e.host.Disabled {
		e.mu.Unlock()
		return fmt.Errorf("%s is disabled; enable it in Settings ▸ Nodes", e.host.DisplayName())
	}
	e.fails = 0
	e.relinkAt = time.Time{}
	e.mu.Unlock()
	e.triggerReconnect()
	m.d.changes.fire(true, false)
	return nil
}

// runEntry is the lifecycle loop for one configured SSH host.
func (m *sshManager) runEntry(ctx context.Context, e *sshEntry) {
	defer func() {
		e.mu.Lock()
		tun := e.tunnel
		e.tunnel = nil
		e.mu.Unlock()
		if tun != nil {
			_ = tun.Close()
		}
	}()
	e.mu.Lock()
	disabled := e.host.Disabled
	e.mu.Unlock()
	if disabled {
		<-ctx.Done()
		return
	}

	for {
		if ctx.Err() != nil {
			return
		}
		e.mu.Lock()
		e.link = protocol.LinkLinking
		e.relinkAt = time.Time{}
		host := e.host.Clone()
		ctrl := e.controlPath
		sock := e.tunnelSock
		e.mu.Unlock()
		m.d.changes.fire(true, false)

		err := m.connectOnce(ctx, e, host, ctrl, sock)
		if ctx.Err() != nil {
			return
		}

		e.mu.Lock()
		e.fails++
		wait := relinkWait(e.fails)
		e.link = protocol.LinkDown
		e.activeMode = ""
		e.linkErr = err.Error()
		e.relinkAt = time.Now().Add(wait)
		e.remoteSessions = nil
		tun := e.tunnel
		e.tunnel = nil
		e.mu.Unlock()
		if tun != nil {
			_ = tun.Close()
		}
		m.d.changes.fire(true, true)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-e.reconnectCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (m *sshManager) connectOnce(ctx context.Context, e *sshEntry, host config.SSHHost, ctrl, sock string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	probe, err := sshremote.Probe(probeCtx, host, ctrl)
	cancel()
	if err != nil {
		return err
	}

	mode := host.EffectiveMode()
	tryDaemon := mode == config.SSHModeDaemon || (mode == config.SSHModeAuto && probe.MakodPath != "")

	if tryDaemon {
		tunCtx, tunCancel := context.WithTimeout(ctx, 25*time.Second)
		tun, tunErr := sshremote.StartDaemonTunnel(tunCtx, host, probe.MakodPath, ctrl, sock, m.d.hello())
		tunCancel()
		if tunErr == nil {
			e.mu.Lock()
			if ctx.Err() != nil {
				e.mu.Unlock()
				_ = tun.Close()
				return ctx.Err()
			}
			e.probe = probe
			e.tunnel = tun
			e.remoteNode = tun.RemoteNode
			e.activeMode = sshremote.FeatureSSHDaemon
			e.link = protocol.LinkUp
			e.linkErr = ""
			e.fails = 0
			e.lastSeen = time.Now()
			e.mu.Unlock()
			m.d.changes.fire(true, true)
			return m.monitorDaemonTunnel(ctx, e, tun)
		}
		if mode == config.SSHModeDaemon {
			return tunErr
		}
		m.d.log.Debug("ssh remote makod unavailable, falling back to direct ssh", "host", host.DisplayName(), "err", tunErr)
	}

	// Direct SSH mode (no remote makod required).
	e.mu.Lock()
	e.probe = probe
	e.activeMode = sshremote.FeatureSSHDirect
	e.link = protocol.LinkUp
	e.linkErr = ""
	e.fails = 0
	e.lastSeen = time.Now()
	e.mu.Unlock()
	m.d.changes.fire(true, false)

	// Periodic lightweight keepalive probe to detect dropped hosts.
	tick := time.NewTicker(45 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.reconnectCh:
			return errors.New("reconnecting")
		case <-tick.C:
			checkCtx, checkCancel := context.WithTimeout(ctx, 15*time.Second)
			p, err := sshremote.Probe(checkCtx, host, ctrl)
			checkCancel()
			if err != nil {
				return err
			}
			e.mu.Lock()
			e.probe = p
			e.lastSeen = time.Now()
			e.mu.Unlock()
		}
	}
}

// monitorDaemonTunnel watches a tunneled remote makod for session and node
// changes until the tunnel exits or ctx is canceled.
func (m *sshManager) monitorDaemonTunnel(ctx context.Context, e *sshEntry, tun *sshremote.DaemonTunnel) error {
	refresh := func() error {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s, err := m.dialTunnelHailed(rctx, tun)
		if err != nil {
			return err
		}
		defer s.Close()
		node, err := s.Welcomed()
		if err != nil {
			return err
		}
		reply, err := transport.Ask[*protocol.Sessions](rctx, s, protocol.TypeListSess, &protocol.ListSessions{
			Approvals: m.d.approvals.watchedHere(),
		})
		if err != nil {
			return err
		}
		e.mu.Lock()
		if node != nil {
			e.remoteNode = node
		}
		e.remoteSessions = ownSessions(reply.GetSessions(), e.remoteNode.GetId(), e.nodeID)
		e.lastSeen = time.Now()
		e.mu.Unlock()
		m.d.changes.fire(true, true)
		return nil
	}

	if err := refresh(); err != nil {
		return err
	}

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	watchErr := make(chan error, 1)
	go func() {
		s, err := m.dialTunnelHailed(watchCtx, tun)
		if err != nil {
			watchErr <- err
			return
		}
		defer s.Hangup()
		stop := context.AfterFunc(watchCtx, func() { _ = s.Hangup() })
		defer stop()
		if err := s.Send(protocol.TypeWatch, "", &protocol.Watch{Approvals: m.d.approvals.watchedHere()}); err != nil {
			watchErr <- err
			return
		}
		for {
			env, err := s.Recv()
			if err != nil {
				watchErr <- err
				return
			}
			if _, err := transport.Reply[*protocol.Changed](env); err != nil {
				watchErr <- err
				return
			}
			_ = refresh()
		}
	}()

	poll := time.NewTicker(10 * time.Second)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.reconnectCh:
			return errors.New("reconnecting")
		case <-tun.Done():
			if err := tun.Err(); err != nil {
				return fmt.Errorf("SSH tunnel closed: %w", err)
			}
			return errors.New("SSH tunnel closed")
		case err := <-watchErr:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// If watch dropped, verify the tunnel is still alive; reconnect if not.
			if rerr := refresh(); rerr != nil {
				return fmt.Errorf("SSH daemon lost: %w", err)
			}
			return fmt.Errorf("SSH watch stream closed: %w", err)
		case <-poll.C:
			if err := refresh(); err != nil {
				return err
			}
		}
	}
}

func (m *sshManager) dialTunnelHailed(ctx context.Context, tun *sshremote.DaemonTunnel) (*transport.Stream, error) {
	s, err := tun.Dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Hail(m.d.hello()); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// nodes returns protocol.Node records for all configured SSH hosts.
func (m *sshManager) nodes() []*protocol.Node {
	m.mu.Lock()
	order := slices.Clone(m.order)
	entries := make([]*sshEntry, 0, len(order))
	for _, id := range order {
		if e, ok := m.entries[id]; ok {
			entries = append(entries, e)
		}
	}
	m.mu.Unlock()

	out := make([]*protocol.Node, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.protocolNode())
	}
	return out
}

func (m *sshManager) node(nodeID string) (*protocol.Node, bool) {
	m.mu.Lock()
	e, ok := m.entries[nodeID]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return e.protocolNode(), true
}

func (e *sshEntry) protocolNode() *protocol.Node {
	e.mu.Lock()
	defer e.mu.Unlock()

	up := e.link == protocol.LinkUp
	n := &protocol.Node{
		Id:        e.nodeID,
		Name:      e.host.DisplayName(),
		Addr:      e.host.DisplayAddr(),
		Os:        e.probe.OS,
		Arch:      e.probe.Arch,
		Version:   "ssh",
		Source:    "ssh",
		Revision:  protocol.Revision,
		TrustsUs:  up,
		Link:      e.link,
		LinkError: e.linkErr,
		Features:  []string{sshremote.FeatureSSH},
	}
	if !e.lastSeen.IsZero() {
		n.LastSeen = timestamppb.New(e.lastSeen)
	}
	if !e.relinkAt.IsZero() {
		n.RelinkAt = protocol.Time(e.relinkAt)
	}
	if up {
		n.Hops = 1
	}
	if e.activeMode != "" {
		n.Features = append(n.Features, e.activeMode)
	}
	if e.activeMode == sshremote.FeatureSSHDaemon && e.remoteNode != nil {
		rn := e.remoteNode
		if rn.GetOs() != "" {
			n.Os = rn.GetOs()
		}
		if rn.GetArch() != "" {
			n.Arch = rn.GetArch()
		}
		if rn.GetVersion() != "" {
			n.Version = rn.GetVersion()
		}
		if rn.GetRevision() > 0 {
			n.Revision = rn.GetRevision()
		}
		n.Agents = rn.GetAgents()
		n.Projects = rn.GetProjects()
	} else if up {
		n.Agents = sshremote.AgentInfos(e.probe.Agents)
	}
	return n
}

func (m *sshManager) entry(nodeID string) (*sshEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[nodeID]
	return e, ok
}

// isDaemonTunnel reports whether nodeID is an SSH host connected via a tunneled
// remote makod daemon.
func (m *sshManager) isDaemonTunnel(nodeID string) bool {
	e, ok := m.entry(nodeID)
	if !ok {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activeMode == sshremote.FeatureSSHDaemon && e.tunnel != nil
}

// isDirect reports whether nodeID is an SSH host currently operating in direct
// (agentless) SSH mode.
func (m *sshManager) isDirect(nodeID string) bool {
	if !config.IsSSHNodeID(nodeID) {
		return false
	}
	e, ok := m.entry(nodeID)
	if !ok {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activeMode != sshremote.FeatureSSHDaemon
}

// dialDaemon opens a hailed mako protocol stream to a daemon-mode SSH host.
func (m *sshManager) dialDaemon(ctx context.Context, nodeID string) (*transport.Stream, error) {
	e, ok := m.entry(nodeID)
	if !ok {
		return nil, fmt.Errorf("unknown SSH host %q", nodeID)
	}
	e.mu.Lock()
	tun := e.tunnel
	name := e.host.DisplayName()
	linkErr := e.linkErr
	e.mu.Unlock()
	if tun == nil {
		if linkErr != "" {
			return nil, fmt.Errorf("%s is not connected: %s", name, linkErr)
		}
		return nil, fmt.Errorf("%s is not connected", name)
	}
	return m.dialTunnelHailed(ctx, tun)
}

// ownSessions is the sessions of the remote node remote among those its
// daemon listed, each marked as on nodeID, its SSH host. Asked as a
// front-end asks, the daemon lists the whole mesh it reaches, which can
// hold this node's own sessions: those stay with the nodes they are on.
func ownSessions(listed []*protocol.SessionInfo, remote, nodeID string) []*protocol.SessionInfo {
	out := make([]*protocol.SessionInfo, 0, len(listed))
	for _, si := range listed {
		if on := si.GetNodeId(); on != "" && on != remote {
			continue
		}
		cp := proto.CloneOf(si)
		cp.NodeId = nodeID
		if cp.GetParentNodeId() == remote {
			cp.ParentNodeId = nodeID
		}
		out = append(out, cp)
	}
	return out
}

// appendUnlisted appends to sessions those of more it does not list yet. A
// host reached over SSH can be a mesh peer as well, its sessions then
// listed already, by the mesh.
func appendUnlisted(sessions, more []*protocol.SessionInfo) []*protocol.SessionInfo {
	listed := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		listed[s.GetId()] = true
	}
	for _, s := range more {
		if !listed[s.GetId()] {
			listed[s.GetId()] = true
			sessions = append(sessions, s)
		}
	}
	return sessions
}

// daemonSessions returns all sessions reported by connected daemon-mode SSH hosts.
func (m *sshManager) daemonSessions() []*protocol.SessionInfo {
	m.mu.Lock()
	entries := make([]*sshEntry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.Unlock()

	var out []*protocol.SessionInfo
	for _, e := range entries {
		e.mu.Lock()
		if e.link == protocol.LinkUp && e.activeMode == sshremote.FeatureSSHDaemon {
			for _, s := range e.remoteSessions {
				out = append(out, proto.CloneOf(s))
			}
		}
		e.mu.Unlock()
	}
	return out
}

// directConnInfo returns the host config, control socket path, and remote home
// directory for a connected direct-mode SSH host.
func (m *sshManager) directConnInfo(nodeID string) (config.SSHHost, string, string, string, error) {
	e, ok := m.entry(nodeID)
	if !ok {
		return config.SSHHost{}, "", "", "", fmt.Errorf("unknown SSH host %q", nodeID)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.link != protocol.LinkUp {
		if e.linkErr != "" {
			return config.SSHHost{}, "", "", "", fmt.Errorf("%s is offline: %s", e.host.DisplayName(), e.linkErr)
		}
		return config.SSHHost{}, "", "", "", fmt.Errorf("%s is not connected", e.host.DisplayName())
	}
	return e.host.Clone(), e.controlPath, e.probe.Home, e.probe.Shell, nil
}

// gitHead returns cached git metadata for a remote directory on a direct SSH
// host, refreshing asynchronously in the background when stale so ListSessions
// never blocks.
func (m *sshManager) gitHead(nodeID, dir string) projectHead {
	if dir == "" {
		return projectHead{}
	}
	e, ok := m.entry(nodeID)
	if !ok {
		return projectHead{}
	}
	e.mu.Lock()
	if e.link != protocol.LinkUp {
		e.mu.Unlock()
		return projectHead{}
	}
	if e.heads == nil {
		e.heads = map[string]cachedSSHHead{}
	}
	c := e.heads[dir]
	stale := c.at.IsZero() || time.Since(c.at) > 15*time.Second
	if stale && !c.fetching {
		c.fetching = true
		e.heads[dir] = c
		host, ctrl, home := e.host.Clone(), e.controlPath, e.probe.Home
		e.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(m.d.ctx, 8*time.Second)
			defer cancel()
			h := sshremote.GitHead(ctx, host, ctrl, home, dir)
			e.mu.Lock()
			e.heads[dir] = cachedSSHHead{head: h, at: time.Now()}
			e.mu.Unlock()
			m.d.changes.fire(false, true)
		}()
	} else {
		e.mu.Unlock()
	}
	return projectHead{
		branch: c.head.Branch,
		repo:   c.head.Repo,
		owner:  c.head.Owner,
		remote: c.head.Remote,
		stamp:  c.head.Stamp,
		at:     c.at,
	}
}

// clearRoutedNodeID clones req and clears its node_id field so a tunneled
// remote makod handles the request locally rather than trying to route "ssh:...".
func clearRoutedNodeID[Req routed](req Req) Req {
	cp := proto.Clone(req).(Req)
	msg := cp.ProtoReflect()
	if fd := msg.Descriptor().Fields().ByName("node_id"); fd != nil && fd.Kind() == protoreflect.StringKind {
		msg.Clear(fd)
	}
	return cp
}

// openDirectSSHSession starts a new session on a direct-SSH host, managed as a
// local PTY running `ssh -tt ...`.
func (d *Daemon) openDirectSSHSession(ctx context.Context, req *protocol.OpenSession) (*session.Session, error) {
	parent, name, prompt := req.GetParentSessionId(), req.GetName(), req.GetPrompt()
	req = proto.CloneOf(req)
	req.ParentSessionId, req.Name, req.Prompt = "", "", ""
	s, err := d.startDirectSSHSession(ctx, newSessionID(), req, parent)
	if err != nil {
		return nil, err
	}
	if name != "" {
		if _, err := d.renameSession(s.ID(), name); err != nil {
			return nil, fmt.Errorf("session %s started but could not be named: %w", s.ID(), err)
		}
	}
	if prompt != "" {
		go d.deliverInitialPrompt(s, prompt)
	}
	return s, nil
}

func (d *Daemon) startDirectSSHSession(ctx context.Context, id string, req *protocol.OpenSession, parent string) (*session.Session, error) {
	// The workspace sandbox is this computer's: what runs on an SSH host
	// runs outside it.
	d.sessionMu.Lock()
	boxedParent := d.sandboxedLocked(parent)
	d.sessionMu.Unlock()
	if boxedParent {
		return nil, errSandboxRemote
	}
	if mode, err := d.configuredSandbox(req.GetAgent(), req.GetProfile()); err != nil || mode != "" {
		return nil, cmp.Or(err, fmt.Errorf("agent %q runs in the workspace sandbox, which cannot run sessions on SSH hosts", agentspec.Target(req.GetAgent(), req.GetProfile())))
	}
	req = proto.CloneOf(req)
	nodeID := req.GetNodeId()
	host, ctrl, remoteHome, remoteShell, err := d.ssh.directConnInfo(nodeID)
	if err != nil {
		return nil, err
	}
	project := req.GetProject()
	if project == "" {
		project = host.DefaultDir
	}
	project = sshremote.ExpandRemotePath(project, remoteHome)
	if project == "" {
		project = remoteHome
	}
	req.Project = project

	if req.GetWorktree() != nil && req.GetWorktree().GetPath() == "" {
		wtCtx, cancel := context.WithTimeout(ctx, time.Minute)
		err := sshremote.EnsureWorktree(wtCtx, host, ctrl, req)
		cancel()
		if err != nil {
			return nil, err
		}
	}

	argv, shown, err := sshremote.BuildSessionCommand(sshremote.SessionLaunch{
		Host:        host,
		ControlPath: ctrl,
		RemoteHome:  remoteHome,
		RemoteShell: remoteShell,
		Agent:       req.GetAgent(),
		Profile:     req.GetProfile(),
		Project:     req.GetProject(),
		Args:        req.GetArgs(),
		Env:         req.GetEnv(),
		Model:       req.GetModel(),
		Effort:      req.GetEffort(),
		Yolo:        req.Yolo,
		LocalAgents: d.agents(),
	})
	if err != nil {
		return nil, err
	}

	if s, err := d.claimStart(ctx, id); s != nil || err != nil {
		return s, err
	}
	defer d.endStart(id)

	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	s, err := session.Start(session.Spec{
		ID:       id,
		NodeID:   nodeID,
		Agent:    req.GetAgent(),
		Profile:  req.GetProfile(),
		Project:  req.GetProject(),
		Command:  argv,
		Shown:    shown,
		Cols:     uint16(req.GetCols()),
		Rows:     uint16(req.GetRows()),
		Worktree: req.GetWorktree(),
		OnChange: d.pokeStatus,
	})
	if err != nil {
		return nil, err
	}
	prev, existed := d.saved[id]
	if existed && prev.Info.GetName() != "" {
		s.SetName(prev.Info.GetName())
	}
	d.sessions.add(s)
	if d.saved == nil {
		d.saved = map[string]savedSession{}
	}
	info := s.Info()
	info.NodeId = nodeID
	d.saved[id] = savedSession{
		Info:     info,
		Request:  req,
		ParentID: parent,
	}
	if err := d.saveSessionsLocked(); err != nil {
		d.log.Warn("save ssh session failed", "id", id, "err", err)
	}
	return s, nil
}

// handleDirectSSHTunnel serves a Tunnel request targeting a direct SSH node via
// `ssh -W 127.0.0.1:<port>`.
func (d *Daemon) handleDirectSSHTunnel(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, req *protocol.Tunnel) error {
	host, ctrl, _, _, err := d.ssh.directConnInfo(req.GetNodeId())
	if err != nil {
		return err
	}
	conn, err := sshremote.OpenPortTunnel(ctx, host, ctrl, int(req.GetPort()))
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := stream.Send(protocol.TypeTunneled, env.GetId(), &protocol.Tunneled{
		Addr: net.JoinHostPort("127.0.0.1", fmt.Sprint(req.GetPort())),
	}); err != nil {
		return err
	}
	if err := transport.Splice(ctx, stream, conn); err != nil && !isClosed(err) {
		d.log.Debug("ssh port tunnel ended", "host", host.DisplayName(), "port", req.GetPort(), "err", err)
	}
	return nil
}

// handleDirectSSHWatchFiles replies to WatchFiles for a direct SSH node by
// reporting its work trees as unwatched so the GUI gently polls them.
func (d *Daemon) handleDirectSSHWatchFiles(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, req *protocol.WatchFiles) (err error) {
	if err := stream.Send(protocol.TypeFilesChanged, env.GetId(), &protocol.FilesChanged{
		Unwatched: req.GetTrees(),
	}); err != nil {
		return err
	}
	h := watchStreamFrames(stream, func(nextEnv *protocol.Envelope) bool {
		if r, err := protocol.Body[*protocol.WatchFiles](nextEnv); err == nil {
			_ = stream.Send(protocol.TypeFilesChanged, env.GetId(), &protocol.FilesChanged{
				Unwatched: r.GetTrees(),
			})
		}
		return true
	})
	defer func() {
		err = h.finish(ctx, env.GetId(), err)
	}()
	select {
	case <-ctx.Done():
		return nil
	case <-h.done:
		return nil
	}
}
