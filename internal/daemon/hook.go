package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ironpark/sanbo/internal/agentspec"
)

// hookArg, as a program's first argument, runs it as an agent's hook.
const hookArg = "hook"

// hookProgram is this program, once HookMain has said it can be a hook.
var hookProgram string

// HookMain runs this program as an agent's hook when it was started as one,
// reporting true: main then returns. Called first in main, it also says the
// program can be one, so the daemons it runs give their agents a hook;
// without it they only watch the agents' open files.
func HookMain() bool {
	if len(os.Args) > 1 && os.Args[1] == hookArg {
		runHook(os.Args[2:], os.Stdin)
		return true
	}
	if exe, err := os.Executable(); err == nil {
		hookProgram = exe
	}
	return false
}

// hookLimit bounds what one hook may send.
const hookLimit = 1 << 20

// hookMessage is what a hook passes on: the session it runs in, when it ran,
// and the agent's own input to it.
type hookMessage struct {
	Session string          `json:"session"`
	Seq     int64           `json:"seq"`
	Input   json.RawMessage `json:"input"`
}

// runHook is an agent's SessionStart hook, run by the daemon's own program
// with the daemon's hook socket as args[0]. It passes the agent's input on
// for the session in SANBO_SESSION. The agent waits on it, so it gives up
// quickly, and it never fails: the daemon falls back to watching the
// agent's open files.
func runHook(args []string, stdin io.Reader) {
	seq := time.Now().UnixNano() // when the agent ran it, not when it arrives
	session := os.Getenv("SANBO_SESSION")
	if len(args) != 1 || session == "" {
		return
	}
	input, err := io.ReadAll(io.LimitReader(stdin, hookLimit))
	if err != nil || !json.Valid(input) {
		return
	}
	conn, err := net.DialTimeout("unix", args[0], time.Second)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = json.NewEncoder(conn).Encode(hookMessage{Session: session, Seq: seq, Input: input})
}

// listenHooks serves the hook socket in dir until ctx ends, setting
// hookCommand once it listens; Run calls it before it serves any session.
// The socket sits in the private run directory, so only this user's
// processes reach it.
func (d *Daemon) listenHooks(ctx context.Context, dir string) {
	if d.hookBinary == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.log.Warn("hooks: no run directory", "err", err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("hooks-%d.sock", d.port))
	_ = os.Remove(path) // left by a daemon that did not stop cleanly
	ln, err := net.Listen("unix", path)
	if err != nil {
		d.log.Warn("hooks: not listening", "err", err)
		return
	}
	context.AfterFunc(ctx, func() { _ = ln.Close() })
	d.hookCommand = agentspec.JoinArgs([]string{d.hookBinary, hookArg, path})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serveHook(conn)
		}
	}()
}

func (d *Daemon) serveHook(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	var msg hookMessage
	if err := json.NewDecoder(io.LimitReader(conn, hookLimit+4096)).Decode(&msg); err != nil {
		return
	}
	if err := d.reportConversation(msg); err != nil {
		d.log.Warn("hooks: report dropped", "session", msg.Session, "err", err)
	}
}
