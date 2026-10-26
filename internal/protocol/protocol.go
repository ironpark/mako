// Package protocol defines the messages spoken between sanbo front-ends,
// daemons, and peer daemons. On the wire every message is a length-prefixed
// protobuf Envelope on a QUIC stream, as proto/sanbo/v2 defines it; the rest
// of sanbo uses the Go types here, which convert to and from it by name.
// See docs/protocol.md.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/ironpark/sanbo/internal/protocol/pb/sanbo/v2"
)

// ALPN is the QUIC application protocol identifier for sanbo: sanbo/2 is
// the protobuf framing, which a sanbo/1 (JSON) peer cannot speak.
const ALPN = "sanbo/2"

// MaxFrameSize caps a single frame so a peer cannot force an allocation storm.
const MaxFrameSize = 8 << 20

// Type identifies the payload carried by an Envelope.
type Type string

const (
	TypeHello         Type = "hello"
	TypeWelcome       Type = "welcome"
	TypeError         Type = "error"
	TypeListNodes     Type = "list_nodes"
	TypeNodes         Type = "nodes"
	TypeListSess      Type = "list_sessions"
	TypeSessions      Type = "sessions"
	TypeOpenSession   Type = "open_session"
	TypeSessionInfo   Type = "session_info"
	TypeCloseSess     Type = "close_session"
	TypeRenameSess    Type = "rename_session"
	TypeAttach        Type = "attach"
	TypeInput         Type = "input"
	TypeOutput        Type = "output"
	TypeResize        Type = "resize"
	TypeSnapshot      Type = "snapshot"
	TypeExited        Type = "exited"
	TypeGossip        Type = "gossip"
	TypeListDir       Type = "list_dir"
	TypeDir           Type = "dir"
	TypeReadFile      Type = "read_file"
	TypeFile          Type = "file"
	TypeWriteFile     Type = "write_file"
	TypeGitStatus     Type = "git_status"
	TypeGitChanges    Type = "git_changes"
	TypeGitDiff       Type = "git_diff"
	TypeDiff          Type = "diff"
	TypeFileOp        Type = "file_op"
	TypeGitLog        Type = "git_log"
	TypeGitHistory    Type = "git_history"
	TypeGitShow       Type = "git_show"
	TypeGitCommitInfo Type = "git_commit_info"
	TypeGitRun        Type = "git_run"
	TypeGitResult     Type = "git_result"
)

// Envelope is one frame: a message of a Type, and the ID a reply carries
// back. Encode makes one, and Decode reads its message into a Go type.
type Envelope struct {
	Type Type
	ID   string
	msg  *pb.Envelope
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
	// Revision is the protocol.Revision the daemon was built with; 0 from
	// one older than revisions.
	Revision int `json:"revision,omitempty"`
}

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
	// Worktree, when set, starts the session in a new git worktree of the
	// repository Project is in, on a new branch.
	Worktree *Worktree `json:"worktree,omitempty"`
}

