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
	s, err := c.stream(ctx, psk)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var reply protocol.Sessions
	if err := s.Request(protocol.TypeListSess, struct{}{}, &reply); err != nil {
		return nil, err
	}
	return reply.Sessions, nil
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
	s, err := c.stream(ctx, psk)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Send(protocol.TypeAttach, "", req); err != nil {
		return err
	}

	restore, err := makeRaw(os.Stdin)
	if err != nil {
		return err
	}
	defer restore()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.forwardWinch(ctx, s, req.SessionID)
	go c.forwardInput(ctx, s, req.SessionID, cancel)

	for {
		env, err := s.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch env.Type {
		case protocol.TypeSnapshot:
			var snap protocol.Snapshot
			if err := protocol.Decode(env, &snap); err != nil {
				return err
			}
			fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H"+snap.Text)
		case protocol.TypeOutput:
			var out protocol.Output
			if err := protocol.Decode(env, &out); err != nil {
				return err
			}
			if _, err := os.Stdout.Write(out.Data); err != nil {
				return err
			}
		case protocol.TypeExited:
			var exited protocol.Exited
			_ = protocol.Decode(env, &exited)
			fmt.Fprintf(os.Stderr, "\r\n[sanbo] session %s exited (%d)\r\n", exited.SessionID, exited.ExitCode)
			return nil
		case protocol.TypeError:
			var e protocol.Error
			_ = protocol.Decode(env, &e)
			return fmt.Errorf("remote: %s", e.Message)
		}
	}
}

func (c *Client) forwardInput(ctx context.Context, s *transport.Stream, sessionID string, cancel func()) {
	defer cancel()
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := s.Send(protocol.TypeInput, "", protocol.Input{SessionID: sessionID, Data: chunk}); err != nil {
				return
			}
		}
		if err != nil || ctx.Err() != nil {
			return
		}
	}
}

// forwardWinch keeps the remote PTY the same size as the local window.
func (c *Client) forwardWinch(ctx context.Context, s *transport.Stream, sessionID string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)

	send := func() {
		size, err := pty.GetsizeFull(os.Stdin)
		if err != nil {
			return
		}
		_ = s.Send(protocol.TypeResize, "", protocol.Resize{
			SessionID: sessionID,
			Cols:      size.Cols,
			Rows:      size.Rows,
		})
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
