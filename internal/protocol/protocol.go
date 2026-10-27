// Package protocol is the messages spoken between mako front-ends, daemons,
// and peer daemons, and their framing. The messages are generated from
// proto/mako/v2 (`make proto`), into this package: mako uses them as they
// are, on the wire and off it. This file and its neighbours add the framing,
// the constants the messages' string fields take, and helpers. See
// docs/protocol.md.
//
// The messages are protobuf messages, and so are used by pointer: copying
// one, or comparing two with ==, is wrong. proto.Equal compares them, and
// proto.Clone copies one to change.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MaxFrameSize caps a single frame so a peer cannot force an allocation storm.
const MaxFrameSize = 8 << 20

// Type is the kind of message an Envelope carries: the name of its body's
// field.
type Type string

const (
	TypeHello          Type = "hello"
	TypeWelcome        Type = "welcome"
	TypeError          Type = "error"
	TypeListNodes      Type = "list_nodes"
	TypeNodes          Type = "nodes"
	TypeWatch          Type = "watch"
	TypeChanged        Type = "changed"
	TypeAccess         Type = "access"
	TypeAccessInfo     Type = "access_info"
	TypeListSess       Type = "list_sessions"
	TypeSessions       Type = "sessions"
	TypeOpenSession    Type = "open_session"
	TypeSessionInfo    Type = "session_info"
	TypeCloseSess      Type = "close_session"
	TypeRenameSess     Type = "rename_session"
	TypeAnswerApproval Type = "answer_approval"
	TypeAttach         Type = "attach"
	TypeInput          Type = "input"
	TypeOutput         Type = "output"
	TypeResize         Type = "resize"
	TypeSnapshot       Type = "snapshot"
	TypeExited         Type = "exited"
	TypeListDir        Type = "list_dir"
	TypeDir            Type = "dir"
	TypeReadFile       Type = "read_file"
	TypeFile           Type = "file"
	TypeWriteFile      Type = "write_file"
	TypeFileOp         Type = "file_op"
	TypeSearch         Type = "search"
	TypeSearchResult   Type = "search_result"
	TypeStageFile      Type = "stage_file"
	TypeStaged         Type = "staged"
	TypeGitStatus      Type = "git_status"
	TypeGitChanges     Type = "git_changes"
	TypeGitDiff        Type = "git_diff"
	TypeDiff           Type = "diff"
	TypeGitLog         Type = "git_log"
	TypeGitHistory     Type = "git_history"
	TypeGitShow        Type = "git_show"
	TypeGitCommitInfo  Type = "git_commit_info"
	TypeGitRun         Type = "git_run"
	TypeGitResult      Type = "git_result"
	TypeReadChat       Type = "read_chat"
	TypeChat           Type = "chat"
)

// body is the Envelope's oneof, and bodyFields its fields by the Type they
// carry. Package variables are set before the generated files' init
// functions run, so body asks for the descriptors to be built first.
var (
	body = func() protoreflect.OneofDescriptor {
		file_mako_v2_envelope_proto_init()
		return (&Envelope{}).ProtoReflect().Descriptor().Oneofs().ByName("body")
	}()
	bodyFields = func() map[Type]protoreflect.FieldDescriptor {
		out := make(map[Type]protoreflect.FieldDescriptor, body.Fields().Len())
		for i := range body.Fields().Len() {
			fd := body.Fields().Get(i)
			out[Type(fd.Name())] = fd
		}
		return out
	}()
)

// Type is the kind of message e carries, "" for a body this build does not
// know, as a newer peer's.
func (e *Envelope) Type() Type {
	if fd := e.ProtoReflect().WhichOneof(body); fd != nil {
		return Type(fd.Name())
	}
	return ""
}

// Encode makes an Envelope of type t carrying msg, which must be the type's
// message; nil sends it empty. msg is carried as it is, not copied.
func Encode(t Type, id string, msg proto.Message) (*Envelope, error) {
	fd, ok := bodyFields[t]
	if !ok {
		return nil, fmt.Errorf("protocol: no message type %q", t)
	}
	env := &Envelope{Id: id}
	m := env.ProtoReflect()
	if msg == nil {
		m.Set(fd, m.NewField(fd))
		return env, nil
	}
	if got := msg.ProtoReflect().Descriptor().FullName(); got != fd.Message().FullName() {
		return nil, fmt.Errorf("protocol: %s carries %s, not %s", t, fd.Message().FullName(), got)
	}
	m.Set(fd, protoreflect.ValueOfMessage(msg.ProtoReflect()))
	return env, nil
}

// Body is the message env carries, as T: the message itself, not a copy.
// A body of another type is an error.
func Body[T proto.Message](env *Envelope) (T, error) {
	var zero T
	m := env.ProtoReflect()
	fd := m.WhichOneof(body)
	if fd == nil {
		return zero, errors.New("protocol: an envelope with no message")
	}
	msg, ok := m.Get(fd).Message().Interface().(T)
	if !ok {
		return zero, fmt.Errorf("protocol: %s carries %s, not %T", fd.Name(), fd.Message().FullName(), zero)
	}
	return msg, nil
}

