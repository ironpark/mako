package gui

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
)

// Files dropped on a session from the desktop are handed to it by path.
// A session on this machine reads the dropped file where it is; one on
// another node cannot, so the file is sent there first, in pieces, and the
// session is given the path of the copy its node keeps.

// stageChunk is how much of a file one piece carries, well inside a frame.
const stageChunk = 4 << 20

// stageTimeout bounds sending one file.
const stageTimeout = 5 * time.Minute

// onThisMachine reports whether node runs on the machine the window does,
// and so reads the same disk: the window's own daemon, reached at a
// loopback address.
func (m *model) onThisMachine(node string) bool {
	if node != "" && node != ggui.Peek(m.Conn).Self.GetId() {
		return false
	}
	addr := m.daemonAddr()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return host == "" || host == "localhost" || ip != nil && ip.IsLoopback()
}

// handOver gives session id the files at paths on this machine: the paths
// themselves for a session here, or for one on another node, the paths of
// copies sent there. done gets them on the UI goroutine, those that could
// be sent, and a toast says what could not.
func (m *model) handOver(id string, paths []string, done func([]string)) {
	s, ok := m.session(id)
	if !ok || len(paths) == 0 {
		return
	}
	node := s.GetNodeId()
	if m.onThisMachine(node) {
		done(paths)
		return
	}
	where := nodeName(ggui.Peek(m.Nodes), node)
	sending := m.toaster.Push(ui.Toast(Tf("Sending to %s…", where), filesLabel(paths)).Duration(0))
	go func() {
		var sent []string
		var failed error
		for _, p := range paths {
			remote, err := m.stage(node, p)
			if err != nil {
				failed = errors.Join(failed, err)
				continue
			}
			sent = append(sent, remote)
		}
		m.post(func() {
			m.toaster.Dismiss(sending)
			if failed != nil {
				m.toaster.Push(ui.Toast(Tf("Could not send to %s", where), failed.Error()).Destructive())
			}
			if len(sent) > 0 {
				done(sent)
			}
		})
	}()
}

// filesLabel names the files dropped: the one, or how many.
func filesLabel(paths []string) string {
	if len(paths) == 1 {
		return filepath.Base(paths[0])
	}
	return Tf("%d files", len(paths))
}

// stage sends the file at path to node, a piece at a time, and returns
// where the node keeps it.
func (m *model) stage(node, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New(Tf("%s is a folder; only files can be sent", info.Name()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	c, err := m.client(ctx)
	if err != nil {
		return "", err
	}
	return stageFile(ctx, c, m.key(), m.remoteID(node), info.Name(), f)
}

// stageFile sends what r holds to node as a staged file named name.
func stageFile(ctx context.Context, c *client.Client, psk, node, name string, r io.Reader) (string, error) {
	buf := make([]byte, stageChunk)
	var path string
	var off int64
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 || path == "" {
			st, serr := c.StageFile(ctx, psk, &protocol.StageFile{NodeId: node, Name: name, Path: path, Offset: off, Data: buf[:n]})
			if serr != nil {
				return "", nodeErrOf(serr)
			}
			path = st.GetPath()
			off += int64(n)
		}
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return path, nil
		case err != nil:
			return "", err
		}
	}
}

// nodeErrOf is err as nodeErr words it, as an error.
func nodeErrOf(err error) error { return errors.New(nodeErr(err)) }
