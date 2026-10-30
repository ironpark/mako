// Package client talks to a mako daemon from a front-end.
package client

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Client is a front-end's link to the daemon of its machine, which relays
// to the rest of the mesh on its behalf. Each request is a connection of
// its own to the daemon's socket, which Close hangs up.
type Client struct {
	path   string
	ctx    context.Context // done once the client is closed
	cancel context.CancelFunc

	mu   sync.Mutex
	node *protocol.Node
}

// Dial reaches the daemon on the socket at path; empty path is the socket
// of this machine's configuration (config.SocketPath).
func Dial(ctx context.Context, path string) (*Client, error) {
	if path == "" {
		var err error
		if path, err = config.SocketPath(""); err != nil {
			return nil, err
		}
	}
	c := &Client{path: path}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	s, err := c.stream(ctx)
	if err != nil {
		c.cancel()
		return nil, err
	}
	s.Close()
	return c, nil
}

// Node reports the daemon this client is attached to, a copy of its own.
func (c *Client) Node() *protocol.Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	return proto.CloneOf(c.node)
}

// Close hangs up every connection the client has open.
func (c *Client) Close() error {
	c.cancel()
	return nil
}

// stream opens a greeted request stream: a connection of its own, hung up
// when the client closes.
func (c *Client) stream(ctx context.Context) (*transport.Stream, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, net.ErrClosed
	}
	s, node, err := transport.Dial(ctx, c.path, &protocol.Hello{Name: "mako-cli", Version: config.Version})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.node = node
	c.mu.Unlock()
	transport.CloseWith(s, c.ctx)
	return s, nil
}

// Nodes lists the daemons the mesh knows about.
func (c *Client) Nodes(ctx context.Context) ([]*protocol.Node, error) {
	reply, err := request[*protocol.Nodes](ctx, c, protocol.TypeListNodes, nil)
	if err != nil {
		return nil, err
	}
	return reply.GetNodes(), nil
}

// Access lists or changes who the daemon lets in; see protocol.Access.
func (c *Client) Access(ctx context.Context, req *protocol.Access) (*protocol.AccessInfo, error) {
	return request[*protocol.AccessInfo](ctx, c, protocol.TypeAccess, req)
}

// Workspace is the workspace the daemon holds, empty for none.
func (c *Client) Workspace(ctx context.Context) (*protocol.Workspace, error) {
	return request[*protocol.Workspace](ctx, c, protocol.TypeGetWorkspace, &protocol.GetWorkspace{})
}

// SetWorkspace gives the daemon a window's workspace, and returns the one
// it holds after: ws, or a newer one.
func (c *Client) SetWorkspace(ctx context.Context, ws *protocol.Workspace) (*protocol.Workspace, error) {
	return request[*protocol.Workspace](ctx, c, protocol.TypeWorkspace, ws)
}

// Sessions lists agent sessions across the mesh.
func (c *Client) Sessions(ctx context.Context) ([]*protocol.SessionInfo, error) {
	return c.ListSessions(ctx, &protocol.ListSessions{})
}

// ListSessions lists agent sessions across the mesh, saying what the
// asker shows of them.
func (c *Client) ListSessions(ctx context.Context, req *protocol.ListSessions) ([]*protocol.SessionInfo, error) {
	reply, err := request[*protocol.Sessions](ctx, c, protocol.TypeListSess, req)
	if err != nil {
		return nil, err
	}
	return reply.GetSessions(), nil
}

// ProjectAgents reads a project's agent activity without consuming messages.
func (c *Client) ProjectAgents(ctx context.Context, req *protocol.ReadProjectAgents) (*protocol.ProjectAgents, error) {
	return request[*protocol.ProjectAgents](ctx, c, protocol.TypeReadProjectAgents, req)
}

// Search lists a project's files on a node, or finds a text in them.
func (c *Client) Search(ctx context.Context, req *protocol.Search) (*protocol.SearchResult, error) {
	return request[*protocol.SearchResult](ctx, c, protocol.TypeSearch, req)
}

// AnswerApproval answers an approval a session's agent waits for.
func (c *Client) AnswerApproval(ctx context.Context, req *protocol.AnswerApproval) (*protocol.SessionInfo, error) {
	return request[*protocol.SessionInfo](ctx, c, protocol.TypeAnswerApproval, req)
}

// Open starts an agent session, on this daemon or on a peer.
func (c *Client) Open(ctx context.Context, req *protocol.OpenSession) (*protocol.SessionInfo, error) {
	return request[*protocol.SessionInfo](ctx, c, protocol.TypeOpenSession, req)
}