// Worktree is a git worktree made for one session: Path, on the new
// Branch, started from Base in the repository at Repo.
type Worktree struct {
	Repo   string `json:"repo"`           // the main work tree
	Path   string `json:"path,omitempty"` // the session's, once made
	Branch string `json:"branch"`         // made for the session
	Base   string `json:"base,omitempty"` // the branch or commit it started from; "" for HEAD
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
	// Idle is set while nothing runs in front of the session's program, as
	// a shell at its prompt: closing it loses no work.
	Idle bool `json:"idle,omitempty"`
	// Branch is the git branch checked out in the project, "" outside a
	// repository.
	Branch string `json:"branch,omitempty"`
	// Repo is the main work tree of the repository the project is in, the
	// same for its subfolders and its linked work trees; "" outside one.
	Repo string `json:"repo,omitempty"`
	// Status is what the agent's hooks last reported, at StatusAt; "" when
	// its agent has no hooks.
	Status   AgentStatus `json:"status,omitempty"`
	StatusAt time.Time   `json:"status_at,omitempty"`
	// LastOutput is when the program last printed.
	LastOutput time.Time `json:"last_output,omitempty"`
	// Approval is the tool call the agent waits for the user to approve.
	Approval *Approval `json:"approval,omitempty"`
	// Worktree is the session's own git worktree, made for it on Base.
	Worktree *Worktree `json:"worktree,omitempty"`
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

// body is the Envelope's oneof, whose field names are the Types, and
// bodyFields its fields by the Type they carry.
var (
	body       = (&pb.Envelope{}).ProtoReflect().Descriptor().Oneofs().ByName("body")
	bodyFields = func() map[Type]protoreflect.FieldDescriptor {
		out := make(map[Type]protoreflect.FieldDescriptor, body.Fields().Len())
		for i := range body.Fields().Len() {
			fd := body.Fields().Get(i)
			out[Type(fd.Name())] = fd
		}
		return out
	}()
)

// envelopeOf is msg as an Envelope: its ID, and its Type by the body set,
// empty for a body this build does not know.
func envelopeOf(msg *pb.Envelope) *Envelope {
	env := &Envelope{ID: msg.GetId(), msg: msg}
	if fd := msg.ProtoReflect().WhichOneof(body); fd != nil {
		env.Type = Type(fd.Name())
	}
	return env
}

// Encode makes an Envelope of type t carrying payload, a struct (or a
// pointer to one) whose fields the type's message has; nil sends the
// message empty.
func Encode(t Type, id string, payload any) (*Envelope, error) {
	fd, ok := bodyFields[t]
	if !ok {
		return nil, fmt.Errorf("protocol: no message type %q", t)
	}
	msg := &pb.Envelope{Id: id}
	m := msg.ProtoReflect()
	b := m.NewField(fd)
	if payload != nil {
		if err := toProto(reflect.ValueOf(payload), b.Message()); err != nil {
			return nil, fmt.Errorf("encode %s: %w", t, err)
		}
	}
	m.Set(fd, b)
	return envelopeOf(msg), nil
}

// Decode reads an envelope's message into out, a pointer to a struct whose
// fields the message has.
func Decode(env *Envelope, out any) error {
	fd, ok := bodyFields[env.Type]
	if !ok || env.msg == nil {
		return fmt.Errorf("protocol: no %q message to decode", env.Type)
	}
	if err := fromProto(env.msg.ProtoReflect().Get(fd).Message(), reflect.ValueOf(out)); err != nil {
		return fmt.Errorf("decode %s: %w", env.Type, err)
	}
	return nil
}

// WriteFrame writes one frame: the envelope's length, as a big-endian
// uint32, then the envelope.
func WriteFrame(w io.Writer, env *Envelope) error {
	size := proto.Size(env.msg)
	if size > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", size)
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+size), uint32(size))
	// The size was just computed, so marshalling need not again.
	frame, err := proto.MarshalOptions{UseCachedSize: true}.MarshalAppend(frame, env.msg)
	if err != nil {
		return err
	}
	_, err = w.Write(frame)
	return err
}

