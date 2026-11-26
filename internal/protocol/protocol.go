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
	"strings"
	"sync"
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
	TypeGetWorkspace   Type = "get_workspace"
	TypeWorkspace      Type = "workspace"
	TypeLayoutDecision Type = "layout_decision"
	TypeListSess       Type = "list_sessions"
	TypeSessions       Type = "sessions"
	TypeOpenSession    Type = "open_session"
	TypeSessionInfo    Type = "session_info"
	TypeCloseSess      Type = "close_session"
	TypeRenameSess     Type = "rename_session"
	TypeAnswerApproval Type = "answer_approval"
	TypeSend           Type = "send"
	TypeAnswerQuestion Type = "answer_question"
	TypeAnswerDialog   Type = "answer_dialog"
	TypeConfigureAgent Type = "configure_agent"
	TypeSleepSession   Type = "sleep_session"
	TypeSeeSession     Type = "see_session"
	TypeAttach         Type = "attach"
	TypeInput          Type = "input"
	TypeOutput         Type = "output"
	TypeResize         Type = "resize"
	TypeSnapshot       Type = "snapshot"
	TypeExited         Type = "exited"
	TypeListDir        Type = "list_dir"
	TypeDir            Type = "dir"
	TypeWatchFiles     Type = "watch_files"
	TypeFilesChanged   Type = "files_changed"
	TypeReadFile       Type = "read_file"
	TypeFile           Type = "file"
	TypeWriteFile      Type = "write_file"
	TypeFileOp         Type = "file_op"
	TypeSearch         Type = "search"
	TypeSearchResult   Type = "search_result"
	TypeStageFile      Type = "stage_file"
	TypeStaged         Type = "staged"
	TypeTunnel         Type = "tunnel"
	TypeTunneled       Type = "tunneled"
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
	TypeListCommands   Type = "list_commands"
	TypeCommands       Type = "commands"
	TypeReadChat       Type = "read_chat"
	TypeChat           Type = "chat"
	TypeForgeList      Type = "forge_list"
	TypeForgeItems     Type = "forge_items"
	TypeForgeView      Type = "forge_view"
	TypeForgeDetail    Type = "forge_detail"
	TypeForgeRun       Type = "forge_run"
	TypeListDevices    Type = "list_devices"
	TypeDevices        Type = "devices"
	TypeBorrow         Type = "borrow"
	TypeLease          Type = "lease"
	TypeGiveBack       Type = "give_back"
	TypeListLeases     Type = "list_leases"
	TypeLeases         Type = "leases"
	TypeClaim          Type = "claim"
	TypeClaimed        Type = "claimed"
	TypeDeviceConn     Type = "device_conn"
	TypeDeviceListen   Type = "device_listen"
	TypeDeviceAccept   Type = "device_accept"
)

const (
	TypeReadProjectAgents     Type = "read_project_agents"
	TypeProjectAgents         Type = "project_agents"
	TypeSetProjects           Type = "set_projects"
	TypeProjects              Type = "projects"
	TypeRelayAgentMessage     Type = "relay_agent_message"
	TypeAgentMessage          Type = "agent_message"
	TypeWatchChat             Type = "watch_chat"
	TypeChatLive              Type = "chat_live"
	TypeListAgentModels       Type = "list_agent_models"
	TypeAgentModels           Type = "agent_models"
	TypeInspectAgentHome      Type = "inspect_agent_home"
	TypeAgentHomeInfo         Type = "agent_home_info"
	TypeMigrateAgentHome      Type = "migrate_agent_home"
	TypeAgentHomeMigrated     Type = "agent_home_migrated"
	TypeRunAgentPrompt        Type = "run_agent_prompt"
	TypeAgentPromptResult     Type = "agent_prompt_result"
	TypeListProviders         Type = "list_providers"
	TypeProviders             Type = "providers"
	TypeProviderAction        Type = "provider_action"
	TypeRunDecision           Type = "run_decision"
	TypeDecisionResult        Type = "decision_result"
	TypeSyncPluginConfig      Type = "sync_plugin_config"
	TypePluginConfigSyncState Type = "plugin_config_sync_state"
	TypeSyncAgentConfig       Type = "sync_agent_config"
	TypeAgentSyncState        Type = "agent_sync_state"
	TypeRelaySpawnAgent       Type = "relay_spawn_agent"
	TypeRelayStopAgent        Type = "relay_stop_agent"
)

// Newer reports whether the edit made at version v on node is newer than
// the one at version w on other: the later wins, and of two at once, the
// greater node's.
func Newer(v int64, node string, w int64, other string) bool {
	return v > w || v == w && node > other
}

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
// uint32, then the envelope. With pack, a frame that compresses well goes
// compressed, as frames between daemons do.
func WriteFrame(w io.Writer, env *Envelope, pack bool) error {
	size := proto.Size(env)
	if size > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", size)
	}
	buf := getFrame()
	defer putFrame(buf)
	frame := binary.BigEndian.AppendUint32(slices.Grow((*buf)[:0], 4+size), uint32(size))
	// The size was just computed, so marshalling need not again.
	frame, err := proto.MarshalOptions{UseCachedSize: true}.MarshalAppend(frame, env)
	*buf = frame
	if err != nil {
		return err
	}
	if pack {
		if small, ok := packed(frame[4:]); ok {
			frame = binary.BigEndian.AppendUint32(frame[:0], uint32(len(small))|packedBit)
			frame = append(frame, small...)
		}
	}
	_, err = w.Write(frame)
	return err
}

