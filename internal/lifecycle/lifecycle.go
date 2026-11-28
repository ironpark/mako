// Package lifecycle runs the user's commands on the events of a session's
// life, whatever its agent: when the session starts, when its agent ends a
// turn, and when its agent's session ends.
//
// The hooks come from two files. hooks.json in the configuration
// directory is the user's own. .mako/hooks.toml at a repository's root is
// the repository's: anyone who can commit to the repository, or an agent
// working in it, can write it, so its hooks run only while the user trusts
// the file as it reads (internal/repotrust), checked against the exact
// bytes they are read from.
//
// A hook runs in the session's folder with a timeout, its output kept up
// to a bound, and its processes, children included, end with it.
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ironpark/mako/internal/repotrust"
	"github.com/ironpark/mako/internal/safeexec"
)

// The events a hook runs on.
const (
	SessionStart = "on_session_start" // the session's program started
	TurnDone     = "on_turn_done"     // its agent ended a turn
	SessionEnd   = "on_session_end"   // its agent's session ended
)

// GlobalFile is the file of the configuration directory the user's own
// hooks are kept in, a JSON array of Hook.
const GlobalFile = "hooks.json"

// ProjectFile is where a repository keeps its hooks, from its root, as
// TOML: a [[hook]] table for each.
const ProjectFile = ".mako/hooks.toml"

