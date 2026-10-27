// Package client talks to a mako daemon from a front-end.
package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/creack/pty"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Client is a front-end connection to one daemon, which relays to the rest of
// the mesh on its behalf.
type Client struct {
	conn *quic.Conn
	node *protocol.Node
}

// Dial connects to a daemon. An empty addr means the local daemon.
func Dial(ctx context.Context, addr, psk string) (*Client, error) {
	if addr == "" {
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(config.DefaultPort))
	} else if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, strconv.Itoa(config.DefaultPort))
	}
	conn, err := transport.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn}

	stream, node, err := transport.Handshake(ctx, conn, &protocol.Hello{
		Name:    "mako-cli",
		Version: config.Version,
		AuthTag: psk,
	})
	if err != nil {
		conn.CloseWithError(0, "handshake")
		return nil, err
	}
	stream.Close()
	c.node = node
	return c, nil
}

// Node reports the daemon this client is attached to, a copy of its own.
func (c *Client) Node() *protocol.Node { return proto.CloneOf(c.node) }

// Close hangs up.
func (c *Client) Close() error { return c.conn.CloseWithError(0, "bye") }

// stream opens a greeted request stream.
func (c *Client) stream(ctx context.Context, psk string) (*transport.Stream, error) {
	s, _, err := transport.Handshake(ctx, c.conn, &protocol.Hello{
		Name:    "mako-cli",
		Version: config.Version,
		AuthTag: psk,
	})
	return s, err
}

// Nodes lists the daemons the mesh knows about.
func (c *Client) Nodes(ctx context.Context, psk string) ([]*protocol.Node, error) {
	reply, err := request[*protocol.Nodes](ctx, c, psk, protocol.TypeListNodes, nil)
	if err != nil {
		return nil, err
	}
	return reply.GetNodes(), nil
}

// Sessions lists agent sessions across the mesh.
func (c *Client) Sessions(ctx context.Context, psk string) ([]*protocol.SessionInfo, error) {
	return c.ListSessions(ctx, psk, &protocol.ListSessions{})
}

// ListSessions lists agent sessions across the mesh, saying what the
// asker shows of them.
func (c *Client) ListSessions(ctx context.Context, psk string, req *protocol.ListSessions) ([]*protocol.SessionInfo, error) {
	reply, err := request[*protocol.Sessions](ctx, c, psk, protocol.TypeListSess, req)
	if err != nil {
		return nil, err
	}
	return reply.GetSessions(), nil
}

// Search lists a project's files on a node, or finds a text in them.
func (c *Client) Search(ctx context.Context, psk string, req *protocol.Search) (*protocol.SearchResult, error) {
	return request[*protocol.SearchResult](ctx, c, psk, protocol.TypeSearch, req)
}

// AnswerApproval answers an approval a session's agent waits for.
func (c *Client) AnswerApproval(ctx context.Context, psk string, req *protocol.AnswerApproval) (*protocol.SessionInfo, error) {
	return request[*protocol.SessionInfo](ctx, c, psk, protocol.TypeAnswerApproval, req)
}

// Open starts an agent session, on this daemon or on a peer.
func (c *Client) Open(ctx context.Context, psk string, req *protocol.OpenSession) (*protocol.SessionInfo, error) {
	return request[*protocol.SessionInfo](ctx, c, psk, protocol.TypeOpenSession, req)
}

// request makes one request on a stream of its own, and returns the reply
// as R.
func request[R proto.Message](ctx context.Context, c *Client, psk string, typ protocol.Type, req proto.Message) (R, error) {
	s, err := c.stream(ctx, psk)
	if err != nil {
		var zero R
		return zero, err
	}
	defer s.Close()
	return transport.Call[R](s, typ, req)
}

// ListDir lists a directory on a node.
func (c *Client) ListDir(ctx context.Context, psk string, req *protocol.ListDir) (*protocol.Dir, error) {
	return request[*protocol.Dir](ctx, c, psk, protocol.TypeListDir, req)
}