// request makes one request on a stream of its own, and returns the reply
// as R.
func request[R proto.Message](ctx context.Context, c *Client, typ protocol.Type, req proto.Message) (R, error) {
	s, err := c.stream(ctx)
	if err != nil {
		var zero R
		return zero, err
	}
	defer s.Close()
	return transport.Call[R](s, typ, req)
}

// ListDir lists a directory on a node.
func (c *Client) ListDir(ctx context.Context, req *protocol.ListDir) (*protocol.Dir, error) {
	return request[*protocol.Dir](ctx, c, protocol.TypeListDir, req)
}

// ReadFile reads the start of a file on a node.
func (c *Client) ReadFile(ctx context.Context, req *protocol.ReadFile) (*protocol.File, error) {
	return request[*protocol.File](ctx, c, protocol.TypeReadFile, req)
}

// WriteFile replaces a file's contents on a node.
func (c *Client) WriteFile(ctx context.Context, req *protocol.WriteFile) (*protocol.File, error) {
	return request[*protocol.File](ctx, c, protocol.TypeWriteFile, req)
}

// StageFile puts a piece of a file on a node, for a session there to read;
// see protocol.StageFile.
func (c *Client) StageFile(ctx context.Context, req *protocol.StageFile) (*protocol.Staged, error) {
	return request[*protocol.Staged](ctx, c, protocol.TypeStageFile, req)
}

// GitStatus reports the changes of the work tree a directory on a node is in.
func (c *Client) GitStatus(ctx context.Context, req *protocol.GitStatus) (*protocol.GitChanges, error) {
	return request[*protocol.GitChanges](ctx, c, protocol.TypeGitStatus, req)
}

// GitDiff reads one changed file's hunks on a node.
func (c *Client) GitDiff(ctx context.Context, req *protocol.GitDiff) (*protocol.Diff, error) {
	return request[*protocol.Diff](ctx, c, protocol.TypeGitDiff, req)
}

// FileOp makes, renames or deletes a file or folder on a node.
func (c *Client) FileOp(ctx context.Context, req *protocol.FileOp) (*protocol.File, error) {
	return request[*protocol.File](ctx, c, protocol.TypeFileOp, req)
}

// GitLog lists a repository's recent commits on a node.
func (c *Client) GitLog(ctx context.Context, req *protocol.GitLog) (*protocol.GitHistory, error) {
	return request[*protocol.GitHistory](ctx, c, protocol.TypeGitLog, req)
}

// GitShow reads what one commit changed on a node.
func (c *Client) GitShow(ctx context.Context, req *protocol.GitShow) (*protocol.GitCommitInfo, error) {
	return request[*protocol.GitCommitInfo](ctx, c, protocol.TypeGitShow, req)
}

// ForgeList lists a repository's pull requests, issues or Actions runs,
// through the GitHub CLI of the node it is on.
func (c *Client) ForgeList(ctx context.Context, req *protocol.ForgeList) (*protocol.ForgeItems, error) {
	return request[*protocol.ForgeItems](ctx, c, protocol.TypeForgeList, req)
}

// ForgeView reads one pull request, issue or run in full.
func (c *Client) ForgeView(ctx context.Context, req *protocol.ForgeView) (*protocol.ForgeDetail, error) {
	return request[*protocol.ForgeDetail](ctx, c, protocol.TypeForgeView, req)
}

// ForgeRun acts on a repository's GitHub: opens a pull request, comments,
// reviews, runs a workflow again and the like.
func (c *Client) ForgeRun(ctx context.Context, req *protocol.ForgeRun) (*protocol.GitResult, error) {
	return request[*protocol.GitResult](ctx, c, protocol.TypeForgeRun, req)
}

// GitRun runs a git operation, a commit or a push and the like, on a node.
func (c *Client) GitRun(ctx context.Context, req *protocol.GitRun) (*protocol.GitResult, error) {
	return request[*protocol.GitResult](ctx, c, protocol.TypeGitRun, req)
}

// Rename names a session, "" for none, and returns it as it is now.
func (c *Client) Rename(ctx context.Context, req *protocol.RenameSession) (*protocol.SessionInfo, error) {
	return request[*protocol.SessionInfo](ctx, c, protocol.TypeRenameSess, req)
}

// Commands lists the slash commands a session's agent takes.
func (c *Client) Commands(ctx context.Context, req *protocol.ListCommands) (*protocol.Commands, error) {
	return request[*protocol.Commands](ctx, c, protocol.TypeListCommands, req)
}

