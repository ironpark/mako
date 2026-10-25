// Package protocol defines the wire format spoken between sanbo front-ends,
// daemons, and peer daemons. Every message is a length-prefixed JSON frame on a
// QUIC stream.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

// ALPN is the QUIC application protocol identifier for sanbo.
const ALPN = "sanbo/1"

// MaxFrameSize caps a single frame so a peer cannot force an allocation storm.
const MaxFrameSize = 8 << 20

// Type identifies the payload carried by an Envelope.
type Type string

const (
	TypeHello       Type = "hello"
	TypeWelcome     Type = "welcome"
	TypeError       Type = "error"
	TypeListNodes   Type = "list_nodes"
	TypeNodes       Type = "nodes"
	TypeListSess    Type = "list_sessions"
	TypeSessions    Type = "sessions"
	TypeOpenSession Type = "open_session"
	TypeSessionInfo Type = "session_info"
	TypeCloseSess   Type = "close_session"
	TypeRenameSess  Type = "rename_session"
	TypeAttach      Type = "attach"
	TypeInput       Type = "input"
	TypeOutput      Type = "output"
	TypeResize      Type = "resize"
	TypeSnapshot    Type = "snapshot"
	TypeExited      Type = "exited"
	TypeGossip      Type = "gossip"
	TypeListDir     Type = "list_dir"
	TypeDir         Type = "dir"
	TypeReadFile    Type = "read_file"
	TypeFile        Type = "file"
	TypeWriteFile   Type = "write_file"
	TypeGitStatus   Type = "git_status"
	TypeGitChanges  Type = "git_changes"
	TypeGitDiff     Type = "git_diff"
	TypeDiff        Type = "diff"
)