// ReadFile reads the start of a file on a node.
func (c *Client) ReadFile(ctx context.Context, psk string, req *protocol.ReadFile) (*protocol.File, error) {
	return request[*protocol.File](ctx, c, psk, protocol.TypeReadFile, req)
}

// WriteFile replaces a file's contents on a node.
func (c *Client) WriteFile(ctx context.Context, psk string, req *protocol.WriteFile) (*protocol.File, error) {
	return request[*protocol.File](ctx, c, psk, protocol.TypeWriteFile, req)
}

// StageFile puts a piece of a file on a node, for a session there to read;
// see protocol.StageFile.
func (c *Client) StageFile(ctx context.Context, psk string, req *protocol.StageFile) (*protocol.Staged, error) {
	return request[*protocol.Staged](ctx, c, psk, protocol.TypeStageFile, req)
}

// GitStatus reports the changes of the work tree a directory on a node is in.
func (c *Client) GitStatus(ctx context.Context, psk string, req *protocol.GitStatus) (*protocol.GitChanges, error) {
	return request[*protocol.GitChanges](ctx, c, psk, protocol.TypeGitStatus, req)
}

// GitDiff reads one changed file's hunks on a node.
func (c *Client) GitDiff(ctx context.Context, psk string, req *protocol.GitDiff) (*protocol.Diff, error) {
	return request[*protocol.Diff](ctx, c, psk, protocol.TypeGitDiff, req)
}

// FileOp makes, renames or deletes a file or folder on a node.
func (c *Client) FileOp(ctx context.Context, psk string, req *protocol.FileOp) (*protocol.File, error) {
	return request[*protocol.File](ctx, c, psk, protocol.TypeFileOp, req)
}

// GitLog lists a repository's recent commits on a node.
func (c *Client) GitLog(ctx context.Context, psk string, req *protocol.GitLog) (*protocol.GitHistory, error) {
	return request[*protocol.GitHistory](ctx, c, psk, protocol.TypeGitLog, req)
}

// GitShow reads what one commit changed on a node.
func (c *Client) GitShow(ctx context.Context, psk string, req *protocol.GitShow) (*protocol.GitCommitInfo, error) {
	return request[*protocol.GitCommitInfo](ctx, c, psk, protocol.TypeGitShow, req)
}

// GitRun runs a git operation, a commit or a push and the like, on a node.
func (c *Client) GitRun(ctx context.Context, psk string, req *protocol.GitRun) (*protocol.GitResult, error) {
	return request[*protocol.GitResult](ctx, c, psk, protocol.TypeGitRun, req)
}

// Rename names a session, "" for none, and returns it as it is now.
func (c *Client) Rename(ctx context.Context, psk string, req *protocol.RenameSession) (*protocol.SessionInfo, error) {
	return request[*protocol.SessionInfo](ctx, c, psk, protocol.TypeRenameSess, req)
}

// Kill terminates a session.
func (c *Client) Kill(ctx context.Context, psk string, req *protocol.CloseSession) error {
	_, err := request[*protocol.Exited](ctx, c, psk, protocol.TypeCloseSess, req)
	return err
}

// Attach connects the local terminal to a session until it ends or the user
// detaches. The terminal is put in raw mode for the duration.
func (c *Client) Attach(ctx context.Context, psk string, req *protocol.Attach) error {
	a, err := c.AttachStream(ctx, psk, req)
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
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)

	send := func() {
		size, err := pty.GetsizeFull(os.Stdin)
		if err != nil {
			return
		}
		_ = a.Resize(size.Cols, size.Rows)
	}
	send()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			send()
		}
	}
}

// WindowSize reports the local terminal size, falling back to 80x24.
func WindowSize() (cols, rows uint16) {
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil || size.Cols == 0 {
		return 80, 24
	}
	return size.Cols, size.Rows
}

// Watch follows the mesh's changes: fn is called with each Changed the
// daemon sends, the first at once, until ctx ends or the stream breaks,
// whose error Watch returns. A daemon too old to watch fails at once.
func (c *Client) Watch(ctx context.Context, psk string, fn func(*protocol.Changed)) error {
	s, err := c.stream(ctx, psk)
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
