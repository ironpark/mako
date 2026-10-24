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
	TypeAttach      Type = "attach"
	TypeInput       Type = "input"
	TypeOutput      Type = "output"
	TypeResize      Type = "resize"
	TypeSnapshot    Type = "snapshot"
	TypeExited      Type = "exited"
	TypeGossip      Type = "gossip"
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
	// Agents are what the node can launch, with their profiles' names; its
	// flags and environment stay on the node.
	Agents []AgentInfo `json:"agents,omitempty"`
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
	StateStarting SessionState = "starting"
	StateRunning  SessionState = "running"
	StateExited   SessionState = "exited"
)

// SessionInfo describes one agent session.
type SessionInfo struct {
	ID        string       `json:"id"`
	NodeID    string       `json:"node_id"`
	Agent     string       `json:"agent"`
	Profile   string       `json:"profile,omitempty"`
	Project   string       `json:"project"`
	Command   []string     `json:"command"`
	State     SessionState `json:"state"`
	Cols      uint16       `json:"cols"`
	Rows      uint16       `json:"rows"`
	StartedAt time.Time    `json:"started_at"`
	ExitCode  *int         `json:"exit_code,omitempty"`
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

// Snapshot is a rendered view of the session screen, used for replay and for
// front-ends that only want the current state rather than a live stream.
type Snapshot struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
	// Text is the screen contents; VT when Styled, plain text otherwise.
	Text   string `json:"text"`
	Styled bool   `json:"styled"`
}

// CloseSession terminates a session.
type CloseSession struct {
	SessionID string `json:"session_id"`
	NodeID    string `json:"node_id,omitempty"`
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

// WriteError is a shorthand for sending a TypeError frame.
func WriteError(w io.Writer, id string, err error) error {
	env, encErr := Encode(TypeError, id, Error{Message: err.Error()})
	if encErr != nil {
		return encErr
	}
	return WriteFrame(w, env)
}
