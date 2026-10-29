package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/device"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/p2p"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// peerTTL drops a discovered node that has not been seen for this long.
const peerTTL = 5 * time.Minute

// Daemon serves front-ends on its Unix socket, and peers over libp2p.
type Daemon struct {
	cfg *config.Config
	log *slog.Logger
	dir string // the configuration directory, config.Dir() when empty

	sessionMu   sync.Mutex // protects saved launches and activation
	saved       map[string]savedSession
	sessionPath string

	sessions      *registry
	conversations conversations
	approvals     approvals
	peers         *peerTable

	// ln is the socket front-ends reach the daemon on, at socket; only
	// this user can.
	ln     net.Listener
	socket string
	ready  chan struct{} // closed once the listener is up; see Ready

	mu sync.Mutex

	// p2p links this daemon to its peers over libp2p; nil when it is off
	// or could not start, leaving the daemon to itself.
	p2p *p2p.Host
	// keyPrint and ownerPrint are the fingerprints of the host's own key
	// and of the SSH key vouching for it.
	keyPrint, ownerPrint string
	access               accessRequests       // the peers asking to be let in
	pairing              pairing              // the pairing code waiting to be read
	heads                heads                // the projects' branches
	remote               peerReports          // what peers say of their sessions and routes; see routes.go
	changes              notifier             // tells watching front-ends what changed
	workspace            workspaces           // the windows' tabs and panes; see workspace.go
	linking              map[string]time.Time // peer ID -> the last link tried, until one holds
	ctx                  context.Context      // Run's, for work that outlasts a request

	kinds  map[string]device.Kind // the kinds of device; see devices.go
	claims claims                 // the devices of this node lent to others
	leases leases                 // the devices this node borrows

	agentFile agentFile
	mcp       *mcp.Resolver // nil without a configuration directory

	hookBinary  string // the program agents run as their hook; see HookMain
	hookCommand string // the shell command agents run as their hook; "" for none
	// statusCommand is the one Claude runs as its status line, which
	// reports the account's limits; "" for none.
	statusCommand string
}

// New builds a daemon from configuration.
func New(cfg *config.Config, log *slog.Logger) *Daemon {
	if log == nil {
		log = slog.Default()
	}
	return &Daemon{
		cfg:      cfg,
		log:      log,
		sessions: newRegistry(),
		peers:    newPeerTable(),
		linking:  map[string]time.Time{},
		ctx:      context.Background(),
		ready:    make(chan struct{}),
		mcp:      newResolver(log),
		kinds:    deviceKinds(cfg),

		hookBinary: hookProgram,
	}
}

// Ready is closed once Run is listening, after which Addr is valid. A Run
// that fails to listen returns its error without closing it.
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// Socket is the socket the daemon listens on. Valid after Ready.
func (d *Daemon) Socket() string { return d.socket }

// Self describes this daemon as a node record.
func (d *Daemon) Self() *protocol.Node {
	return &protocol.Node{
		Id:         d.cfg.NodeID,
		Name:       d.cfg.Name,
		Addr:       d.addr(),
		Os:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Version:    config.Version,
		Source:     "self",
		LastSeen:   timestamppb.Now(),
		Revision:   protocol.Revision,
		Agents:     agentInfo(d.agents()),
		PeerId:     d.peerID(),
		Multiaddrs: d.multiaddrs(),
		Key:        d.keyPrint,
		Owner:      d.ownerPrint,
	}
}

// publicSelf is this daemon as it shows itself to a node it does not let
// in: without the agents it can launch.
func (d *Daemon) publicSelf() *protocol.Node {
	self := d.Self()
	self.Agents = nil
	return self
}

// Run serves until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	dir := d.dir
	if dir == "" {
		var err error
		if dir, err = config.Dir(); err != nil {
			return err
		}
	}
	run, err := config.RunDir(dir)
	if err != nil {
		return err
	}
	socket := filepath.Join(run, "daemon.sock")
	d.ln, err = transport.Listen(socket)
	if err != nil {
		return err
	}
	d.socket = socket
	defer d.ln.Close()
	defer os.Remove(socket)
	if err := d.loadSessions(dir); err != nil {
		return err
	}
	d.workspace.load(dir)
	defer d.sessions.closeAll()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.ctx = ctx
	d.listenHooks(ctx, run)
	if !d.cfg.P2P.Disabled {
		if err := d.startP2P(ctx, dir); err != nil {
			d.log.Warn("libp2p did not start; this daemon has no peers", "err", err)
		} else {
			defer d.p2p.Close()
		}
	}
	d.log.Info("daemon listening", "socket", socket, "node", d.cfg.NodeID, "name", d.cfg.Name)
	close(d.ready)

	go d.warmContextServers(ctx)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); d.serve(ctx) }()
	go func() { defer wg.Done(); d.discover(ctx) }()
	go func() { defer wg.Done(); d.maintain(ctx) }()
	go func() { defer wg.Done(); d.watchState(ctx) }()

	<-ctx.Done()
	_ = d.ln.Close()
	wg.Wait()
	return nil
}

// serve takes front-ends' connections on the socket, one request stream
// to a connection.
func (d *Daemon) serve(ctx context.Context) {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			d.log.Warn("accept failed", "err", err)
			continue
		}
		go func() {
			stream := transport.NewStream(conn)
			defer stream.Close()
			if err := d.serveStream(ctx, stream, false); err != nil && !isClosed(err) {
				d.log.Debug("stream ended", "err", err)
			}
		}()
	}
}