// ProjectFileIn is the hooks file of the repository whose main work tree
// is repo, named with the separator repo is named with: a front-end names
// a file on a node of another system as that node does.
func ProjectFileIn(repo string) string {
	if strings.Contains(repo, `\`) && !strings.Contains(repo, "/") {
		return strings.TrimRight(repo, `\`) + `\` + strings.ReplaceAll(ProjectFile, "/", `\`)
	}
	return strings.TrimRight(repo, "/") + "/" + ProjectFile
}

// DisableEnv set to 1 in the daemon's environment runs no hook, the
// user's or a repository's, trusted or not.
const DisableEnv = "MAKO_NO_LIFECYCLE_HOOKS"

const (
	// DefaultTimeout is how long a hook runs that names no timeout.
	DefaultTimeout = 30 * time.Second
	// MaxTimeout is the longest a hook runs, whatever it names.
	MaxTimeout = 5 * time.Minute
	// MaxOutput is how much of a hook's output, stdout and stderr
	// together, is kept; the rest is read and dropped.
	MaxOutput = 16 << 10
	// outputGrace is how long a hook's output is still read after its
	// command ended, from a process that left the command's group.
	outputGrace = 2 * time.Second
)

// Hook is a command run on an event of a session's life.
type Hook struct {
	// Name says which hook it is in the daemon's log; "" names it by
	// its event and command.
	Name  string `json:"name,omitempty" toml:"name"`
	Event string `json:"event" toml:"event"`
	// Command is run by sh -c, or cmd /C on Windows.
	Command string `json:"command" toml:"command"`
	// Agents are the agents whose sessions it runs for, by name; empty,
	// or with "*", every agent's.
	Agents []string `json:"agents,omitempty" toml:"agents"`
	// TimeoutSec bounds its run, in seconds: DefaultTimeout when unset,
	// at most MaxTimeout.
	TimeoutSec int `json:"timeout_sec,omitempty" toml:"timeout_sec"`
	// Enabled false keeps the hook without running it; unset, it runs.
	Enabled *bool `json:"enabled,omitempty" toml:"enabled"`
}

// Validate says what is wrong with h, if anything.
func (h Hook) Validate() error {
	switch h.Event {
	case SessionStart, TurnDone, SessionEnd:
	default:
		return fmt.Errorf("unknown event %q, not %s, %s or %s", h.Event, SessionStart, TurnDone, SessionEnd)
	}
	if strings.TrimSpace(h.Command) == "" {
		return errors.New("no command")
	}
	if h.TimeoutSec < 0 {
		return fmt.Errorf("timeout_sec %d is negative", h.TimeoutSec)
	}
	return nil
}

// Timeout is how long h runs before it is killed.
func (h Hook) Timeout() time.Duration {
	switch {
	case h.TimeoutSec <= 0:
		return DefaultTimeout
	// Compared in seconds: so many as to overflow a Duration would read
	// as a negative one.
	case h.TimeoutSec >= int(MaxTimeout/time.Second):
		return MaxTimeout
	}
	return time.Duration(h.TimeoutSec) * time.Second
}

// Label names h in a log.
func (h Hook) Label() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Event + ": " + h.Command
}

// Event is an event of a session's life, as its hooks are told of it.
type Event struct {
	Name      string // SessionStart, TurnDone or SessionEnd
	SessionID string
	Agent     string
	Project   string // the session's folder, where its hooks run
	Branch    string // checked out in Project as the event fired; "" outside a repository
}

// env is what a hook is told of ev, in its environment.
func (ev Event) env() []string {
	return []string{
		"AGENT_MAKO_SESSION=" + ev.SessionID,
		"AGENT_MAKO_AGENT=" + ev.Agent,
		"AGENT_MAKO_PROJECT=" + ev.Project,
		"AGENT_MAKO_BRANCH=" + ev.Branch,
		"AGENT_MAKO_EVENT=" + ev.Name,
	}
}

// For are the hooks of list to run for ev: enabled, of its event, and for
// its agent.
func For(list []Hook, ev Event) []Hook {
	var out []Hook
	for _, h := range list {
		if (h.Enabled != nil && !*h.Enabled) || h.Event != ev.Name {
			continue
		}
		if len(h.Agents) > 0 && !slices.ContainsFunc(h.Agents, func(a string) bool {
			a = strings.TrimSpace(a)
			return a == "*" || strings.EqualFold(a, ev.Agent)
		}) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// LoadGlobal reads the user's hooks from the configuration directory dir:
// none without the file. A file that does not read cleanly, an unknown
// field, a hook wrong in it or anything after its array, gives none of its
// hooks and says why.
func LoadGlobal(dir string) ([]Hook, error) {
	if dir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, GlobalFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var list []Hook
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("read %s: %w", GlobalFile, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read %s: more after the array of hooks", GlobalFile)
	}
	if err := validate(GlobalFile, list); err != nil {
		return nil, err
	}
	return list, nil
}

// Trust says whether the user trusts the file, a slash-separated path from
// the root of the repository at root, as data has it.
type Trust func(root, file string, data []byte) bool

// Project is a repository's hooks file, as LoadProject read it.
type Project struct {
	// File is its absolute path; "" when the repository has none.
	File string
	// Trusted says the user trusts it as it was read. Hooks is empty when
	// it is not: an untrusted file is not parsed.
	Trusted bool
	Hooks   []Hook
}

// LoadProject reads the hooks of the repository whose main work tree is
// repo, if trusted trusts the file as it reads; a nil trusted trusts
// nothing. A file that is trusted but does not read cleanly gives none of
// its hooks and says why.
func LoadProject(repo string, trusted Trust) (Project, error) {
	if repo == "" {
		return Project{}, nil
	}
	// Read as the trust dialog reads it: a regular file, of a bounded
	// size, these very bytes checked and parsed.
	data, err := repotrust.Read(repo, ProjectFile)
	if errors.Is(err, fs.ErrNotExist) {
		return Project{}, nil
	}
	p := Project{File: filepath.Join(repo, filepath.FromSlash(ProjectFile))}
	if err != nil {
		return p, err
	}
	if trusted == nil || !trusted(repo, ProjectFile, data) {
		return p, nil
	}
	p.Trusted = true
	var doc struct {
		Hooks []Hook `toml:"hook"`
	}
	md, err := toml.Decode(string(data), &doc)
	if err != nil {
		return p, fmt.Errorf("parse %s: %w", ProjectFile, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return p, fmt.Errorf("parse %s: unknown field %q", ProjectFile, undec[0].String())
	}
	if err := validate(ProjectFile, doc.Hooks); err != nil {
		return p, err
	}
	p.Hooks = doc.Hooks
	return p, nil
}

// validate says what is wrong with the first wrong hook of list, read from
// file.
func validate(file string, list []Hook) error {
	for i, h := range list {
		if err := h.Validate(); err != nil {
			return fmt.Errorf("%s: hook %d: %w", file, i+1, err)
		}
	}
	return nil
}

// Run runs h for ev in ev's folder, until it exits, its timeout passes or
// ctx ends, and returns its output, cut at MaxOutput. Whatever processes
// of its own the command leaves are killed as it ends; on Windows only the
// command's shell is, and what the shell started runs on (group_windows.go).
//
// What the hook is told of ev reaches it only in its environment, never in
// its command's text. On Windows, cmd reads a variable the command names
// as %NAME% into the command's text before it parses it, so a hook whose
// command names that way a value holding cmd's syntax is not run.
func Run(ctx context.Context, h Hook, ev Event) (string, error) {
	env := ev.env()
	shell := []string{"sh", "-c"}
	if runtime.GOOS == "windows" {
		if name, value := cmdExpandsUnsafe(h.Command, env); name != "" {
			return "", fmt.Errorf("not run: cmd would read %%%s%%, %q, as part of the command; read it from the environment in a program or script the command runs", name, value)
		}
		shell = []string{"cmd", "/C"}
	}
	timeout := h.Timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell[0], shell[1], h.Command)
	cmd.Dir = ev.Project
	cmd.Env = append(os.Environ(), env...)
	ownGroup(cmd)
	// Output goes to a pipe of its own rather than through exec's copying,
	// which would wait for every process holding it: the command's end is
	// its shell's.
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	cmd.Stdout, cmd.Stderr = w, w
	// safeexec checks the folder and the shell before fork(2): a child
	// failing after it crashes a GUI process on macOS.
	err = safeexec.Start(cmd)
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		return "", err
	}
	out := &capped{max: MaxOutput}
	read := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, r)
		close(read)
	}()
	err = cmd.Wait()
	// What it left running ends with it, which closes the output it holds.
	_ = killGroup(cmd)
	// A process that left the group may hold the output still: it is
	// read a moment more, then let go.
	select {
	case <-read:
	case <-time.After(outputGrace):
	}
	_ = r.Close()
	return out.String(), ended(ctx, err, timeout)
}

// ended is how a hook's command ended, err from its Wait, run under ctx
// with timeout: a command that failed as ctx's deadline passed timed out.
// One that exited 0 did not, though the deadline passed while what it
// left was killed or its output read.
func ended(ctx context.Context, err error, timeout time.Duration) error {
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s", timeout)
	}
	return err
}

// cmdSyntax are the characters cmd reads as its own syntax in what it
// expands into a command's text: they join, pipe and redirect commands,
// quote and escape, open and close blocks, and expand again.
const cmdSyntax = "&|<>^\"%!()\r\n"

// cmdExpandsUnsafe is the variable of env, and its value, that command
// names as cmd expands it into the command's text (%NAME%, %NAME:…% or
// !NAME!) while the value holds cmd's syntax; "" when there is none. A
// branch may be named x&calc, and a folder R&D.
func cmdExpandsUnsafe(command string, env []string) (name, value string) {
	upper := strings.ToUpper(command)
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if !strings.ContainsAny(value, cmdSyntax) {
			continue
		}
		name = strings.ToUpper(name)
		for _, ref := range []string{"%" + name + "%", "%" + name + ":", "!" + name + "!"} {
			if strings.Contains(upper, ref) {
				return name, value
			}
		}
	}
	return "", ""
}

// capped keeps the first max bytes written to it, and counts the rest. A
// reader let go may still write to it.
type capped struct {
	mu   sync.Mutex
	buf  []byte
	max  int
	more int
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := min(len(p), c.max-len(c.buf))
	c.buf = append(c.buf, p[:n]...)
	c.more += len(p) - n
	return len(p), nil
}

// String is the output kept, saying how much more there was.
func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := strings.TrimSpace(string(c.buf))
	if c.more > 0 {
		s += fmt.Sprintf("\n[%d more bytes not kept]", c.more)
	}
	return s
}
