package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/mcp"
	"github.com/ironpark/sanbo/internal/p2p"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// peerTTL drops a discovered node that has not been seen for this long.
const peerTTL = 5 * time.Minute

// Daemon serves front-ends and peers over QUIC.
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

	ln    *transport.Listener
	port  int
	ready chan struct{} // closed once the listener is up; see Ready

	mu       sync.Mutex
	outbound map[string]*quic.Conn // node ID -> peer connection, for older peers

	// p2p links this daemon to its peers over libp2p; nil when it is off
	// or could not start, leaving the QUIC links alone.
	p2p     *p2p.Host
	heads   heads                // the projects' branches
	remote  remoteSessions       // the sessions peers publish
	changes notifier             // tells watching front-ends what changed
	linking map[string]time.Time // peer ID -> the last link tried, until one holds
	ctx     context.Context      // Run's, for work that outlasts a request

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
		outbound: map[string]*quic.Conn{},
		linking:  map[string]time.Time{},
		ctx:      context.Background(),
		ready:    make(chan struct{}),
		mcp:      newResolver(log),

		hookBinary: hookProgram,
	}
}

// Ready is closed once Run is listening, after which Addr is valid. A Run
// that fails to listen returns its error without closing it.
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// Addr is the address the daemon listens on. Valid after Ready.
func (d *Daemon) Addr() net.Addr { return d.ln.Addr() }

// Self describes this daemon as a node record.
func (d *Daemon) Self() *protocol.Node {
	addr := d.cfg.Listen
	if d.ln != nil {
		addr = d.ln.Addr().String()
	}
	return &protocol.Node{
		Id:         d.cfg.NodeID,
		Name:       d.cfg.Name,
		Addr:       addr,
		Os:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Version:    config.Version,
		Source:     "self",
		LastSeen:   timestamppb.Now(),
		Revision:   protocol.Revision,
		Agents:     agentInfo(d.agents()),
		PeerId:     d.peerID(),
		Multiaddrs: d.multiaddrs(),
	}
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
	tlsConf, err := transport.ServerTLS(dir)
	if err != nil {
		return err
	}
	d.ln, err = transport.Listen(d.cfg.Listen, tlsConf)
	if err != nil {
		return err
	}
	defer d.ln.Close()
	if err := d.loadSessions(dir); err != nil {
		return err
	}
	defer d.sessions.closeAll()

	d.port = portOf(d.ln.Addr())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.ctx = ctx
	if run, err := config.RunDir(); err == nil {
		d.listenHooks(ctx, run)
	}
	if !d.cfg.P2P.Disabled {
		if err := d.startP2P(ctx, dir); err != nil {
			d.log.Warn("libp2p did not start; peers are reached over QUIC alone", "err", err)
		} else {
			defer d.p2p.Close()
		}
	}
	d.log.Info("daemon listening", "addr", d.ln.Addr().String(), "node", d.cfg.NodeID, "name", d.cfg.Name)
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

func (d *Daemon) serve(ctx context.Context) {
	for {
		conn, err := d.ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.log.Warn("accept failed", "err", err)
			continue
		}
		go d.serveConn(ctx, conn)
	}
}

func (d *Daemon) serveConn(ctx context.Context, conn *quic.Conn) {
	defer conn.CloseWithError(0, "bye")
	for {
		stream, err := transport.AcceptStream(ctx, conn)
		if err != nil {
			return
		}
		go func() {
			defer stream.Close()
			from := &protocol.Node{Addr: remoteAddr(conn, d.port)}
			if err := d.serveStream(ctx, stream, from); err != nil && !isClosed(err) {
				d.log.Debug("stream ended", "err", err)
			}
		}()
	}
}

// serveStream handles one request stream, from where the node from says:
// its address on QUIC, or its peer ID on libp2p. The first frame must be
// a hello, which on QUIC carries the mesh's key; a libp2p stream comes
// from a host that has shown it knows the key already.
func (d *Daemon) serveStream(ctx context.Context, stream *transport.Stream, from *protocol.Node) error {
	trusted := from.GetPeerId() != ""
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
	if !trusted && d.cfg.PSK != "" && hello.GetAuthTag() != d.cfg.PSK {
		return protocol.WriteError(stream, env.GetId(), errors.New("authentication failed"))
	}
	if err := stream.Send(protocol.TypeWelcome, env.GetId(), &protocol.Welcome{Node: d.Self()}); err != nil {
		return err
	}
	if hello.GetIsPeer() && hello.GetNodeId() != "" && hello.GetNodeId() != d.cfg.NodeID {
		from.Id, from.Name, from.Source = hello.GetNodeId(), hello.GetName(), "inbound"
		d.notePeer(from)
	}

	for {
		env, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := d.dispatch(ctx, stream, env, hello.GetIsPeer()); err != nil {
			if isClosed(err) {
				return err
			}
			if writeErr := protocol.WriteError(stream, env.GetId(), err); writeErr != nil {
				return writeErr
			}
		}
	}
}

// fromPeer marks a stream greeted by another daemon rather than a front-end.
func (d *Daemon) dispatch(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	switch env.Type() {
	case protocol.TypeListNodes:
		nodes := append([]*protocol.Node{d.Self()}, d.peers.list()...)
		return stream.Send(protocol.TypeNodes, env.GetId(), &protocol.Nodes{Nodes: nodes})

	case protocol.TypeGossip:
		payload, err := protocol.Body[*protocol.Nodes](env)
		if err != nil {
			return err
		}
		for _, node := range payload.GetNodes() {
			if node.GetId() != d.cfg.NodeID {
				d.peers.merge(node)
			}
		}
		return stream.Send(protocol.TypeNodes, env.GetId(), &protocol.Nodes{Nodes: d.peers.list()})

	case protocol.TypeListSess:
		return d.handleListSessions(ctx, stream, env, fromPeer)

	case protocol.TypeWatch:
		return d.handleWatch(ctx, stream, env)

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

	case protocol.TypeReadChat:
		return d.handleReadChat(ctx, stream, env)

	case protocol.TypeAnswerApproval:
		return d.handleAnswerApproval(ctx, stream, env)

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

func portOf(addr net.Addr) int {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return config.DefaultPort
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return config.DefaultPort
	}
	return n
}

// remoteAddr rewrites a peer's ephemeral source port to the sanbo service port,
// which is where that daemon can be dialed back.
func remoteAddr(conn *quic.Conn, fallbackPort int) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return net.JoinHostPort(host, strconv.Itoa(fallbackPort))
}

func isClosed(err error) bool {
	if err == nil {
		return false
	}
	var appErr *quic.ApplicationError
	var idleErr *quic.IdleTimeoutError
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, network.ErrReset) ||
		errors.As(err, &appErr) ||
		errors.As(err, &idleErr)
}