// WriteFrame writes one frame: the envelope's length, as a big-endian
// uint32, then the envelope.
func WriteFrame(w io.Writer, env *Envelope) error {
	size := proto.Size(env)
	if size > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", size)
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+size), uint32(size))
	// The size was just computed, so marshalling need not again.
	frame, err := proto.MarshalOptions{UseCachedSize: true}.MarshalAppend(frame, env)
	if err != nil {
		return err
	}
	_, err = w.Write(frame)
	return err
}

// ReadFrame reads one frame. An envelope whose body this build does not
// know, from a newer peer, has no Type, which a daemon answers as
// unsupported.
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
	env := &Envelope{}
	if err := proto.Unmarshal(buf, env); err != nil {
		return nil, err
	}
	return env, nil
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
	e := &Error{Message: err.Error()}
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

// Codes of an Error.
const (
	// CodeChanged is a WriteFile refused because the file changed since it
	// was read.
	CodeChanged = "changed"
	// CodeUnsupported is a request its daemon does not know, being older
	// than the requester.
	CodeUnsupported = "unsupported"
)

// HasFeature reports whether a node speaks a protocol extension.
func (n *Node) HasFeature(feature string) bool {
	return slices.Contains(n.GetFeatures(), feature)
}

// SessionState is the lifecycle stage of a session, SessionInfo.State.
type SessionState = string

const (
	StateStopped  SessionState = "stopped" // restored metadata; starts on attach
	StateStarting SessionState = "starting"
	StateRunning  SessionState = "running"
	StateExited   SessionState = "exited"
)

// Operations of a FileOp.
const (
	FileCreate = "create" // an empty file, which must not exist yet
	FileMkdir  = "mkdir"  // a folder, which must not exist yet
	FileRename = "rename" // to To, which must not exist yet
	FileDelete = "delete" // a file, or a folder and all it holds
)

// Change kinds of a GitFile.
const (
	GitAdded    = "added" // new to the index or untracked
	GitModified = "modified"
	GitDeleted  = "deleted"
	GitRenamed  = "renamed"
	// GitConflicted is a file a merge, rebase, cherry-pick or revert left
	// with conflicts to resolve.
	GitConflicted = "conflicted"
)

// Operations of a GitRun.
const (
	GitOpCommit   = "commit"
	GitOpPush     = "push"
	GitOpPull     = "pull" // fast-forward only, or rebasing with Rebase
	GitOpFetch    = "fetch"
	GitOpCheckout = "checkout"
	GitOpBranch   = "branch" // creates Branch at Commit, or HEAD, and checks it out
	GitOpDiscard  = "discard_hunk"
	GitOpMerge    = "merge"           // a worktree's branch into its base, in the main work tree
	GitOpPR       = "pull_request"    // pushes a worktree's branch and opens a pull request of it
	GitOpRemoveWT = "remove_worktree" // a worktree with no changes left, and its merged branch

	GitOpDiscardFiles = "discard_files" // Paths' changes, and untracked files themselves
	GitOpIgnore       = "ignore"        // adds Paths to the root's .gitignore
	GitOpStash        = "stash"         // every change, untracked files too, as Message
	GitOpStashApply   = "stash_apply"   // Stash, kept
	GitOpStashPop     = "stash_pop"     // Stash, dropped once applied
	GitOpStashDrop    = "stash_drop"    // Stash
	GitOpTrack        = "track"         // checks out a remote branch, Branch as "origin/name", tracking it
	GitOpDeleteBranch = "delete_branch" // Branch, unmerged too with Force
	GitOpMergeBranch  = "merge_branch"  // Branch into the one checked out
	GitOpRebase       = "rebase"        // the branch checked out onto Branch
	GitOpDetach       = "detach"        // checks out Commit, on no branch
	GitOpCherryPick   = "cherry_pick"   // Commit onto the branch checked out
	GitOpRevert       = "revert"        // Commit, as a new commit
	GitOpTag          = "tag"           // Tag at Commit, annotated with Message if any
	GitOpContinue     = "continue"      // the operation under way, its conflicts resolved
	GitOpAbort        = "abort"         // the operation under way
)

// Same reports whether a and b are equal messages: proto.Equal, typed, for
// a signal's equality, and a message is itself without a look at its fields.
func Same[T proto.Message](a, b T) bool {
	return proto.Message(a) == proto.Message(b) || proto.Equal(a, b)
}

// Equal reports whether a and b hold equal messages, in order.
func Equal[T proto.Message](a, b []T) bool { return slices.EqualFunc(a, b, Same[T]) }

// Time is t as a message carries it: nil for the zero time, so that an
// unset time reads back as zero through TimeOf.
func Time(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// TimeOf is a message's time as Go's: the zero time for none.
func TimeOf(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}
