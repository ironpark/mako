// Package client talks to a sanbo daemon from a front-end.
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

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// Client is a front-end connection to one daemon, which relays to the rest of
// the mesh on its behalf.
type Client struct {
	conn *quic.Conn
	node protocol.Node
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

	stream, node, err := transport.Handshake(ctx, conn, protocol.Hello{
		Name:    "sanbo-cli",
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

// Node reports the daemon this client is attached to.
func (c *Client) Node() protocol.Node { return c.node }

// Close hangs up.
func (c *Client) Close() error { return c.conn.CloseWithError(0, "bye") }

// stream opens a greeted request stream.
func (c *Client) stream(ctx context.Context, psk string) (*transport.Stream, error) {
	s, _, err := transport.Handshake(ctx, c.conn, protocol.Hello{
		Name:    "sanbo-cli",
		Version: config.Version,
		AuthTag: psk,
	})
	return s, err
}

// Nodes lists the daemons the mesh knows about.
func (c *Client) Nodes(ctx context.Context, psk string) ([]protocol.Node, error) {
	s, err := c.stream(ctx, psk)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var reply protocol.Nodes
	if err := s.Request(protocol.TypeListNodes, struct{}{}, &reply); err != nil {
		return nil, err
	}
	return reply.Nodes, nil
}

// Sessions lists agent sessions across the mesh.
func (c *Client) Sessions(ctx context.Context, psk string) ([]protocol.SessionInfo, error) {
	return c.ListSessions(ctx, psk, protocol.ListSessions{})
}

// ListSessions lists agent sessions across the mesh, saying what the
// asker shows of them.
func (c *Client) ListSessions(ctx context.Context, psk string, req protocol.ListSessions) ([]protocol.SessionInfo, error) {
	var reply protocol.Sessions
	if err := c.request(ctx, psk, protocol.TypeListSess, req, &reply); err != nil {
		return nil, err
	}
	return reply.Sessions, nil
}

// Search lists a project's files on a node, or finds a text in them.
func (c *Client) Search(ctx context.Context, psk string, req protocol.Search) (protocol.SearchResult, error) {
	var out protocol.SearchResult
	err := c.request(ctx, psk, protocol.TypeSearch, req, &out)
	return out, err
}

// AnswerApproval answers an approval a session's agent waits for.
func (c *Client) AnswerApproval(ctx context.Context, psk string, req protocol.AnswerApproval) (protocol.SessionInfo, error) {
	var out protocol.SessionInfo
	err := c.request(ctx, psk, protocol.TypeAnswerApproval, req, &out)
	return out, err
}

// Open starts an agent session, on this daemon or on a peer.
func (c *Client) Open(ctx context.Context, psk string, req protocol.OpenSession) (protocol.SessionInfo, error) {
	s, err := c.stream(ctx, psk)
	if err != nil {
		return protocol.SessionInfo{}, err
	}
	defer s.Close()
	var info protocol.SessionInfo
	if err := s.Request(protocol.TypeOpenSession, req, &info); err != nil {
		return protocol.SessionInfo{}, err
	}
	return info, nil
}

// request makes one request on a stream of its own.
func (c *Client) request(ctx context.Context, psk string, typ protocol.Type, req, reply any) error {
	s, err := c.stream(ctx, psk)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Request(typ, req, reply)
}

// ListDir lists a directory on a node.
func (c *Client) ListDir(ctx context.Context, psk string, req protocol.ListDir) (protocol.Dir, error) {
	var dir protocol.Dir
	err := c.request(ctx, psk, protocol.TypeListDir, req, &dir)
	return dir, err
}

// ReadFile reads the start of a file on a node.
func (c *Client) ReadFile(ctx context.Context, psk string, req protocol.ReadFile) (protocol.File, error) {
	var f protocol.File
	err := c.request(ctx, psk, protocol.TypeReadFile, req, &f)
	return f, err
}

// WriteFile replaces a file's contents on a node.
func (c *Client) WriteFile(ctx context.Context, psk string, req protocol.WriteFile) (protocol.File, error) {
	var f protocol.File
	err := c.request(ctx, psk, protocol.TypeWriteFile, req, &f)
	return f, err
}

// GitStatus reports the changes of the work tree a directory on a node is in.
func (c *Client) GitStatus(ctx context.Context, psk string, req protocol.GitStatus) (protocol.GitChanges, error) {
	var out protocol.GitChanges
	err := c.request(ctx, psk, protocol.TypeGitStatus, req, &out)
	return out, err
}

// GitDiff reads one changed file's hunks on a node.
func (c *Client) GitDiff(ctx context.Context, psk string, req protocol.GitDiff) (protocol.Diff, error) {
	var out protocol.Diff
	err := c.request(ctx, psk, protocol.TypeGitDiff, req, &out)
	return out, err
}

// FileOp makes, renames or deletes a file or folder on a node.
func (c *Client) FileOp(ctx context.Context, psk string, req protocol.FileOp) (protocol.File, error) {
	var out protocol.File
	err := c.request(ctx, psk, protocol.TypeFileOp, req, &out)
	return out, err
}

// GitLog lists a repository's recent commits on a node.
func (c *Client) GitLog(ctx context.Context, psk string, req protocol.GitLog) (protocol.GitHistory, error) {
	var out protocol.GitHistory
	err := c.request(ctx, psk, protocol.TypeGitLog, req, &out)
	return out, err
}

// GitShow reads what one commit changed on a node.
func (c *Client) GitShow(ctx context.Context, psk string, req protocol.GitShow) (protocol.GitCommitInfo, error) {
	var out protocol.GitCommitInfo
	err := c.request(ctx, psk, protocol.TypeGitShow, req, &out)
	return out, err
}

// GitRun runs a git operation, a commit or a push and the like, on a node.
func (c *Client) GitRun(ctx context.Context, psk string, req protocol.GitRun) (protocol.GitResult, error) {
	var out protocol.GitResult
	err := c.request(ctx, psk, protocol.TypeGitRun, req, &out)
	return out, err
}

// Rename names a session, "" for none, and returns it as it is now.
func (c *Client) Rename(ctx context.Context, psk string, req protocol.RenameSession) (protocol.SessionInfo, error) {
	var out protocol.SessionInfo
	err := c.request(ctx, psk, protocol.TypeRenameSess, req, &out)
	return out, err
}

// Kill terminates a session.
func (c *Client) Kill(ctx context.Context, psk string, req protocol.CloseSession) error {
	s, err := c.stream(ctx, psk)
	if err != nil {
		return err
	}
	defer s.Close()
	var exited protocol.Exited
	return s.Request(protocol.TypeCloseSess, req, &exited)
}

// Attach connects the local terminal to a session until it ends or the user
// detaches. The terminal is put in raw mode for the duration.
func (c *Client) Attach(ctx context.Context, psk string, req protocol.Attach) error {
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
		case protocol.FrameSnapshot:
			fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
		case protocol.FrameOutput:
			if _, err := os.Stdout.Write(ev.Data); err != nil {
				return err
			}
		case protocol.FrameExited:
			fmt.Fprintf(os.Stderr, "\r\n[sanbo] session %s exited (%d)\r\n", req.SessionID, ev.ExitCode)
			return nil
		case protocol.FrameError:
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