// WriteStreamed writes one frame packed as p's next piece, for a reader
// that asked for its stream so packed.
func WriteStreamed(w io.Writer, env *Envelope, p *Packer) error {
	size := proto.Size(env)
	if size > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", size)
	}
	plainBuf := getFrame()
	defer putFrame(plainBuf)
	plain, err := proto.MarshalOptions{UseCachedSize: true}.MarshalAppend(slices.Grow((*plainBuf)[:0], size), env)
	*plainBuf = plain
	if err != nil {
		return err
	}
	if len(plain) > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", len(plain))
	}
	frameBuf := getFrame()
	defer putFrame(frameBuf)
	frame := slices.Grow((*frameBuf)[:0], 4+len(plain)/4)[:4]
	*frameBuf = frame
	frame, err = p.pack(frame, plain)
	if frame != nil {
		*frameBuf = frame
	}
	if err != nil {
		return err
	}
	if len(frame)-4 > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", len(frame)-4)
	}
	binary.BigEndian.PutUint32(frame, uint32(len(frame)-4)|streamedBit)
	_, err = w.Write(frame)
	return err
}

// ReadFrame reads one frame, compressed or not. An envelope whose body
// this build does not know, from a newer peer, has no Type, which a daemon
// answers as unsupported.
func ReadFrame(r io.Reader) (*Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n&streamedBit != 0 {
		// Only the daemon that asked for them reads streamed frames, with
		// UnpackFrames.
		return nil, errors.New("frame: streamed on a stream that did not ask for it")
	}
	packed := n&packedBit != 0
	n &^= packedBit
	if n > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	room := getFrame()
	defer putFrame(room)
	*room = slices.Grow((*room)[:0], int(n))[:n]
	buf := *room
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	if packed {
		var err error
		if buf, err = Decompress(buf); err != nil {
			return nil, fmt.Errorf("frame: %w", err)
		}
	}
	env := &Envelope{}
	if err := proto.Unmarshal(buf, env); err != nil {
		return nil, err
	}
	return env, nil
}

// frames is the room frames are written and read in, which a frame is done
// with once written, or once unmarshalled: proto.Unmarshal copies the
// bytes and strings it reads out. Only the room of a frame up to
// maxPooledFrame goes back, so that one as large as MaxFrameSize is not
// held on to.
var frames = sync.Pool{New: func() any { return new([]byte) }}

const maxPooledFrame = 64 << 10

func getFrame() *[]byte { return frames.Get().(*[]byte) }

func putFrame(b *[]byte) {
	if cap(*b) <= maxPooledFrame {
		frames.Put(b)
	}
}