// Envelope wraps every message. Payload is the JSON body for Type.
type Envelope struct {
	Type    Type            `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Hello opens a connection and states who is calling.
type Hello struct {
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	IsPeer  bool   `json:"is_peer"`
	AuthTag string `json:"auth_tag,omitempty"`
}

// Welcome answers a Hello.
type Welcome struct {
	Node Node `json:"node"`
}

// Error reports a failed request.
type Error struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// Node describes a daemon reachable on the mesh.
type Node struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Addr     string    `json:"addr"`
	OS       string    `json:"os"`
	Arch     string    `json:"arch"`
	Version  string    `json:"version"`
	Source   string    `json:"source,omitempty"` // mdns, tailscale, static, inbound, gossip, self
	LastSeen time.Time `json:"last_seen"`
	// Features are protocol extensions this daemon speaks, so a peer can use
	// them only where they are understood.
	Features []string `json:"features,omitempty"`
	// Agents are what the node can launch, with their profiles' names; its
	// flags and environment stay on the node.
	Agents []AgentInfo `json:"agents,omitempty"`
}

// FeatureAttachBinary is a daemon that frames attach streams as
// protocol.StreamFrame after the Attach request.
const FeatureAttachBinary = "attach_binary"

// HasFeature reports whether a node speaks a protocol extension.
func (n Node) HasFeature(feature string) bool {
	return slices.Contains(n.Features, feature)
}

// AgentInfo names an agent a node can launch and its profiles.
type AgentInfo struct {
	Name     string   `json:"name"`
	Profiles []string `json:"profiles,omitempty"`
}

// Nodes is the reply to ListNodes, and the body of a Gossip push.
type Nodes struct {
	Nodes []Node `json:"nodes"`
}

// OpenSession asks a daemon to start an agent session.
type OpenSession struct {
	NodeID  string            `json:"node_id,omitempty"` // empty means the local daemon
	Agent   string            `json:"agent"`             // codex, claude, agy, shell...
	Profile string            `json:"profile,omitempty"` // one of the agent's profiles on that node
	Project string            `json:"project"`           // working directory
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cols    uint16            `json:"cols"`
	Rows    uint16            `json:"rows"`
}

// SessionState is the lifecycle stage of a session.
type SessionState string

const (
	StateStopped  SessionState = "stopped" // restored metadata; starts on attach
	StateStarting SessionState = "starting"
	StateRunning  SessionState = "running"
	StateExited   SessionState = "exited"
)

// SessionInfo describes one agent session.
type SessionInfo struct {
	ID             string `json:"id"`
	NodeID         string `json:"node_id"`
	Agent          string `json:"agent"`
	Profile        string `json:"profile,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"` // native agent conversation, stable across restarts
	// Name is what the user called the session; empty, it goes by its
	// program's title or its agent.
	Name      string       `json:"name,omitempty"`
	Title     string       `json:"title,omitempty"` // latest program-set terminal title
	Project   string       `json:"project"`
	Command   []string     `json:"command"`
	State     SessionState `json:"state"`
	Cols      uint16       `json:"cols"`
	Rows      uint16       `json:"rows"`
	StartedAt time.Time    `json:"started_at"`
	ExitCode  *int         `json:"exit_code,omitempty"`
	Busy      bool         `json:"busy,omitempty"` // printing on its own, as an agent at work does
}

// Sessions is the reply to ListSessions.
type Sessions struct {
	Sessions []SessionInfo `json:"sessions"`
}

// Attach binds the current stream to a session's I/O.
type Attach struct {
	SessionID string `json:"session_id"`
	NodeID    string `json:"node_id,omitempty"`
	// Replay asks the daemon to send the current screen before live output.
	Replay bool `json:"replay"`
	// Binary asks for framed binary messages after this request instead of
	// JSON envelopes. Output is the hot path, and carrying it as base64
	// inside JSON costs a third more bytes and a decode per frame.
	Binary bool `json:"binary,omitempty"`
}

// Input carries keystrokes toward the session PTY.
type Input struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}

// Output carries PTY bytes toward the front-end.
type Output struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}

// Resize changes the session window size.
type Resize struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

// ProgressReport carries a terminal's latest OSC 9;4 state. RemainingMS is
// relative so peers do not need synchronized clocks for report expiry.
type ProgressReport struct {
	State       uint8 `json:"state"`
	Value       int16 `json:"value"`
	RemainingMS int64 `json:"remaining_ms"`
}

// Snapshot is a rendered view of the session screen, used for replay and for
// front-ends that only want the current state rather than a live stream.
type Snapshot struct {
	Progress  *ProgressReport `json:"progress,omitempty"`
	SessionID string          `json:"session_id"`
	Cols      uint16          `json:"cols"`
	Rows      uint16          `json:"rows"`
	Title     string          `json:"title,omitempty"`
	// Text is the screen contents; VT when Styled, plain text otherwise.
	Text   string `json:"text"`
	Styled bool   `json:"styled"`
}

// CloseSession terminates a session.
type CloseSession struct {
	SessionID string `json:"session_id"`
	NodeID    string `json:"node_id,omitempty"`
}

// RenameSession names a session; an empty Name gives its own back. The
// reply is its SessionInfo.
type RenameSession struct {
	SessionID string `json:"session_id"`
	NodeID    string `json:"node_id,omitempty"`
	Name      string `json:"name"`
}

// Exited reports session termination.
type Exited struct {
	SessionID string `json:"session_id"`
	ExitCode  int    `json:"exit_code"`
	Err       string `json:"err,omitempty"`
}

// Encode marshals payload into an Envelope of the given type.
func Encode(t Type, id string, payload any) (*Envelope, error) {
	env := &Envelope{Type: t, ID: id}
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", t, err)
		}
		env.Payload = body
	}
	return env, nil
}

// Decode unmarshals an envelope payload into out.
func Decode(env *Envelope, out any) error {
	if len(env.Payload) == 0 {
		return errors.New("empty payload")
	}
	return json.Unmarshal(env.Payload, out)
}

// WriteFrame writes one length-prefixed JSON frame.
func WriteFrame(w io.Writer, env *Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if len(body) > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", len(body))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// ReadFrame reads one length-prefixed JSON frame.
func ReadFrame(r io.Reader) (*Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// CodedError is a failure a requester may act on by its Code, which
// crosses the wire in Error.Code: from a handler's error to the requester,
// through any daemon relaying it.
type CodedError struct {
	Code    string
	Message string
}

func (e *CodedError) Error() string { return e.Message }

// HasCode reports whether err is a CodedError with code.
func HasCode(err error, code string) bool {
	var ce *CodedError
	return errors.As(err, &ce) && ce.Code == code
}

// WriteError is a shorthand for sending a TypeError frame.
func WriteError(w io.Writer, id string, err error) error {
	e := Error{Message: err.Error()}
	var ce *CodedError
	if errors.As(err, &ce) {
		e.Code = ce.Code
	}
	env, encErr := Encode(TypeError, id, e)
	if encErr != nil {
		return encErr
	}
	return WriteFrame(w, env)
}

// ListDir asks a node for a directory's entries.
type ListDir struct {
	NodeID string `json:"node_id,omitempty"` // empty means the local daemon
	Path   string `json:"path"`              // absolute
}

// DirEntry is one entry of a directory.
type DirEntry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir,omitempty"` // a directory, or a link to one
	Link    bool      `json:"link,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Ignored bool      `json:"ignored,omitempty"` // left out of the repository by .gitignore
}

// Dir is the reply to ListDir: the entries, directories first, then by name.
type Dir struct {
	Path    string     `json:"path"`
	Entries []DirEntry `json:"entries"`
}

// ReadFile asks a node for the start of a file.
type ReadFile struct {
	NodeID string `json:"node_id,omitempty"`
	Path   string `json:"path"`
	Max    int    `json:"max,omitempty"` // bytes; capped by the daemon
	// Image reads the file whole, as the bytes of an image to show, binary
	// or not, unless it is larger than a frame carries; then Truncated is
	// set and Data left out.
	Image bool `json:"image,omitempty"`
}

// File is the reply to ReadFile.
type File struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Data      []byte `json:"data,omitempty"`
	Truncated bool   `json:"truncated,omitempty"` // Data is only the start
	Binary    bool   `json:"binary,omitempty"`    // not text; Data is omitted
	// ModTime is when the file last changed, which a WriteFile hands back
	// so the write fails if it changed again since.
	ModTime time.Time `json:"mod_time"`
}

// WriteFile asks a node to replace a file's contents, keeping its mode. The
// reply is a File with the new size and ModTime and no Data.
type WriteFile struct {
	NodeID string `json:"node_id,omitempty"`
	Path   string `json:"path"` // absolute
	Data   []byte `json:"data"`
	// ModTime, unless zero, is when the file changed as the writer last
	// read it: the write fails with CodeChanged if it has changed since.
	ModTime time.Time `json:"mod_time"`
}

// CodeChanged is the Error code of a WriteFile refused because the file
// changed since it was read.
const CodeChanged = "changed"

// GitStatus asks a node for the changes in the repository a directory is in.
type GitStatus struct {
	NodeID string `json:"node_id,omitempty"`
	Path   string `json:"path"` // absolute, anywhere inside the work tree
}

// Change kinds of a GitFile.
const (
	GitAdded    = "added" // new to the index or untracked
	GitModified = "modified"
	GitDeleted  = "deleted"
	GitRenamed  = "renamed"
)

// GitChanges is the reply to GitStatus: the work tree's changes against
// HEAD, staged or not, with their lines added and removed.
type GitChanges struct {
	Root    string    `json:"root"`               // the work tree, absolute
	Branch  string    `json:"branch,omitempty"`   // or the commit, detached
	NotRepo bool      `json:"not_repo,omitempty"` // Path is in no repository
	Files   []GitFile `json:"files"`
}

// GitFile is one changed file.
type GitFile struct {
	Path    string `json:"path"` // slash-separated, from Root
	From    string `json:"from,omitempty"`
	Status  string `json:"status"` // a Git* change kind
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"` // no lines to count
}

// GitDiff asks a node for one file's changes against HEAD.
type GitDiff struct {
	NodeID string `json:"node_id,omitempty"`
	Root   string `json:"root"`
	Path   string `json:"path"`           // as GitFile.Path
	From   string `json:"from,omitempty"` // where HEAD has a renamed file
}

// Diff is the reply to GitDiff: the file's hunks, as unified diff lines.
type Diff struct {
	Path      string     `json:"path"`
	Binary    bool       `json:"binary,omitempty"`
	Lines     []DiffLine `json:"lines,omitempty"`
	Truncated bool       `json:"truncated,omitempty"` // only the first hunks
}

// DiffLine is one line of a diff. Op is '+' added, '-' removed, ' ' kept
// around a change, or '@' a hunk's header; Old and New are its line
// numbers on each side, 0 where it has none.
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
}