// serveStream handles one request stream: from a front-end on the socket,
// which only this user can reach, or from a peer this daemon lets in, on
// libp2p. The first frame must be a hello.
func (d *Daemon) serveStream(ctx context.Context, stream *transport.Stream, fromPeer bool) error {
	env, err := stream.Recv()
	if err != nil {
		return err
	}
	if env.Type() != protocol.TypeHello {
		return protocol.WriteError(stream, env.GetId(), errors.New("expected hello"))
	}
	hello, err := protocol.Body[*protocol.Hello](env)
	if err != nil {
		return protocol.WriteError(stream, env.GetId(), err)
	}
	if fromPeer {
		ctx = withHops(ctx, hello.GetHops()+1)
	}
	if err := stream.Send(protocol.TypeWelcome, env.GetId(), &protocol.Welcome{Node: d.Self()}); err != nil {
		return err
	}

	for {
		env, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := d.dispatch(ctx, stream, env, fromPeer); err != nil {
			if isClosed(err) {
				return err
			}
			if writeErr := protocol.WriteError(stream, env.GetId(), err); writeErr != nil {
				return writeErr
			}
		}
	}
}

// fromPeer marks a stream from another daemon, on libp2p, rather than from
// a front-end.
func (d *Daemon) dispatch(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	switch env.Type() {
	case protocol.TypeListNodes:
		return stream.Send(protocol.TypeNodes, env.GetId(), &protocol.Nodes{Nodes: d.nodes()})

	case protocol.TypeAccess:
		return d.handleAccess(ctx, stream, env, fromPeer)

	case protocol.TypeGetWorkspace, protocol.TypeWorkspace:
		return d.handleWorkspace(ctx, stream, env, fromPeer)

	case protocol.TypeListSess:
		return d.handleListSessions(ctx, stream, env, fromPeer)

	case protocol.TypeWatch:
		return d.handleWatch(ctx, stream, env, fromPeer)

	case protocol.TypeOpenSession:
		return d.handleOpenSession(ctx, stream, env)

	case protocol.TypeCloseSess:
		return d.handleCloseSession(ctx, stream, env)

	case protocol.TypeRenameSess:
		return d.handleRenameSession(ctx, stream, env)

	case protocol.TypeAttach:
		return d.handleAttach(ctx, stream, env)

	case protocol.TypeListDir:
		return d.handleListDir(ctx, stream, env)

	case protocol.TypeReadFile:
		return d.handleReadFile(ctx, stream, env)

	case protocol.TypeWriteFile:
		return d.handleWriteFile(ctx, stream, env)

	case protocol.TypeStageFile:
		return d.handleStageFile(ctx, stream, env)

	case protocol.TypeTunnel:
		return d.handleTunnel(ctx, stream, env)

	case protocol.TypeListDevices:
		return d.handleListDevices(ctx, stream, env, fromPeer)

	case protocol.TypeBorrow:
		return d.handleBorrow(ctx, stream, env)

	case protocol.TypeGiveBack:
		return d.handleGiveBack(ctx, stream, env)

	case protocol.TypeListLeases:
		return d.handleListLeases(ctx, stream, env)

	case protocol.TypeClaim:
		return d.handleClaim(ctx, stream, env)

	case protocol.TypeDeviceConn:
		return d.handleDeviceConn(ctx, stream, env)

	case protocol.TypeDeviceListen:
		return d.handleDeviceListen(ctx, stream, env)

	case protocol.TypeDeviceAccept:
		return d.handleDeviceAccept(ctx, stream, env)

	case protocol.TypeGitStatus:
		return d.handleGitStatus(ctx, stream, env)

	case protocol.TypeGitDiff:
		return d.handleGitDiff(ctx, stream, env)

	case protocol.TypeFileOp:
		return d.handleFileOp(ctx, stream, env)

	case protocol.TypeGitLog:
		return d.handleGitLog(ctx, stream, env)

	case protocol.TypeGitShow:
		return d.handleGitShow(ctx, stream, env)

	case protocol.TypeGitRun:
		return d.handleGitRun(ctx, stream, env)

	case protocol.TypeForgeList:
		return d.handleGitHubList(ctx, stream, env)

	case protocol.TypeForgeView:
		return d.handleGitHubView(ctx, stream, env)

	case protocol.TypeForgeRun:
		return d.handleGitHubRun(ctx, stream, env)

	case protocol.TypeReadChat:
		return d.handleReadChat(ctx, stream, env)

	case protocol.TypeAnswerApproval:
		return d.handleAnswerApproval(ctx, stream, env)

	case protocol.TypeSend:
		return d.handleSend(ctx, stream, env)

	case protocol.TypeSearch:
		return d.handleSearch(ctx, stream, env)

	case protocol.TypeResize:
		payload, err := protocol.Body[*protocol.Resize](env)
		if err != nil {
			return err
		}
		s, ok := d.sessions.get(payload.GetSessionId())
		if !ok {
			return fmt.Errorf("unknown session %q", payload.GetSessionId())
		}
		if err := s.Resize(uint16(payload.GetCols()), uint16(payload.GetRows())); err != nil {
			return err
		}
		return stream.Send(protocol.TypeSessionInfo, env.GetId(), s.Info())

	case protocol.TypeInput:
		payload, err := protocol.Body[*protocol.Input](env)
		if err != nil {
			return err
		}
		s, ok := d.sessions.get(payload.GetSessionId())
		if !ok {
			return fmt.Errorf("unknown session %q", payload.GetSessionId())
		}
		_, err = s.Write(payload.GetData())
		return err

	default:
		return &protocol.CodedError{Code: protocol.CodeUnsupported, Message: fmt.Sprintf("unsupported message %q", env.Type())}
	}
}

func isClosed(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, network.ErrReset)
}