// UnpackFrames copies the frames src carries to dst until src ends,
// decompressing those that came compressed, streamed or not, for a reader
// that takes only the plain form, as a front-end on its daemon's socket.
func UnpackFrames(dst io.Writer, src io.Reader) error {
	var hdr [4]byte
	var stream Unpacker
	defer stream.Close()
	for {
		if _, err := io.ReadFull(src, hdr[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := unpackOneFrame(dst, src, &hdr, &stream); err != nil {
			return err
		}
	}
}

func unpackOneFrame(dst io.Writer, src io.Reader, hdr *[4]byte, stream *Unpacker) error {
	n := binary.BigEndian.Uint32(hdr[:])
	packed, streamed := n&packedBit != 0, n&streamedBit != 0
	n &^= packedBit | streamedBit
	if n > MaxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", n)
	}
	inRoom := getFrame()
	defer putFrame(inRoom)
	*inRoom = slices.Grow((*inRoom)[:0], 4+int(n))[:4+int(n)]
	in := *inRoom
	if _, err := io.ReadFull(src, in[4:]); err != nil {
		return err
	}
	out := in
	switch {
	case packed && streamed:
		return errors.New("frame: both packed and streamed")
	case packed:
		outRoom := getFrame()
		defer putFrame(outRoom)
		prefix := slices.Grow((*outRoom)[:0], 4)[:4]
		*outRoom = prefix
		body, err := decompressTo(prefix, in[4:])
		if body != nil {
			*outRoom = body
		}
		if err != nil {
			return fmt.Errorf("frame: %w", err)
		}
		if len(body)-4 > MaxFrameSize {
			return fmt.Errorf("frame too large: %d bytes", len(body)-4)
		}
		out = body
	case streamed:
		outRoom := getFrame()
		defer putFrame(outRoom)
		prefix := slices.Grow((*outRoom)[:0], 4)[:4]
		*outRoom = prefix
		body, err := stream.unpackTo(prefix, in[4:])
		if body != nil {
			*outRoom = body
		}
		if err != nil {
			return fmt.Errorf("frame: %w", err)
		}
		if len(body)-4 > MaxFrameSize {
			return fmt.Errorf("frame too large: %d bytes", len(body)-4)
		}
		out = body
	}
	binary.BigEndian.PutUint32(out, uint32(len(out)-4))
	_, err := dst.Write(out)
	return err
}

// CodedError is a failure a requester may act on by its Code, which
// crosses the wire in Error.Code: from a handler's error to the requester,
// through any daemon relaying it.
type CodedError struct {
	Code    string
	Message string
	// Args are what the code's sentence names, for a front-end to say it
	// in its user's language.
	Args []string
}

func (e *CodedError) Error() string { return e.Message }

// HasCode reports whether err is a CodedError with code.
func HasCode(err error, code string) bool {
	var ce *CodedError
	return errors.As(err, &ce) && ce.Code == code
}

// WriteError is a shorthand for sending a TypeError frame.
func WriteError(w io.Writer, id string, err error) error {
	// An error may quote what a server, a registry or a plugin wrote.
	e := &Error{Message: Printable(err.Error())}
	if ce, ok := errors.AsType[*CodedError](err); ok {
		e.Code = ce.Code
		for _, a := range ce.Args {
			e.Args = append(e.Args, Printable(a))
		}
	}
	env, encErr := Encode(TypeError, id, e)
	if encErr != nil {
		return encErr
	}
	return WriteFrame(w, env, false)
}

// Codes of an Error.
const (
	// CodeChanged is a WriteFile refused because the file changed since it
	// was read.
	CodeChanged = "changed"
	// CodeUnsupported is a request its daemon does not know, being older
	// than the requester.
	CodeUnsupported = "unsupported"
	// CodeNoPlugin is a block of a plugin the node does not have, which
	// asking again will not change until the plugin is installed.
	CodeNoPlugin = "no_plugin"
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
	FileCopy   = "copy"   // to To, which must not exist yet: a file, or a folder and all it holds
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
	GitOpClone        = "clone"         // clones Message (a git URL) into Root (destination folder)
)

// What a ForgeList and a ForgeView are of.
const (
	ForgePR    = "pr"
	ForgeIssue = "issue"
	ForgeRuns  = "run" // a workflow run of the forge's Actions
)

// Operations of a ForgeRun.
const (
	ForgeOpCreatePR   = "pr_create"   // pushes the branch checked out and opens a pull request of it into Base
	ForgeOpCheckout   = "pr_checkout" // checks out pull request Number's branch
	ForgeOpFetchPR    = "pr_fetch"    // fetches pull request Number's head; replies with its commit
	ForgeOpComment    = "comment"     // Body on pull request or issue Number, as Kind says
	ForgeOpReview     = "review"      // reviews pull request Number as Review says, with Body
	ForgeOpRerun      = "run_rerun"   // runs run Number again, only its failed jobs with Failed
	ForgeOpCancel     = "run_cancel"  // cancels run Number
	ForgeOpFailedLogs = "run_log"     // replies with the logs of run Number's failed steps
)

// Failed reports whether a forge check or run's conclusion is a failure.
func Failed(conclusion string) bool {
	switch strings.ToLower(conclusion) {
	case "failure", "error", "cancelled", "timed_out", "action_required", "startup_failure":
		return true
	}
	return false
}

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

// Kinds of a Device.
const (
	DeviceADB = "adb" // an Android device or emulator, through adb
)

// DeviceDetached is the state of a device lent that is no longer attached
// to its node.
const DeviceDetached = "detached"

// States of a Lease.
const (
	LeaseHeld         = "held"
	LeaseDetached     = "detached"     // its device is not attached to its node, for now
	LeaseReconnecting = "reconnecting" // its device's node cannot be reached, for now
	LeaseReturned     = "returned"
)

// Ref names a device across the mesh: "node/kind/serial".
func (d *Device) Ref() string { return DeviceRef(d.GetNodeId(), d.GetKind(), d.GetSerial()) }

// DeviceRef is the ref of the device of kind and serial on node.
func DeviceRef(node, kind, serial string) string { return node + "/" + kind + "/" + serial }

// MatchDevice finds the device of devices name names: by its ref, its
// node's name and its serial ("node/serial"), its serial, or its name.
// nodeName names a node by its ID.
func MatchDevice(devices []*Device, nodeName func(id string) string, name string) (*Device, error) {
	var found []*Device
	for _, d := range devices {
		switch {
		case d.Ref() == name:
			return d, nil
		case name == nodeName(d.GetNodeId())+"/"+d.GetSerial(), name == d.GetSerial(), strings.EqualFold(name, d.GetName()):
			found = append(found, d)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, fmt.Errorf("no device %q", name)
	}
	var names []string
	for _, d := range found {
		names = append(names, nodeName(d.GetNodeId())+"/"+d.GetSerial())
	}
	return nil, fmt.Errorf("%q could be %s", name, strings.Join(names, " or "))
}

// ParseDeviceRef splits a ref into its node, kind and serial.
func ParseDeviceRef(ref string) (node, kind, serial string, err error) {
	parts := strings.SplitN(ref, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("%q is not a device, as node/kind/serial", ref)
	}
	return parts[0], parts[1], parts[2], nil
}