// Kill terminates a session.
func (c *Client) Kill(ctx context.Context, req *protocol.CloseSession) error {
	_, err := request[*protocol.Exited](ctx, c, protocol.TypeCloseSess, req)
	return err
}

// Attach connects the local terminal to a session until it ends or the user
// detaches. The terminal is put in raw mode for the duration.
func (c *Client) Attach(ctx context.Context, req *protocol.Attach) error {
	a, err := c.AttachStream(ctx, req)
	if err != nil {
		return err
	}
	defer a.Close()

	restore, err := makeRaw(os.Stdin)
	if err != nil {
		return err
	}
	defer restore()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.forwardWinch(ctx, a)
	go c.forwardInput(ctx, a, cancel)

	for {
		ev, err := a.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch ev.Kind {
		case protocol.EventSnapshot:
			fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
		case protocol.EventOutput:
			if _, err := os.Stdout.Write(ev.Data); err != nil {
				return err
			}
		case protocol.EventExited:
			fmt.Fprintf(os.Stderr, "\r\n[mako] session %s exited (%d)\r\n", req.GetSessionId(), ev.ExitCode)
			return nil
		case protocol.EventError:
			return fmt.Errorf("remote: %s", ev.Err)
		}
	}
}

func (c *Client) forwardInput(ctx context.Context, a *Attachment, cancel func()) {
	defer cancel()
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := a.Input(chunk); err != nil {
				return
			}
		}
		if err != nil || ctx.Err() != nil {
			return
		}
	}
}

// forwardWinch keeps the remote PTY the same size as the local window.
func (c *Client) forwardWinch(ctx context.Context, a *Attachment) {
	send := func() {
		if cols, rows, ok := localSize(); ok {
			_ = a.Resize(cols, rows)
		}
	}
	send()
	resized := watchResize(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-resized:
			send()
		}
	}
}

// localSize is the local terminal's size, if stdin is one.
func localSize() (cols, rows uint16, ok bool) {
	w, h, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return uint16(w), uint16(h), true
}

// WindowSize reports the local terminal size, falling back to 80x24.
func WindowSize() (cols, rows uint16) {
	if cols, rows, ok := localSize(); ok {
		return cols, rows
	}
	return 80, 24
}

// Watch follows the mesh's changes: fn is called with each Changed the
// daemon sends, the first at once, until ctx ends or the stream breaks,
// whose error Watch returns. A daemon too old to watch fails at once.
func (c *Client) Watch(ctx context.Context, fn func(*protocol.Changed)) error {
	s, err := c.stream(ctx)
	if err != nil {
		return err
	}
	defer s.Hangup()
	stop := context.AfterFunc(ctx, func() { _ = s.Hangup() })
	defer stop()
	if err := s.Send(protocol.TypeWatch, "", &protocol.Watch{}); err != nil {
		return err
	}
	for {
		env, err := s.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		changed, err := transport.Reply[*protocol.Changed](env)
		if err != nil {
			return err
		}
		fn(changed)
	}
}

// Tunnel connects to a TCP port on a node's loopback; see
// protocol.Tunnel. What it returns carries the connection's bytes.
func (c *Client) Tunnel(ctx context.Context, req *protocol.Tunnel) (io.ReadWriteCloser, error) {
	s, err := c.stream(ctx)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.SetReadDeadline(deadline)
	}
	if _, err := transport.Call[*protocol.Tunneled](s, protocol.TypeTunnel, req); err != nil {
		s.Close()
		return nil, err
	}
	_ = s.SetReadDeadline(time.Time{})
	return s, nil
}

// Devices lists the devices attached to a node, or with Mesh to every
// node the daemon reaches.
func (c *Client) Devices(ctx context.Context, req *protocol.ListDevices) (*protocol.Devices, error) {
	return request[*protocol.Devices](ctx, c, protocol.TypeListDevices, req)
}

// Borrow has a node borrow a device, for its programs.
func (c *Client) Borrow(ctx context.Context, req *protocol.Borrow) (*protocol.Lease, error) {
	return request[*protocol.Lease](ctx, c, protocol.TypeBorrow, req)
}

// GiveBack ends a node's lease of a device.
func (c *Client) GiveBack(ctx context.Context, req *protocol.GiveBack) (*protocol.Lease, error) {
	return request[*protocol.Lease](ctx, c, protocol.TypeGiveBack, req)
}

// Leases lists the devices a node borrows.
func (c *Client) Leases(ctx context.Context, req *protocol.ListLeases) (*protocol.Leases, error) {
	return request[*protocol.Leases](ctx, c, protocol.TypeListLeases, req)
}