// ReadFrame reads one frame. An envelope whose body this build does not
// know, from a newer peer, comes back with an empty Type, which a daemon
// answers as unsupported.
func ReadFrame(r io.Reader) (*Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	msg := &pb.Envelope{}
	if err := proto.Unmarshal(buf, msg); err != nil {
		return nil, err
	}
	return envelopeOf(msg), nil
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

// CodeUnsupported is the Error code of a request its daemon does not know,
// being older than the requester.
const CodeUnsupported = "unsupported"

// FileOp asks a node to make, rename or delete a file or folder. The reply
// is a File naming the path that now is.
type FileOp struct {
	NodeID string `json:"node_id,omitempty"`
	Op     string `json:"op"`           // a File* operation
	Path   string `json:"path"`         // absolute
	To     string `json:"to,omitempty"` // absolute: where a rename moves Path
}

// Operations of a FileOp.
const (
	FileCreate = "create" // an empty file, which must not exist yet
	FileMkdir  = "mkdir"  // a folder, which must not exist yet
	FileRename = "rename" // to To, which must not exist yet
	FileDelete = "delete" // a file, or a folder and all it holds
)

// GitStatus asks a node for the changes in the repository a directory is in.
type GitStatus struct {
	NodeID string `json:"node_id,omitempty"`
	Path   string `json:"path"` // absolute, anywhere inside the work tree
	// Base, when set, asks for the changes since the work tree left this
	// branch, its commits included, rather than against HEAD.
	Base string `json:"base,omitempty"`
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
	// Upstream is the branch Branch tracks, "" for none, and Ahead and
	// Behind the commits each has that the other lacks.
	Upstream string   `json:"upstream,omitempty"`
	Ahead    int      `json:"ahead,omitempty"`
	Behind   int      `json:"behind,omitempty"`
	Branches []string `json:"branches,omitempty"` // the local ones
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

// GitDiff asks a node for one file's changes against HEAD, or with Commit
// the changes that commit made to it.
type GitDiff struct {
	NodeID string `json:"node_id,omitempty"`
	Root   string `json:"root"`
	Path   string `json:"path"`             // as GitFile.Path
	From   string `json:"from,omitempty"`   // where HEAD has a renamed file
	Commit string `json:"commit,omitempty"` // a commit's hash
	Base   string `json:"base,omitempty"`   // the changes since the work tree left this branch
}

// GitLog asks a node for a repository's recent commits: those of its
// branches, remote branches and tags, newest first.
type GitLog struct {
	NodeID string `json:"node_id,omitempty"`
	Root   string `json:"root"`
	Max    int    `json:"max,omitempty"` // capped by the daemon
	// Known is the Fingerprint of the history the requester has, which
	// comes back Unchanged rather than again.
	Known string `json:"known,omitempty"`
}

// GitHistory is the reply to GitLog.
type GitHistory struct {
	Commits     []GitCommit `json:"commits"`
	Current     string      `json:"current,omitempty"`   // the branch checked out
	More        bool        `json:"more,omitempty"`      // older commits are left out
	Fingerprint string      `json:"fingerprint"`         // of the refs and HEAD
	Unchanged   bool        `json:"unchanged,omitempty"` // as Known; Commits are left out
}

// GitCommit is one commit as a history lists it.
type GitCommit struct {
	Hash    string    `json:"hash"`
	Parents []string  `json:"parents,omitempty"`
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
	Subject string    `json:"subject"`
	Refs    []string  `json:"refs,omitempty"` // as git log's %D names them
}

// GitShow asks a node for what one commit changed.
type GitShow struct {
	NodeID string `json:"node_id,omitempty"`
	Root   string `json:"root"`
	Hash   string `json:"hash"`
}

// GitCommitInfo is the reply to GitShow: the commit, its whole message,
// and the files it changed against its first parent.
type GitCommitInfo struct {
	Commit GitCommit `json:"commit"`
	Email  string    `json:"email,omitempty"`
	Body   string    `json:"body,omitempty"`
	Files  []GitFile `json:"files"`
}

// GitRun asks a node to run a git operation on a work tree.
type GitRun struct {
	NodeID string `json:"node_id,omitempty"`
	Root   string `json:"root"`
	Op     string `json:"op"` // a GitOp*
	// Message and Paths are a commit's: the files to commit, as GitFile
	// paths and the From of a rename, or none for every change.
	Message string   `json:"message,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Amend   bool     `json:"amend,omitempty"`
	Branch  string   `json:"branch,omitempty"` // a checkout's or new branch's name
	// Hunk is the hunk a discard undoes, in Paths[0], as the diff read it.
	Hunk []DiffLine `json:"hunk,omitempty"`
	// Worktree is the session worktree a merge, pull request or removal
	// is of.
	Worktree *Worktree `json:"worktree,omitempty"`
}

// Operations of a GitRun.
const (
	GitOpCommit   = "commit"
	GitOpPush     = "push"
	GitOpPull     = "pull" // fast-forward only
	GitOpFetch    = "fetch"
	GitOpCheckout = "checkout"
	GitOpBranch   = "branch" // creates Branch at HEAD and checks it out
	GitOpDiscard  = "discard_hunk"
	GitOpMerge    = "merge"           // a worktree's branch into its base, in the main work tree
	GitOpPR       = "pull_request"    // pushes a worktree's branch and opens a pull request of it
	GitOpRemoveWT = "remove_worktree" // a worktree with no changes left, and its merged branch
)

// GitResult is the reply to GitRun: what git printed.
type GitResult struct {
	Output string `json:"output,omitempty"`
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
