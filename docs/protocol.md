# mako protocol

This is the wire protocol spoken between mako front-ends (the `mako` CLI and
the desktop app), the daemon they connect to (`makod`, or the one the app
runs itself), and the daemons of the other machines on the mesh. Its messages
are protocol buffers: the schema is [`proto/mako/v2`](../proto/mako/v2),
and this document describes how the messages are used.

## At a glance

| | Front-end ↔ its daemon | Daemon ↔ daemon |
|---|---|---|
| Transport | the daemon's Unix socket | libp2p (QUIC or TCP), port `4243`, maybe relayed |
| Stream | a connection to a request | `/mako/3`, a libp2p stream to a request |
| Access | the socket's file mode: this user alone | keys, one way: `/mako/trust/1` |
| Frames | length-prefixed protobuf `Envelope`s | the same, the larger compressed |
| Pushed state | `watch` → `changed` | `NodeState` on `/mako/state/3`, compressed |

## Contents

- [Roles](#roles)
- [Transport](#transport)
- [Streams and framing](#streams-and-framing)
- [Handshake](#handshake)
- [Requests and replies](#requests-and-replies)
- [Errors](#errors)
- [Routing across the mesh](#routing-across-the-mesh)
- [Message reference](#message-reference)
- [Attach streams](#attach-streams)
- [Discovery](#discovery)
- [The mesh over libp2p](#the-mesh-over-libp2p)
- [Compatibility](#compatibility)
- [Limits](#limits)
- [Layout documents](#layout-documents)
- [Plugin blocks](#plugin-blocks)
- [The local hook socket](#the-local-hook-socket)
- [Access](#access)
- [Security](#security)

## Roles

```
 front-end ──socket──▶ daemon A ──libp2p──▶ daemon B
 (CLI, app)            (local)              (peer)
```

- A **front-end** connects to one daemon, the one on its own machine, and
  does everything through it.
- A **daemon** runs the sessions of its machine and answers for them. When a
  request names another node, it forwards the request to that node's daemon
  and relays the answer, so one connection reaches the whole mesh.
- A **peer** is another daemon. Daemons find each other by discovery and
  from each other's state, and talk to each other with the same protocol,
  marked as peers in their handshake. Who may ask what of whom is decided by
  keys, one way; see [Access](#access).

## Transport

A daemon listens for its front-ends on a Unix socket, `daemon.sock` in its
run directory, with mode `0600`: only its user can reach it, which is all
the access check a front-end gets. The run directory (`config.RunDir`) is
`run` in the configuration directory, made `0700`, or where that is too
long for a socket's path (some 100 bytes), `mako-<uid>-<hash of the
configuration directory>` in the temporary directory. It holds the hook
socket and the sessions' MCP configurations too. A socket left
behind by a daemon that is gone is replaced when the next starts; one a
daemon still answers on is not, so two daemons cannot share a
configuration directory.

Each request is a connection of its own; closing it ends the request, or
the attach or watch it turned into. A front-end on another machine reaches
a daemon only through the mesh, by way of its own daemon, or with the
socket forwarded, as `ssh -L` can.

Daemons link to each other over libp2p (see
[The mesh over libp2p](#the-mesh-over-libp2p)).

## Streams and framing

Every exchange happens on a bidirectional stream that the caller opens: a
connection to the daemon's socket, or a libp2p stream of `/mako/3` between
daemons.
A stream carries **frames**, each one protobuf `Envelope` with a length
prefix:

```
┌──────────────────────┬──────────────────────────────────┐
│ length: uint32, BE   │ Envelope: protobuf, length bytes │
└──────────────────────┴──────────────────────────────────┘
```

A frame is at most 8 MiB (`protocol.MaxFrameSize`); a reader refuses a larger
length before allocating.

**Compression.** Between daemons, a frame whose envelope is at least
1 KiB goes compressed with [zstd](https://facebook.github.io/zstd/) when
that saves at least an eighth of it: a file, a diff, a conversation, a
replayed screen. Its length then has the top bit set, `0x80000000`, and
counts the compressed bytes:

```
┌──────────────────────────────┬────────────────────────────────────┐
│ 1 | length: 31 bits, BE      │ zstd frame of the Envelope         │
└──────────────────────────────┴────────────────────────────────────┘
```

Every reader takes both forms, and refuses one that decompresses past
8 MiB. A daemon's socket carries only the plain form, since its frames
never leave the machine: a daemon relaying a peer's attach to its
front-end decompresses the peer's frames on the way, and a daemon
relaying one to another daemon passes them on as they came.

**Streamed frames.** An attached session's output is mostly small frames
that repeat what came before: a spinner redrawn, a status line, the same
colours and cursor moves. Packed alone, none is worth it. A daemon relaying
an attach to its front-end sets the `attach`'s `pack_stream`, and the
session's daemon then packs every frame after it as the next piece of one
zstd stream, flushed so each can be read as it comes; each costs about what
is new since the frames before it. Such a frame has the second bit of its
length set, `0x40000000`, and starts with its envelope's size:

```
┌──────────────────────────────┬────────────────┬───────────────────────────────┐
│ 0 1 | length: 30 bits, BE    │ size: uvarint  │ the zstd stream's next piece  │
└──────────────────────────────┴────────────────┴───────────────────────────────┘
```

The piece decodes to exactly the envelope; one that decodes to less, or
holds more, ends the stream. A piece refers at most 1 MiB back. Only the
daemon that asked reads these, as it unpacks every frame for its socket;
a daemon relaying the attach between two others passes them on as they
came, and a daemon that does not know `pack_stream` sends frames as before.

The `Envelope` is an `id`, a `within_ms`, and a `body` oneof, one field for
each message type:

```proto
message Envelope {
  string id = 1;
  uint32 within_ms = 2;
  oneof body {
    Hello hello = 10;
    Welcome welcome = 11;
    Error error = 12;
    // ...
    Output output = 42;
    // ...
  }
}
```

The body field's name is the message's type (`protocol.Type`): `hello`,
`open_session`, `git_status`, and so on. `id` is optional: a reply carries
the `id` of its request, and current clients leave it empty and rely on
order. A body this build does not know, from a newer peer, reads as no type,
which a daemon answers as `unsupported`.

`within_ms`, on a request whose reply ends the stream's use, is how long
from its sending the asker waits for that reply; 0, or left out, for no
bound. It is a time rather than a moment, as nodes' clocks differ. A daemon
passing the request on to another node waits for that node's reply no
longer, and sets `within_ms` to what is left; one passed on with no bound
is waited for two minutes at most, so a peer that takes a request and never
answers holds nothing for long. A request whose stream goes on past its
reply, as an attach, a tunnel or a claim, sets none.

### The schema and the Go types

[`proto/mako/v2`](../proto/mako/v2) holds the schema, in files by area:
`envelope.proto`, `mesh.proto`, `session.proto`, `files.proto`, `git.proto`
and `chat.proto`. [buf](https://buf.build) lints it and generates the Go
messages into `internal/protocol` itself (`make proto`); the generated code
is committed. The schema is the only definition of the messages: mako uses
the generated types throughout, the daemon and the window alike, with no
types of its own to convert to.

Hand-written files beside the generated ones add what the schema cannot:
the framing (`Encode`, `Body`, `WriteFrame`, `ReadFrame`), the constants the
string fields take (`StateRunning`, `AgentWaiting`, `GitOpCommit`, …),
`Time` and `TimeOf` for timestamps, and methods such as `Usage.Merge`.
`protocol.Encode(t, id, msg)` places a message in the envelope as it is,
and `protocol.Body[*T](env)` hands it back, not copied.

Being protobuf messages, they are used by pointer and read through their
getters, which are nil-safe: copying one or comparing two with `==` is
wrong, and `go vet` reports a copy. `proto.Equal` compares them
(`protocol.Equal` a list of them), and code that changes a message it does
not own changes a `proto.CloneOf` it. Times are
`google.protobuf.Timestamp`; `SessionInfo`'s `exit_code` is an `optional`
field. Enumerations such as a session's `state` stay strings, matching the
constants.

The daemon keeps its saved sessions (`sessions.json`) in protojson with the
proto field names, which reads the files the JSON era wrote.

## Handshake

The first frame on every stream is a `hello`, and the daemon answers with a
`welcome` before anything else:

```
hello   { name: "mako-cli", version: "v0.3.0" }
welcome { node: { ...the daemon's Node... } }
```

| `Hello` field | |
|---|---|
| `name` | The caller's name: a daemon's node name, or `mako-cli` from the CLI and the app alike. |
| `version` | The caller's build. |
| `hops` | On a peer's stream, how many daemons the requests went through before the one that sent them: 0 from the daemon whose front-end made them. See [Routing](#routing-across-the-mesh). |

A stream is a peer's or a front-end's by where it comes from, not by what
its hello says: one on libp2p is a peer's, which gets only the daemon's own
sessions from `list_sessions` and may not send `access`, and one on the
socket a front-end's.

A first frame that is not a `hello` gets an `error`, and the stream is then
done.

A daemon asking a peer sends its request right after its `hello`, without
waiting for the `welcome`, and reads the `welcome` with the reply: each
hop of a request costs one round trip, not two.

The `welcome`'s `Node` is the daemon describing itself. Its `features` and
`revision` are what a caller checks before using newer parts of the protocol;
see [Compatibility](#compatibility).

## Requests and replies

After the handshake a stream carries requests, one at a time. For each
request the daemon sends exactly one reply frame (or an `error` frame) before
reading the next request, except where the reference says otherwise. A
caller may keep a stream for many requests or open a new one for each; the
clients in this repository open a greeted stream per request.

Three requests break that rule:

| Request | |
|---|---|
| `input` | Sent on an ordinary stream, it is not answered. |
| `attach` | Turns the stream into a session pipe; see [Attach streams](#attach-streams). |
| `watch` | Turns the stream into a feed of `changed` frames, until the caller closes it. |

A stream that has become a pipe or a feed carries no more requests.

## Errors

A failed request is answered with an `error` frame in place of its reply:

```
error { message: "unknown session \"7f3a…\"", code: "" }
```

`message` is for people. `code`, when present, is for programs:

| Code | Meaning |
|---|---|
| `changed` | A `write_file` was refused because the file changed since the writer read it. |
| `unsupported` | The daemon does not know the request's type; it predates the caller. |
| `no_plugin` | A `watch_block` or `block_event` of a plugin the node does not have, or a node that runs no plugins: asking again changes nothing until the plugin is installed (revision 31). |

From revision 34, the plugin store's refusals, and a plugin enabled while
it is blocked, carry a code and `args`, what its sentence names, in
order, so that a front-end says it in its user's language; `message`
stays the daemon's English, for a terminal and for a front-end that does
not know the code:

| Code | `args` | Meaning |
|---|---|---|
| `registry_unreachable` | registry | The registry could not be read: out of reach, or not an index mako reads. |
| `registry_needed` | id | Several registries list the ID: the install must name one. |
| `not_listed` | id | No registry of the node lists the ID. |
| `api_mismatch` | id, registry | The registry lists no version for this mako's Host API. |
| `plugin_blocked` | id, version, why | A blocklist blocks it: not installed, updated or enabled. |
| `pinned_key` | id, registry | Signed by another key than the one pinned at its first install. |
| `installed_already` | id, version | That version is installed. |
| `downgrade` | id, version, installed | Older than the version installed: mako does not go back. |
| `dev_folder` | id | `plugins/<id>` is a folder in development: neither replaced nor removed. |
| `package_unreachable` | id, version | The package did not download. |
| `timed_out` | id, version | Its download ran out of time. |
| `too_large` | id, version | It is larger than a package may be. |
| `hash_mismatch` | id, version, registry | Not the package the index lists (SHA-256 or size). |
| `bad_package` | id, version | Not a valid package, or not of the ID and version listed. |
| `signature_mismatch` | id, version, registry | Not signed by the key the index lists. |
| `permissions_differ` | id, version, registry | Its manifest asks for other permissions than the listing shows. |
| `not_installed` | id | A removal of what is not installed. |

A daemon forwarding a request to a peer passes the peer's `code` back
unchanged. The message gains a `remote: ` prefix at each hop. A message
may quote what another party wrote, a registry, a package, a plugin or
a server; its control and reordering characters but for a line break
and a tab are sent escaped (`\x1b`, `\u202e`), so that a terminal
printing it takes none as its own.

An `error` does not end the stream: the daemon goes on reading requests.

## Routing across the mesh

Most requests have a `node_id`:

- **Empty, or the daemon's own ID:** the daemon serves the request itself.
- **Another node's ID:** the daemon opens a peer stream to that node, sends
  the request with `node_id` cleared, and relays the reply, or the error.
  A node it does not know fails with "unknown node".
- **A node that does not let the daemon in, but that it reaches through
  others:** the daemon sends the request, `node_id` and all, to the peer it
  reaches the node through, which routes it in turn.

Trust carries along a chain: a daemon reaches every node at the end of a
chain of nodes each letting in the one before, through the first of them.
That gives it nothing it lacked, since the first could make the same
requests of the rest itself, and a link of the chain that lets go cuts off
the rest. Each daemon sends the peers it lets in, in its state's `peers`,
how it reaches each node it knows: `hops`, 1 for a node that lets it in, and
`via`, the peer it goes through. A peer that cannot reach a node in fewer
hops itself routes through it, ignoring routes that go through itself. A
request goes through at most 8 daemons: each counts them in its `hello`'s
`hops` to the next, and passes on none past that, so a stale route cannot
send one round. Other nodes fail with "has not let this node in". Each
node's state tells the nodes it lets in of the nodes it knows, so the table
fills with the whole mesh.

`list_sessions` is not routed by node. A daemon asked by a front-end answers
with its own sessions and those every peer that lets it in
[sent](#the-mesh-over-libp2p) in the last 20 seconds; it asks no one. A
daemon asked by a peer answers with its own sessions only. A peer's state
carries the sessions of the nodes it reaches too, which a daemon that
reaches those nodes through it keeps; it drops the rest.

`list_nodes`, `watch` and `access` are answered by the daemon itself.

## Message reference

Requests are grouped by area. The reply column is the `type` of the frame
that answers on success.

### Mesh

| Request | Payload | Reply | |
|---|---|---|---|
| `list_nodes` | `{}` | `nodes` | The daemon itself, then its peers, each with how the daemon stands with it. |
| `watch` | `Watch` | `changed`, then more | Turns the stream into a feed: a `changed` at once, then one whenever the mesh's nodes or sessions, or the daemon's access, change, until the front-end closes the stream. With `approvals`, the front-end watches for [approvals](#sessions) meanwhile too; with `layouts`, it shows the window layout changes staged on the daemon (revision 24; see [Layout documents](#layout-documents)). |
| `access` | `Access` | `access_info` | Who the daemon lets in, and changes to it; see [Access](#access). From a front-end only. |
| `get_workspace` | `GetWorkspace` | `workspace` | The workspace the daemon holds; see [Workspaces](#workspaces). With `node_id`, the one that node holds, which a node gives one it lets in, to copy its spaces from (revision 22), whether it shares it or not; with its `digest` only while it shares it, and a peer merges only an answer with one. |
| `workspace` | `Workspace` | `workspace` | Gives the daemon an edit of the workspace, a delta in `state` (revision 23), or the whole `doc` a window kept before, to take in once; the reply is the workspace held after. With `to_node`, the edit is given to that node, routed as a request's `node_id` is; a node takes a delta, never a `doc`, from a node it lets in (revision 25). |
| `layout_decision` | `LayoutDecision` | `workspace` | Keeps or reverts the window layout change staged on the daemon, or forgets what the user allowed agents to change without asking (revision 24; see [Layout documents](#layout-documents)); from revision 32 allows (with `option`) or denies a plugin's change, which no session holds an approval of. From the node's own front-ends only. |

```proto
message Node {
  string id = 1;
  string name = 2;
  string addr = 3;
  string os = 4;
  string arch = 5;
  string version = 6;
  string source = 7; // mdns, tailscale, mesh, self
  google.protobuf.Timestamp last_seen = 8;
  repeated string features = 9; // protocol extensions it speaks
  repeated AgentInfo agents = 10; // what it can launch: {name, profiles}
  int64 revision = 11; // protocol.Revision of its build
  string peer_id = 12; // its libp2p identity
  repeated string multiaddrs = 13; // where it is on libp2p, relayed addresses included
  bool trusted = 14;   // the daemon answering lets it in
  bool trusts_us = 15; // it lets the daemon answering in
  string key = 16;     // its own key's fingerprint, SHA256:…
  string owner = 17;   // the fingerprint of the SSH key vouching for it
  uint32 hops = 18;    // daemons a request goes through to it: 1 when it lets the daemon in, 0 for unreachable
  string via = 19;     // the peer a request to it goes to first, when hops is more than 1
  string link = 20;    // the daemon's own libp2p link to it: up, linking, down; "" for itself or none
  string link_error = 21; // why the last try at a link down failed
  google.protobuf.Timestamp relink_at = 22; // when a link down is tried again
  bytes workspace_digest = 23; // its workspace's digest, as it tells the nodes it lets in (revision 25)
}

message Changed {
  bool nodes = 1;    // the nodes changed: ask list_nodes again
  bool sessions = 2; // the sessions changed: ask list_sessions again
  bool access = 3;   // a request came, or the keys changed: ask access again
  bool workspace = 4; // the workspace changed: ask get_workspace again
  bool devices = 5;   // a device lent or borrowed here changed
  bool plugins = 6;   // a plugin came, went or changed: ask list_plugins again (revision 26)
  bool agent_blocks = 7; // an agent's block came, went or moved: ask list_agent_blocks again (revision 27)
}

message Workspace {
  bytes doc = 1;      // a window's from before revision 23, opaque to the daemon
  int64 version = 2;  // when doc was made, in Unix nanoseconds: the later wins
  string node_id = 3; // where doc was made, which breaks a tie
  WorkspaceState state = 4; // the workspace as a CRDT, or a delta of it (revision 23)
  bytes digest = 5;   // a hash of state
  LayoutStage stage = 6; // a window layout change held here, to the node's own front-ends (revision 24)
  string to_node = 7; // the node an edit is given to, "" for the daemon's own (revision 25); node_id is no address
}

message LayoutStage {
  string id = 1;         // also the approval's ID on the agent's session
  string document = 2;   // the document's name
  bytes doc = 3;         // the document proposed
  string phase = 4;      // approve, keep
  string session_id = 5; // the agent's session
  string agent = 6;      // its name
  string tool = 7;       // set_layout, patch_layout or undo_layout
  string summary = 8;    // what it changes, in a line
  int64 deadline = 9;    // keep: when it reverts, in Unix ms
  string plugin_id = 10; // a plugin's change: no session; revision 32
}

message LayoutDecision {
  string stage_id = 1;
  string decision = 2; // keep, revert, forget; allow, deny (revision 32)
  string node_id = 3;  // "" for the daemon's own
  string option = 4;   // an allow's: space, always; "" for once
}
```

#### Workspaces

The workspace is what the windows of one person's nodes share: the spaces,
the projects of several folders, the names and looks given to sections,
the pins, and the tabs and panes of each space (`layouts`, by
`spaceID|context`). Since revision 23 it is a CRDT, `WorkspaceState`
(`proto/mako/v2/workspace.proto`): every field a register, every list a set
or a map of entries, each with the hybrid logical clock stamp of its last
edit, so that two nodes' states merge field by field and nodes that merged
the same states, in any order, hold the same. An entity, a space, project
or root, shows while its `created_at` is later than its `deleted_at`; what
refers to one that does not show is kept, and left out by readers. A
list's order is its entries' `rank`s, fractional indices compared as
strings, then their IDs.

**Edits.** Only a daemon stamps: a front-end sends `workspace` with a delta
in `state`, a `WorkspaceState` holding only what it changed, and the daemon
stamps every clock in it with one tick of its own clock, whatever the
front-end put there, merges it, and answers with the whole state and its
`digest`. In a delta, a register present is written; a `created_at`,
`deleted_at`, `added_at` or `removed_at` asks for its edit when it is not
zero, whatever it holds (`logical: 1` will do), and an empty one is no
edit. A `GetWorkspace` is answered the same way.

**Between daemons.** The workspace goes in the daemon's `NodeState` to the
peers it and they let each other in, with `state` and `digest` and no
`doc`: whole when it changed since the daemon last sent it whole, and to a
peer just linked; otherwise `digest` alone, and a peer whose own differs
asks for it with `get_workspace` and `node_id`. The receiving daemon merges
it. An entry stamped further ahead of its own clock than 60 seconds came
from a node whose clock is wrong: it is left out, the rest merged, and is
merged once real time catches up, as states keep coming. A daemon whose
clock was wrong, and is put right, stamps its own such entries again. A
daemon that has never merged a peer's state counts its stamps rather than
timing them (`wall_time` 0), so that what a new node did before it joined
gives way, on the same key, to what the mesh has; what only it had is
kept. A daemon tells its watching front-ends of any change with
`changed.workspace`. It keeps the state in `workspace.pb` in its folder.

**From a node let in.** A node takes an edit of its workspace from a node
it lets in as from its own front-ends: the front-end gives `workspace`
to its daemon with `to_node`, the daemon routes it as any request (a
chain of nodes each letting in the one before carries it on), and the
node addressed stamps it with its own clock, merges it, and answers with
the state it holds after, with its `digest` while it shares the
workspace. A node let in starts sessions and runs programs there
already, so editing the workspace is no more than it can do. A peer
gives a delta only; a `doc` from a peer is refused.

This is how a node that merges no one's workspace shows and edits one: the
Android app's node lets no one in, so it shares with no computer, and the
app reads a computer that lets it in with `get_workspace` and `node_id`
and gives its edits to it. Each node puts its workspace's digest in its
own record, `Node.workspace_digest`, in the state it sends the nodes it
lets in, and a daemon tells its front-ends `changed.nodes` when a peer's
record says it changed: the front-end lists the nodes and reads the
workspace again. The digest there is a hint that it changed, never a
reason to merge; a daemon merges only what comes as described above.

**Projects.** Once the state holds projects, the daemon's projects are the
state's, as far as their roots' `locations` are on its node (keys of
`node_id`, a NUL, and the path): its agents are grouped by them, and a
front-end's `set_projects` is answered with them rather than kept.

**Sharing.** `workspace.sync` in `config.json` turns the sharing off: the
daemon then sends its workspace to no peer and takes none of theirs, while
its front-ends still edit it. It still shows it to a node it lets in that
asks (`get_workspace` with `node_id`), to copy from, but without its
`digest`, which a peer merging asks for. Left out, it is as the window's "Follow other
nodes" was set in `gui.json`: on, unless that was turned off.

**The desktop window** holds the workspace as its document, the JSON it
has always kept (`workspace.json`'s `doc`): the view of the state it last
took, which it edits, and whose edit from one to the next it gives the
daemon as a delta once its edits pause (`workspace.Diff`). A moved item's
`rank` alone is rewritten, and a root carries its `id` in the document so
that an edit of it is of the same one. A state the daemon has meanwhile is
taken up as it comes, but never over an edit of the window's: one not yet
given goes first, and the answer, holding both, is taken up. The window
gives its daemon its edits whether or not the node shares the workspace.
`workspace.json` also keeps the window's own: by space, the project whose
tabs it showed last; and while it has edits its daemon has not had, the
document they are of (`base`), so that a window started again gives them.
A window pointed at another daemon brings its workspace to none: it takes
up the new daemon's, or gives it its own when it holds nothing.

**Before revision 23.** A daemon of an older build kept the latest
`doc` it was given or sent, the later `version` winning, and shares only
those. A newer daemon sends none, which an older one, keeping only a
workspace with a `version`, leaves; and it merges no peer's `doc`, whose
edits could not be told from what it never had, but for taking one into a
state that holds nothing. So the workspace is shared only among nodes of
revision 23 and later. The `doc` a daemon kept in `workspace.pb` is taken
into its state when it first starts with one, stamped at its `version`; a
window's `workspace.json` from before, which still has its `version`, is
given to the daemon as `doc`, once, and taken in the same way, whatever
the state holds, so that nothing of either is lost.

A front-end that watches still polls now and then, as a check on a change
it missed; a daemon too old to watch answers `watch` with `unsupported`, and
is polled as before.

### Sessions

| Request | Payload | Reply | |
|---|---|---|---|
| `list_sessions` | `ListSessions` | `sessions` | The mesh's sessions; see [routing](#routing-across-the-mesh). |
| `open_session` | `OpenSession` | `session_info` | Starts an agent, a shell, or another configured program. |
| `close_session` | `CloseSession` | `exited` | Stops the session and forgets it. |
| `rename_session` | `RenameSession` | `session_info` | An empty name gives the session its own back. At most 200 bytes, folded to one line. |
| `answer_approval` | `AnswerApproval` | `session_info` | Answers the tool call the session's agent waits on. |
| `send` | `Send` | `session_info` | Types into the session without attaching: `text` pasted, bracketed when its program asked for that, then `keys` as they are, such as `\r` to submit. A `text` holding a paste's end marker is refused. With `text`, an agent that shows a view over its own prompt (Claude Code's or Codex's subagent, transcript, or agents list) is first taken back to that prompt, as its screen shows, so the text reaches the agent itself. |
| `configure_agent` | `ConfigureAgent` | `session_info` | Sets what is given of the session's agent, as `SessionInfo.controls` offers: `mode`, by the key that moves its permission mode on, pressed at its prompt until its screen names the mode; `model` and `effort`, by its own commands typed there (Claude Code's `/model` and `/effort`, which it also keeps as the user's defaults); and `fast_mode` (revision 52), toggling session/thread-scoped Codex Fast Mode via its managed app-server bridge without mutating global Codex config. Refused for a value not offered, or a mode the agent does not reach. |
| `goal_action` | `GoalAction` | `session_info` | Sets, edits, or clears a session's tracked long-running goal (`action`: `set`, `edit`, or `clear`; `objective` for `set`/`edit`). Uses `thread/goal/set` and `thread/goal/clear` on managed Codex app-server sessions and `/goal` commands at an idle prompt on Claude Code sessions. Routed by `node_id`. Revision 51. |
| `sleep_session` | `SleepSession` | `session_info` | Stops the program of a session whose agent rests, to free what it holds: running, with a `conversation_id` its agent resumes natively, not at work, waiting on nothing of the user's (`approval`, `dialog`, status `waiting`), with no background task running, no subagent at work and no session it started at work, and nothing attached to its terminal. The session stays, `stopped`, and the next `attach` starts its agent again on its conversation. Refused otherwise, saying why. Revision 21. |
| `see_session` | `SeeSession` | `session_info` | Says the user has looked at the session's attention up to `seq` (`SessionInfo.attention_seq`), as the front-end showing it saw: its `seen_seq` moves there, never past its `attention_seq`, and never back. Routed by `node_id`. Revision 36. |
| `answer_dialog` | `AnswerDialog` | `session_info` | Picks `option`, by index, of the dialog `dialog_id` the session's agent shows in its terminal (`SessionInfo.dialog`), as the user would there: the cursor moved with the arrow keys, then Enter. Refused once the screen shows another dialog, or none. |
| `answer_question` | `AnswerQuestion` | `session_info` | Answers the questions the session's agent asks in a dialog in its terminal, which no hook holds (Codex's `request_user_input`; agents with `CapAnswerKeys`): `answers`, one for each of `questions` in order, as `QuestionAnswer` (`picked` option indexes, `text` in the user's own words), typed into the dialog. Refused unless the screen shows the dialog and every question has an answer. |
| `list_commands` | `ListCommands` | `commands` | The slash commands the session's agent takes, as its node finds them: the agent's own, the user's and the project's commands and skills, and its plugins'. None for an agent mako knows no commands of. |
| `read_chat` | `ReadChat` | `chat` | The agent's native conversation, read from its log. |
| `read_project_agents` | `ReadProjectAgents` | `project_agents` | Read-only agent and message monitor for an absolute project `path` on `node_id`. Worktrees include their repository's sessions. `message_limit` defaults to 50 and is capped at 100. Each agent has its session, parent ID, messaging availability and pending message count; recent messages include sender, recipient, timestamp, text and state (`pending`, `acknowledged`, `removed`). Monitoring never consumes messages. Requires revision 15. |
| `set_projects` | `SetProjects` | `projects` | Gives `node_id`'s daemon the projects the user put together, each as its absolute folders on that node (`paths`): its agents in any of them are one project's, for the agents' MCP tools and `read_project_agents`. A folder holds its subfolders, but not a repository of its own inside it. `give_dirs` starts an agent in one folder with the others too, as an agent that takes `--add-dir` does. The front-end sends every project each time; the daemon keeps the last, on disk, until its [workspace](#workspaces) holds projects, from revision 23: from then on it answers with the workspace's, keeping none. Requires revision 16. |
| `relay_agent_message` | `RelayAgentMessage` | `agent_message` | Passes a message an agent sent over its MCP tools to an agent of its project on `node_id`, from one daemon to another. The receiving daemon keeps it in the recipient's inbox when `scope` (`project:` and the project's ID, or `repo:` and the normalized remote) is both the recipient's, there, and the sender's, as the sender's node said in its state (`SessionInfo.agent_scope`, with `agent_messaging`). |
| `relay_spawn_agent` | `RelaySpawnAgent` | `session_info` | Asks `node_id` to spawn a new agent session in a shared project (`scope`) on behalf of `parent_session_id` on `parent_node_id`. The receiving daemon verifies the caller is an active messaging agent in `scope`, validates `folder` (defaulting to the project's first folder there) and the requested `agent`/`profile`, starts the session with `parent_session_id` and `parent_node_id`, and returns its `SessionInfo`. Requires revision 48. |
| `relay_stop_agent` | `RelayStopAgent` | `exited` | Asks `node_id` to stop and remove `session_id` when it was spawned by `parent_session_id` on `parent_node_id` within `scope`. Requires revision 48. |
| `run_agent_prompt` | `RunAgentPrompt` | `agent_prompt_result` | Runs a single non-interactive prompt on `node_id`'s agent (`text` or `json` output, optionally validated by `json_schema`) and returns its output without creating a saved session. Requires revision 39. |
| `list_providers` | `ListProviders` | `providers` | Lists the local daemon's decision model providers (`openrouter`, `typesafe`, `openai`, `google`), whether each has a stored or environment key (never raw secrets), secret storage backend, and default provider/model. Local front-ends only. Requires revision 39. |
| `provider_action` | `ProviderAction` | `providers` | Sets or clears a provider API key (`set_key`, `clear_key`) or updates decision defaults (`set_defaults`) on the local daemon. Local front-ends only. Requires revision 39. |
| `run_decision` | `RunDecision` | `decision_result` | Evaluates structured decision questions (`request_json` -> `response_json`) on the local daemon using the configured decision provider. Local front-ends only. Requires revision 39. |
| `attach` | `Attach` | — | Turns the stream into a session pipe; see [Attach streams](#attach-streams). |
| `resize` | `Resize` | `session_info` | Resizes a local session's window. |
| `input` | `Input` | — | Writes to a local session's PTY. No reply. |

`resize` and `input` on an ordinary stream reach only the daemon's own
sessions and are not forwarded. Front-ends send them on an attach stream
instead, which is routed.

**`OpenSession`.** `agent` names a program the node has configured (`claude`,
`codex`, `agy`, `shell`, …) and `profile` one of its profiles there. `model`,
`effort`, and `yolo` (revision 39) override the profile's or agent's defaults
for that launch. `project`
is the working directory; empty starts in the node's home. `args` and `env` add
to the node's own. `cols` and `rows` size the PTY. With `worktree` set, the
daemon first makes a git worktree on a new `branch` in the repository
`project` is in, and starts there. The worktree starts at the commit `start`
when set, else at `base`. If both are empty, it starts at the repository's
`base_ref` from `.mako/worktree.toml`, else at `project`'s HEAD. If `base` is
empty, the daemon sets it to the local branch the worktree came off: `main`
for a `base_ref` of `origin/main`, or else the branch `project` has checked
out. If `branch` is empty, the
daemon names it after the repository's folder and a random 6-character ID,
for example `mako-k3x9q2`. The session's `worktree` reports that name.
`base` stays the branch the worktree merges and opens its pull request into.
The repository's `.mako/worktree.toml` can give a `setup_cmd`. The daemon
runs it only if the user trusts that file as it reads (Repository trust).
If not, the daemon makes the worktree without it and sets the worktree's
`skipped_hooks` to the file's absolute path (revision 53).

**`SessionInfo`** is what listings and most replies describe a session with.
The fields a front-end usually needs:

| Field | |
|---|---|
| `id`, `node_id` | The session, and the node it runs on. |
| `agent`, `profile`, `command` | What it runs. `command` leaves out what the daemon adds for itself, its hooks' settings and MCP servers. |
| `project` | Its working directory. |
| `repo` | The main work tree of the repository `project` is in, the same for its subfolders and its worktrees; empty outside one. |
| `branch` | The branch checked out in `project`; empty outside a repository. |
| `state` | `stopped` (restored, or put to sleep; starts on attach), `starting`, `running`, `exited`. |
| `exit_code` | Once exited. |
| `name`, `title` | The user's name for it, and the latest terminal title its program set. |
| `conversation_id` | The agent's own conversation, stable across restarts. |
| `busy`, `idle` | Printing on its own, as an agent at work does; nothing running in front of its program, as a shell at its prompt. |
| `status`, `status_at` | Reconciled agent activity: `idle`, `working`, `compacting`, `waiting`, `done`, from hooks, terminal progress, the screen and supported log events, worked out in one place by the daemon running the session. Running subagents defer a parent's `done` until they finish. Empty when no status is known. |
| `turns` | The turns the agent has ended: work gone `idle` or `done`, its subagents' included. |
| `state_seq`, `attention_seq`, `seen_seq` | `state_seq` numbers the changes to `status` and `approval`. `attention_seq` is the `state_seq` of the last change that asks for the user: a turn `done`, a `waiting`, a new `approval`. `seen_seq` is the attention a front-end said the user has looked at, with `see_session`. A front-end shows the session unread while `attention_seq` is past `seen_seq`, and tells the user of an `attention_seq` it had not seen before, so every front-end, on any node, shows the same. The numbers start again with the session's program (`started_at`). 0 from a daemon before revision 36. |
| `controls` | What `configure_agent` may set of the agent (`AgentControls`: `modes`, as `Usage.permission_mode` names them, `models`, `efforts`, and `fast_mode` from revision 52); unset for an agent with none. |
| `fast_mode` | Whether Codex Fast Mode (priority service tier) is currently enabled on this session; false when off, unsupported, or once the session ends. Controlled by `configure_agent.fast_mode`. Revision 52. |
| `auth_error` | Provider authentication failure that ended the session's last turn on its host (`AuthError`: `kind`, `provider`, `summary`, `guidance`, `command`, `detected_at`); unset otherwise and cleared once a later turn starts or succeeds, or once the session ends. Revision 51. |
| `goal` | The tracked long-running goal state of the session (`GoalStatus`: `source`, `status`, `reason`, `text`, `provider_status`, `observed_at`, `source_session_id`, `source_revision`, `can_edit`, `can_clear`); unset when none has been observed or once the session ends. Controlled by `goal_action`. Revision 51. |
| `dialog` | A dialog of options the agent's terminal shows while it waits there on one no hook holds, as a folder's trust or a permission asked while nobody watched (`ScreenDialog`: `id`, `text`, `options`, `selected`, the option its cursor is on); unset otherwise. Answered by `answer_dialog`. |
| `approval` | The tool call the agent waits to have approved: its `kind` and the `options` to allow it with (see Approvals). A question tool's (Claude Code's `AskUserQuestion`) carries its `questions` (`Question`: `question`, `header`, `options` of `label` and `description`, `multi_select`). |
| `worktree` | The worktree made for it. |
| `last_output`, `started_at`, `cols`, `rows` | |
| `last_message` | When the conversation's latest prompt or reply was written, as its log says. Unset before the bound log has one; clients fall back to `last_output`. |
| `agent_scope` | The project the session's agent is in, as the state a daemon sends its peers says: `project:` and the ID of the project of several folders holding it, else `repo:` and the remote of its repository, the same on every node; empty elsewhere. Agents of one project on other nodes find each other by it. |
| `agent_messaging` | Whether the session's agent takes messages over its MCP tools, as of the same state. |
| `tasks` | The commands the session's agent runs in the background, as Claude Code's Bash does with `run_in_background`, oldest first: those running and the last five that ended (`BackgroundTask`: `id`, `call_id`, `command` cut to its first line, `description`, `running`, `status` — `completed`, `failed`, `killed`, or `stopped` for one the agent's process did not outlive — `exit_code`, `started`/`ended` in Unix seconds, and `output`, the file on the session's node its output goes to). Read from the agent's log; empty once the session has ended. |

**Approvals.** An agent's `PermissionRequest` hook holds its request in the
daemon only while some front-end is watching for approvals. A front-end
watches by keeping a `watch` with `approvals` open, or by sending
`list_sessions` with `"approvals": true` at least every 15 seconds; either
counts for 15 seconds after it stops. A front-end that polls should keep the
`watch` open: a poll every 15 seconds leaves gaps, the time each poll takes,
in which an approval asked goes to the terminal, and one already shown is
taken back there. While a session waits, its `approval` is set. `answer_approval`
gives a `decision`:

| Decision | |
|---|---|
| `allow` | The tool call runs. |
| `deny` | It does not; `message` says why, to the agent. |
| `terminal` | The question goes back to the agent's own prompt in its terminal. |

An approval with `questions` is answered with `allow` and `answers`, one
`QuestionAnswer` for each question, in order: the agent takes them as the
user's answers to its own dialog. `allow` without an answer to each, as a
front-end that knows no questions sends, is taken as `terminal`; `deny`
declines to answer.

An approval's `kind` names what the call does, as `ChatMessage.kind` does,
its `input` then read as that kind's arguments: an edit's `old` and `new`, a
command's `command`. Its `options` are ways to allow the call besides once
that the agent offers (`ApprovalOption`: `mode`, a permission mode to
switch to, or `rules`, calls allowed from then on without asking, as the
agent writes them, such as `Bash(npm test:*)`); `allow` with `option` set
to one's `id` allows the call that way. A plan the agent puts to the user
(kind `exit_plan`, Claude Code's `ExitPlanMode`; its `input` has the plan's
Markdown as `plan`) is approved with `allow` and the option of the mode the
work goes on in; `allow` with no option goes on in `default`, asking before
edits; `deny` with a `message` keeps the agent planning, told what to
change.

A prompt template an agent asks to save (kind `template`, the MCP tool
`save_template`, revision 28) is the window's to keep: its `input` is
`{"name", "content", "replace"}`, the template's name and whole file, and
`detail` how it is called. Any window on a node that may answer it shows
it; the one the user allows it in saves the file in its own `templates/`
folder, refusing a name it has a template of, or a built-in's, unless
`replace` is set, and then answers `allow` with `option` `saved`. An
`allow` without that option, from a front-end that saves no templates,
saves nothing and is refused to the agent; `deny`'s `message` reaches it.
The daemon asks only while a front-end watches for approvals, here or on
a peer, and has checked the template as the window reads one.

When nobody is watching, the agent asks in its terminal, as it would without
mako.

**`ReadChat` and `Chat`.** The daemon reads the conversation bound to the
session from the agent's log (Claude and Codex). `chat.supported` is false for
an agent mako cannot read; `conversation` is empty until the agent has saved
one. `messages` are `user`, `assistant` and `tool` turns, with the oldest left
out past a size limit (`truncated`). `start` is the byte offset in the log the
messages begin at, 0 when nothing comes before them. A reader that scrolls
back pages with it: `before` set to a `start` reads the messages before it,
and `after` with `before` the ones between, which joins older pages to the
latest as the latest move on. Each read keeps the latest of what it finds,
within the same limits as the latest messages. A daemon that predates them
answers with the latest messages and `start` 0.
Each `chat` carries a `digest` of itself. A reader that polls sends the
one it has as `seen`, and when the reply would be the same it gets back
`unchanged`, with only the `digest`: a quiet conversation is not sent
again. A reader that also sets `patch` is sent, when the latest messages
changed, only what changed of the chat `seen` names: `base` is that digest,
and the messages are the base's with `drop` of them left out from the
front and the `keep` after them kept, followed by the reply's `messages`.
Every other field is whole, and `digest` is the patched chat's. A daemon
that no longer has the chat `seen` names, or predates `patch`, sends the
whole chat with `base` 0. Pages read with `before` or `after` are always
whole.
`usage` is the whole conversation's tokens:

| `Usage` field | |
|---|---|
| `input`, `cached_input`, `output` | Tokens spent, cached reads apart from fresh input. |
| `context`, `window` | The latest prompt's size, and the model's context window when the agent says. |
| `limits` | The account's subscription limits, shortest window first: `{minutes, used, resets}`, where `used` is a percentage and `resets` Unix seconds. Claude's are its five-hour and weekly windows (300 and 10080 minutes). |
| `fast_mode` | Optional boolean reporting whether the latest reply or turn ran in fast mode, when known (revision 52). |

A `tool` turn that calls a tool carries its answer in `result`; one not
answered yet is `pending`. A question call (`kind` `question`) carries the
`questions` it asks, as an approval does. `agents` are the subagents the calls in
`messages` started, as Claude's Agent tool does, oldest first, each read from
the log the agent keeps of its own:

| `SubAgent` field | |
|---|---|
| `id`, `call_id` | The agent's ID, and the call in `messages` that started it. |
| `kind`, `description` | The sort of agent, as `Explore`, and what it was asked to do, in brief. |
| `running` | Whether it is still at work: its call is unanswered, or for one run in the background, it has not said it finished. One that has written nothing for 15 minutes is taken to have stopped. |
| `tool_calls`, `tool`, `detail` | How many calls it has made, and its latest. |
| `started`, `updated` | When it began and last wrote, in Unix seconds. |

With `"usage_only": true`, the reply carries `usage` alone, read without
parsing the messages: what a front-end that only counts tokens should ask
for. A daemon that predates the flag ignores it and sends the messages too.

### Files

| Request | Payload | Reply | |
|---|---|---|---|
| `list_dir` | `ListDir` | `dir` | A directory's entries, directories first, then by name. |
| `watch_files` | `WatchFiles` | `files_changed`, a stream | Follows `paths`, files and folders, a folder for its entries, and `trees`, git work trees with all below them but for what git ignores, version control's folders and editors' temporary files, and with their git directory's `index` and `HEAD`, so that a file staged or a checkout counts as a change in the tree. The first `FilesChanged` is empty and says the node watches; each after names the `paths` changed (added, written or removed) and the `trees` something changed in, gathered as they come. Another `WatchFiles` sent on the stream takes the place of what is followed. A change to `paths` is found by a stat of each, woken by the system's notifications (FSEvents, inotify, ReadDirectoryChangesW) and run every 2 seconds besides, so a late or lost notification costs time, never a change. A tree is followed by the notifications alone, with nothing kept of its files, so what it costs does not grow with them; one outside a work tree is not followed. A tree the node cannot follow, without notifications or, on Linux, past the folders it watches, it names in `unwatched`, and the front-end looks for its changes itself until a later `WatchFiles` names it again. Routed by `node_id`. Revision 37. |
| `read_file` | `ReadFile` | `file` | The start of a file, or with `image` the whole of an image, or with `raw` a piece of any file from `offset`. |
| `write_file` | `WriteFile` | `file` | Replaces a file's contents, keeping its mode, or with an `offset` adds a piece to its end. |
| `file_op` | `FileOp` | `file` | `create`, `mkdir`, `rename` (to `to`), `copy` (to `to`, a folder with all it holds) or `delete`. None replaces what exists. |
| `search` | `Search` | `search_result` | A project's files, or the lines holding `query`. Both leave out what `.gitignore` does. |
| `stage_file` | `StageFile` | `staged` | A piece of a file the front-end has, such as one dropped on a window, kept on the node so a session there can read it. |

Paths are absolute, on the node named, except that `read_file` and
`list_dir` also take one in the node's home written `~` or `~/…`, and answer
with the path they read.
`read_file` sets `binary` and leaves `data` out for a file that is not text,
and `truncated` when `data` is only the start. Its `mod_time` is what a `write_file` hands back: a non-zero `mod_time`
in the write makes it fail with code `changed` if the file changed since.
Search matching is case-insensitive unless `query` has a capital.

`stage_file` sends a file in pieces of a few megabytes, each a request of its
own. The first gives the file's `name` and no `path`; the node keeps it under
a folder of its own in the user's cache directory (its run directory without
one), as `mako-drops/<time>-<id>/<name>`,
and answers with the `path`. The rest give that `path` and the `offset` their
`data` goes at. A staged file is at most 512 MB, and the first piece staged a
day later removes the folders older than that.

A front-end copying a file to a node, rather than handing it to a session,
makes it with `file_op` `create`, which refuses a name taken, then writes it
in pieces: the first `write_file` with no `offset` writes it whole, and each
of the rest gives the `offset` it goes at, which must be the file's length,
so a piece sent twice or lost fails. A copy back reads it with `read_file`
`raw`, a piece at a time from each `offset`, until `truncated` is unset.
A daemon from before these fields answers a piece as it would a whole
write or read, so a front-end asks only a node whose `revision` is 7 or
later, as it does before opening a tunnel.

### Tunnels

| Request | Payload | Reply | |
|---|---|---|---|
| `tunnel` | `Tunnel` | `tunneled` | Connects to a TCP `port` on the node's loopback; the stream then carries the connection. |

A tunnel reaches a server that listens only on its own machine, as a web
server in development does, from a front-end on another. The node dials
`127.0.0.1`, then `::1`, at `port`, and answers `tunneled` with the address
it reached, or an error when nothing answers there within 5 s. After
`tunneled` the stream carries the TCP connection's bytes as they are, in
both directions, with no more frames. Either side ending what it sends
closes only the other's writing half, where the connection allows, so a
program that reads to the end of a request still hears it; the stream
closes when both have ended.

A daemon relaying a tunnel to a peer opens a tunnel stream to it and copies
the bytes both ways without reading them, as it does an attach. The
desktop app listens on the same port of its own loopback when it is free,
else on any, and opens one tunnel for each connection made to it.

### Devices

| Request | Payload | Reply | |
|---|---|---|---|
| `list_devices` | `ListDevices` | `devices` | The devices attached to the node, of every kind, shared or not, with the nodes borrowing each. With `mesh`, from a front-end, also those of every node the daemon reaches at revision 10 or later, asked at once; `errors` names those that did not answer. |
| `borrow` | `Borrow` | `lease` | Has `node_id` borrow a device, held by hand: `kind` and `serial` attached to `device_node_id`, which may be `node_id` itself, or with no `serial` the one `device` names, which `node_id` resolves among the devices it reaches. A device it borrows already answers its lease again, held by hand too. |
| `give_back` | `GiveBack` | `lease` | Gives back what was borrowed by hand, by `lease_id` or `ref` (the device's ref, serial or name). The lease ends, `state` `returned`, unless sessions hold it, which it is left to; one only sessions hold is refused. |
| `list_leases` | `ListLeases` | `leases` | The devices `node_id` borrows. |
| `claim` | `Claim` | `claimed` | Between daemons: holds a device for a borrower while the stream stays open, and 30 s after. |
| `device_conn` | `DeviceConn` | `tunneled` | Between daemons: one connection to a claimed device; the stream then carries the kind's own bytes. |
| `device_listen` | `DeviceListen` | `tunneled` | Between daemons, revision 11: listens on a port of the device's node's loopback for a claim's borrower, for as long as the stream stays open; `addr` is where. |
| `device_accept` | `DeviceAccept` | `tunneled` | Between daemons, revision 11: waits for the next connection to the port at `addr`; the stream then carries it. |

A device is named across the mesh by its ref, `node/kind/serial`. The only
kind so far is `adb`.

A daemon borrowing a device sends `claim` to the device's node, with a
`claim_id` of its own making, and keeps the stream open: the stream is the
claim. The node refuses a kind its configuration does not share (its
`devices.share`), a device not attached, and a device of an exclusive kind
lent to another. It looks at the device every 5 s, and sends another
`claimed` on the stream when its state changes, with `state` `detached`
when it is no longer attached. When the stream ends the node keeps the
claim for 30 s, and a `claim` with the same `claim_id` resumes it. The
borrower then lends the device on a port of its own loopback
(`Lease.addr`), with the environment its tools find it by (`Lease.env`),
and sends each connection made to the port as a `device_conn` under the
claim, unless its side of the kind answers the connection itself. After
`tunneled` the stream carries the connection's bytes as a tunnel's does.
For adb, those are adb's protocol between a client and its server, which
the device's node filters to the device lent; see
[devices.md](devices.md#android-through-adb).

`device_listen` and `device_accept` let the device reach the borrower,
which the device's node cannot open a stream to, as adb reverse needs: the
borrower listens on the device's node with one, and waits on another for
each connection made there, opening the next as one comes. A connection
that comes with no borrower waiting waits for one, up to 16.

Every one of these is routed to `node_id` as an attach is.

A lease is held by hand, `borrowed`, and by the `sessions` of its node
started with the device; it ends when none holds it. `OpenSession.devices`
names devices, as `Borrow.device` does, that the session's node borrows
for the session before it starts; the leases' environments are added to
the session's, over `env`, and the session holds them until it ends. Two
devices whose environments set one variable apart, as two adb devices do,
are refused. `Changed.devices` tells a watching front-end that a lease or
claim of its daemon changed.

### Git

| Request | Payload | Reply | |
|---|---|---|---|
| `git_status` | `GitStatus` | `git_changes` | The work tree's changes against HEAD, or with `base` since it left that branch. Also branch, upstream, ahead and behind, the local and remote branches, the stashes, a merge, rebase, cherry-pick or revert under way (`operation`, whose unmerged files have status `conflicted`), and the session `worktree` at `path` if any (field 12, revision 46). |
| `git_diff` | `GitDiff` | `diff` | One file's hunks: against HEAD, against `base`, or as `commit` changed it. |
| `git_log` | `GitLog` | `git_history` | Recent commits of every branch and tag, newest first. |
| `git_show` | `GitShow` | `git_commit_info` | One commit's message and changed files. |
| `git_run` | `GitRun` | `git_result` | An operation, and what git printed. |

`git_log` takes the `fingerprint` of a history the caller already has as
`known`; when the refs and HEAD are unchanged, the reply sets `unchanged` and
leaves the commits out.

`git_run` operations (`op`):

| Op | |
|---|---|
| `commit` | `message`, optionally only `paths`, optionally `amend`. |
| `push`, `pull`, `fetch` | `pull` fast-forwards only, or rebases with `rebase`. |
| `checkout`, `branch` | Check out `branch`, or create it at `commit` (HEAD when empty) and check it out. |
| `track` | Check out the remote `branch` (`origin/name`) as a local branch tracking it. |
| `delete_branch`, `merge_branch`, `rebase` | Delete `branch` (unmerged too with `force`), merge it into the branch checked out, or rebase that branch onto it. |
| `discard_hunk` | Undo `hunk` in `paths[0]`, as the diff showed it. |
| `discard_files` | Put `paths` back as HEAD has them; files HEAD lacks are deleted. |
| `ignore` | Add `paths` to the root's `.gitignore`, anchored. |
| `stash`, `stash_apply`, `stash_pop`, `stash_drop` | Stash every change, untracked files too, as `message`; or apply, pop or drop stash `stash`. |
| `detach`, `cherry_pick`, `revert`, `tag` | Of `commit`: check it out on no branch, apply it, undo it as a new commit, or tag it `tag` (annotated with `message`). |
| `continue`, `abort` | Go on with the `operation` under way, adding what was resolved, or abandon it. |
| `merge`, `pull_request`, `remove_worktree` | Of a session's `worktree`: merge its branch into its base, push it and open a pull request, or remove it once nothing is left in it. Removing runs the repository's `teardown_cmd` only if the user trusts its file, and the output says so if it did not run. |

Diff lines are `{op, text, old, new}`, where `op` is `+`, `-`, a space for
context, or `@` for a hunk header, and `old`/`new` are line numbers on each
side, 0 where a line has none.

A `git_diff` with `pull` set is a file of that pull request's change, read
from its forge as the Forges section says; `commit` is then the pull request's
head as the asker saw it.

### Repository trust

A command file is a file in a repository that tells mako to run commands
for it, such as the `setup_cmd` of `.mako/worktree.toml`. Anyone who can
commit to the repository can write one. An agent that works in it can
write one too. So a node runs these commands only while its user trusts
the file as it reads now.

A trust is for one file of one repository. The node keeps it by three
values: the root of the repository with its links resolved, the path of
the file from that root (slash-separated), and the SHA-256 of the file's
content. The node keeps these in `repo-trust.json` in its configuration
directory. When the file changes, it is not trusted. The user must trust
it again as it reads then. Revision 53.

| Request | Payload | Reply | |
|---|---|---|---|
| `repo_trust` | `RepoTrust` | `repo_trust_info` | Shows, trusts, forgets or lists command files, by `op`. Routed by `node_id`. A direct SSH host keeps no trust. |

| `op` | |
|---|---|
| `""` | Show the file at `path` (absolute). Its repository is the git work tree it is in. The reply gives its `root`, `path`, `sha256` and `content` as it reads now, and `trusted` if the user trusts it as it reads now. |
| `trust` | Trust the file at `path` as it reads now. Its content must hash to `sha256`, the content the user saw. If the file changed, the node refuses. |
| `revoke` | Forget the file at `path`, or every file of the repository whose root is `path`, also when the repository is gone. |
| `list` | List every file trusted, with the hash it is trusted at. |

A front-end shows the user the whole file before it asks to trust it,
with its control characters made visible (`protocol.Printable`). The MCP
tools of agents cannot trust a file.

### Forges

The node answers for the forge of the repository at `root`: the host its
origin fetches from, else its first remote's, when that is GitHub or a
Forgejo or Gitea; else GitHub, when a remote pushes there. GitHub is asked
through its REST and GraphQL APIs, as its CLI, `gh`, asks them, which need
not be installed: the repository is found as gh finds it (the remote `gh
repo set-default` chose, else `upstream`, `github` or `origin` before the
rest), with the login gh keeps (`GH_TOKEN`, `GITHUB_TOKEN`, gh's
`hosts.yml` or the system's keyring), else the credentials git keeps for
github.com. A Forgejo or
Gitea is asked through its API at https on the host, with the credentials
git keeps for it (`git credential fill`), or none, for what is public.
`forge_items` and `forge_detail` say which answered in `forge`: `github`,
`forgejo` or `gitea`.

| Request | Payload | Reply | |
|---|---|---|---|
| `forge_list` | `ForgeList` | `forge_items` | Pull requests, issues or Actions runs (`kind`: `pr`, `issue`, `run`), `state` `open`, `closed` or `all`; of `branch`, or with `current` of the branch checked out. |
| `forge_view` | `ForgeView` | `forge_detail` | One in full: its body, comments, reviews and line comments oldest first, a pull request's files and checks, a run's jobs. Pull request 0 is the branch's, `none` when it has none. `no_files` leaves a pull request's files out. Each comment carries its author's `association` with the repository, as the forge names it, and `bot` for an app or bot account; `viewer` is the login the node reads as. |
| `forge_run` | `ForgeRun` | `git_result` | An operation, and what the forge said: a run's failed logs, or the address of what was made. |

A run's `number` is its ID. `forge_run` operations (`op`):

| Op | |
|---|---|
| `pr_create` | Push the branch checked out, its upstream set if it has none, and open a pull request of it into `base`: titled `title` with `body`, or filled from its commits without a title; a draft with `draft`. |
| `pr_checkout`, `pr_fetch` | Check pull request `number` out, or fetch its head and reply with the commit, for a worktree to start at. |
| `comment` | Comment `body` on the pull request or issue (`kind`) `number`. |
| `review` | Review pull request `number`: `review` `approve`, `request_changes` or `comment`, with `body`. |
| `run_rerun`, `run_cancel` | Run `number` again, only its failed jobs with `failed`; or cancel it. GitHub only. |
| `run_log` | Reply with the logs of run `number`'s failed steps, their last 256 KiB. GitHub only. |

On a Forgejo or Gitea a run has no jobs, a pull request in a list no
checks, and a draft is one titled `WIP:`.

### Plugins

A node's daemon runs its plugins ([docs/plugins.md](plugins.md)).
`list_plugins` and `plugin_action` reach them from the node's own
front-ends alone, and a peer is refused (revision 26): what a plugin is
allowed, and whether it runs, is the user's to say on the node it runs
on. The blocks plugins and agents draw, `watch_block`, `block_event` and
`list_agent_blocks`, are routed by `node_id` as any request, and served
to a node let in as to the node's own front-ends (revision 30): a node
let in starts sessions there already, which run any program, so seeing
a block and tapping it gives it nothing it lacked. That is how the
Android app shows the blocks of the computer it shows. `changed` has
`plugins` set when a plugin comes, goes, or changes how it stands, for
the front-end to list them again.

| Request | Payload | Reply | |
|---|---|---|---|
| `list_plugins` | `ListPlugins` | `plugins` | The node's plugins, by ID: each one's manifest (version, publisher, blocks, commands, settings and their values, a secret's never), the permissions it asks for, those allowed, those still `asked`, and its `state` (`waiting`, `denied`, `ready`, `running`, `failed`, `disabled`, and from revision 29 `blocked`) with an `error`; `stale` when the version running is the last that worked and `error` says why the folder's newer one did not. From revision 29, `installed` when it came from a registry's package, with `source`, that registry, `key`, the publisher key pinned at its first install, `update`, a newer version that registry lists signed by that key, and `blocked`, why a registry's blocklist stops it. From revision 32, `secrets`, where the node keeps its plugins' secrets: the system keychain's name, or `file` when it reaches none, with `secrets_note` saying why; and each plugin's `agent_ask`, an agent it asks to start (`sessions:write`) waiting for the user to allow it, with the agent, profile, project's name, folder, the session's name and the first 2 KiB of the prompt, and `agents_allowed` when the user let it start agents without asking. From revision 46, `bundled` (field 26) marks first-party plugins embedded in mako itself. |
| `plugin_action` | `PluginAction` | `plugin_done` | Acts for the user: `grant` allows what the plugin asks for, `deny` refuses it and stops the plugin, `enable` / `disable`, `reload` reads its folder again, `run` runs its `command`, `set` sets its setting `key` to `value` ("" unsets); from revision 32, `allow_agent` lets the agent start that the plugin's `agent_ask` of id `key` asks for, `value` `once` or `always` (for this plugin from then on, on this node), `deny_agent` refuses it, and `forget_agents` has the plugin ask again before every start. `open_url` in `PluginDone` is a link the plugin asked, in answer, to open; `toast` (revision 41) is a short notice; `copy_text` and `open` (`repeated PluginOpen`, revision 46) carry clipboard text and gesture-gated window intents (`diff`, `file`, `pr`, `runs`, `create_pr`) for the front-end that asked. |
| `watch_block` | `WatchBlock` | `block_update`, then more | Follows a plugin's block, `plugin:<id>/<block>`: the tree whole, then the changes. The block is shown, and its plugin started, while a front-end watches it; see [Plugin blocks](#plugin-blocks). `scope` selects a scoped instance (`space`, `project`, `session` from revision 42; `folder` from revision 46), `locale` passes the front-end's BCP-47 tag (revision 44), and `standalone` (field 5, revision 46) reports whether this viewer shows the block solo rather than tabbed. A block of another node, `node_id`, is relayed as an `attach` is: the daemon passes the request on and copies what comes back, in plain frames to its socket. |
| `block_event` | `BlockEvent` | `plugin_done` | The user acting on a node of a block's tree: `tap`, `change` / `submit` with the value, `drop` with the drag payload (revision 40), or `check` (`protocol.UiCheck`, revision 46) with `"on"` / `"off"`, plus `values`, the block's inputs', textareas', toggles', checkboxes', selects' and tabs' values as the front-end has them, by node id (revision 27): 32 KiB of ids and values in all, and no more values than a tree holds nodes; a value of a node the daemon's tree no longer holds is left out, while the event's own node must be the tree's. The daemon looks up what the node's last render said to send; a front-end names only the node. |
| `list_agent_blocks` | `ListAgentBlocks` | `agent_blocks` | The blocks the node's agents show (`show_ui`, [agent-mcp.md](agent-mcp.md#ui-of-your-own)), in the order shown, each its `name`, `agent:<session>/<id>`, `session_id`, `agent`, `title`, and where it goes: none, the right panel's last tab; or `document`, `parent`, `index` (-1 for the last) and `size`, which the user allowed (revision 27). `changed` has `agent_blocks` set as one comes, goes or moves. |
| `list_panels` | `ListPanels` | `panels` | The blocks the node's window layout documents place (the default first, then by name, each depth first, its compact root after its root) and then its agents' blocks, each once: its `block` name, its `title` (a plugin's manifest's, an agent's block's, the window's English for its own; `""` for a plugin's block the node lacks) and `kind`, `builtin`, `plugin`, `agent` or `missing`, a hint for its icon. Nothing else of a plugin: not its permissions, settings, registry, nor a plugin no document places. Routed by `node_id`, and served to a node let in, as `watch_block` is: what a phone lists a computer's panels by before opening one (revision 35). |

`watch_block` and `block_event` serve both kinds of block, a plugin's,
`plugin:<id>/<block>`, and an agent's, `agent:<session>/<id>`: one tree
format, one stream. An agent's block's `block_update` names its
`session_id` and `agent` in place of `plugin`, and the last update of a
block closed has `gone`. An event of an agent's block goes to the agent
as a message from `window`, whichever node's front-end sent it. A
front-end lays the agents' blocks over the layout document it draws,
never into it; they are the node's alone, never in its workspace nor its
state, and a node let in reads them by asking, with `node_id`. Allowing
what a plugin asks for stays the node's own: a node let in sees a
plugin's block `waiting`, or `denied`, and cannot answer it, as it
cannot answer a [layout change](#layout-documents) an agent asks for.

#### The plugin store

A node installs plugins from registries (revision 29): git repositories
anyone keeps, each an index of signed `.makoplugin` packages and a
blocklist, which nobody reviews ([plugins.md](plugins.md#publishing)).
Installing is each node's own: these requests come from the node's own
front-ends, and a peer, let in or not, is refused all four.

| Request | Payload | Reply | |
|---|---|---|---|
| `list_registries` | `ListRegistries` | `registries` | The registries the node reads, the one mako comes with first (`builtin`): each one's `url`, the `name` its index gives itself, when it was last read (`fetched_at`, Unix seconds, 0 for never), the `error` of the last reading, with what it gave before standing meanwhile, and how many `plugins` it lists and entries its blocklist has (`blocked`). |
| `registry_action` | `RegistryAction` | `registries` | `add` a registry by `url`, read at once (one that cannot be read is added all the same, its error beside it); `remove` one, its listings and blocklist with it, the built-in one too; `refresh` reads them all again now. |
| `search_plugins` | `SearchPlugins` | `plugin_listings` | The plugins the registries list matching `query`, by ID, name, description and publisher, or the plugin `id` alone: one `PluginListing` for each registry listing it, by ID and then registry, its newest version for this node's Host API, with the registry, the publisher's name (which nobody checked), its `key` and `fingerprint`, the permissions the index says it asks for, the version `installed` here (`dev` for a folder in development) and where from, `other_key` when this node pinned another key for the ID, and `blocked`; from revision 31, `previous_fingerprint`, for an ID not installed now that this node last installed by another key, that key's fingerprint. A registry not read in the last 10 minutes is read first. |
| `install_plugin` | `InstallPlugin` | `plugin_installed` | `install` the plugin `id` from `registry` (which may be left out when one registry alone lists it), `update` it from the registry it came from, or `remove` it with what it was allowed and kept on the node, and its pin. The reply's `added` are the permissions the version installed asks for beyond the one it replaced, which the user is then asked for as a plugin's are; from revision 31, `fingerprint`, the key's it is installed by, and for a first install by another key than the one this node last installed the ID by, `previous_fingerprint`, that key's, which a front-end says before the user is asked. A package asking for other permissions than its listing's is refused. |

An install is refused, with nothing changed, for: a package whose
SHA-256 or size is not the index's; a signature that does not hold, or is
not by the key the index lists; an ID or version other than the index's;
a manifest that does not read (one asking for `exec` among them); a key
other than the one pinned for the ID, from any registry; a version not
newer than the one installed; a version a blocklist names; and a folder of
`plugins/` written by hand under the ID, which an install never replaces.

## Attach streams

An `attach` request turns its stream into a live pipe to one session:

```proto
message Attach {
  string session_id = 1;
  string node_id = 2; // routed like any request
  bool replay = 3;    // send the current screen before live output
  bool pack_stream = 4; // between daemons: frames streamed, see Streams and framing
}
```

A session in state `stopped` is started by the attach. Closing the stream
detaches; the session keeps running.

From the daemon, as envelopes:

1. With `replay`, a **`snapshot`**: the screen's size, title and progress
   report, with its `text` left empty. The screen itself follows as
   **`output`**, in pieces of at most 256 KiB, as VT text the front-end feeds
   its terminal like live output.
2. Live PTY **`output`**, as the program prints.
3. **`exited`**, with the exit code, when the program ends. The stream ends
   with it. A failure ends it with an **`error`** instead.

If the front-end reads too slowly and the session drops it, the daemon
subscribes again and sends a fresh snapshot rather than an exit. A front-end
must accept a snapshot at any point and reset its screen to it.

From the front-end: **`input`** (bytes for the PTY) and **`resize`** (columns
and rows, and a cell's size in pixels where the front-end draws images,
zero to keep the last), each naming the `session_id`. Anything else is
ignored. The pixel size is what the PTY reports to programs that ask for
the window's size in pixels and what the daemon's terminal sizes Kitty
images placed in cells by; a styled snapshot ends with the Kitty images on
the screen and the Kitty keyboard flags the program pushed, sent again.

A `resize` may carry `colors`, the front-end's terminal colours: its
default foreground and background, the sixteen ANSI colours, and whether it
is dark. The daemon's terminal answers the program's colour queries (OSC 4,
10, 11) and color scheme queries (`CSI ? 996 n`) with them, and tells a
program that subscribed (mode 2031) when dark becomes light or back. A
`resize` of colours alone leaves `cols` and `rows` zero. A resize that
changes the size sends a program that asked for in-band size reports (mode
2048) one.

The program's bells and the desktop notifications it posts itself (OSC 9
and 777) reach the front-ends in `SessionInfo`: `bells` counts the bells,
and `notice` is the latest notification, its `seq` growing with each.

`output` and `input` carry their bytes as protobuf `bytes`, as they are,
which is why the attach stream needs no framing of its own. (The first
version of the protocol, in JSON, needed a separate binary framing for
it, since JSON carried the bytes in base64.) A 4 KiB piece of output costs
its session ID and a few bytes more on the wire.

A daemon attaching a front-end to a peer's session opens an attach stream to
the peer, then copies the bytes between the two streams in both directions
without reading them: both speak the same frames.

## Discovery

A daemon learns of its peers these ways, recorded as the node's `source`:

| Source | |
|---|---|
| `mdns` | Bonjour on the local network: service `_mako._udp`, with TXT records `id=`, `name=`, `version=` and `peer=` (its peer ID); the service's port is its libp2p port. |
| `tailscale` | The machines on the tailnet, by MagicDNS name (or the names configured), probed on the libp2p port. |
| `mesh` | A node that linked to this one, or that a peer letting this one in knows of. The config's `peers` are linked to, and recorded, this way: a host, with its libp2p port or `4243`, probed, or a multiaddr ending in its `/p2p/` ID, linked to again every 30 seconds while the link is down. |

**Probing.** An address without a peer ID, from Tailscale or the config, is
probed on its libp2p TCP port: the prober opens libp2p's own TLS handshake
(`/tls/1.0.0`) with a key of the moment, which shows the daemon's peer ID
and proves it, and hangs up. The daemon is then linked to at that peer ID,
which holds it to it, and its node record comes as the two introduce
themselves. A node found any of these ways is linked to over libp2p.

A node not seen for five minutes is dropped, unless it is still linked or
configured. A node learned from a peer keeps the `last_seen` that peer sent,
clamped to the receiver's clock, so peers passing a dead node to each other
cannot keep it alive. A daemon never adds itself.

## The mesh over libp2p

Daemons link to each other over [libp2p](https://libp2p.io). Each has a
libp2p identity, an Ed25519 key kept as `peer.key` in its configuration
directory, which its peer ID names: the node's own key. It listens on QUIC
and TCP, on port `4243` (`config.DefaultP2PPort`) unless `p2p.port` says
otherwise. A daemon links to every node it knows of, whether or not either
lets the other in.

| Protocol | |
|---|---|
| `/mako/trust/1` | The two introduce themselves, and each says whether it lets the other in. |
| `/mako/3` | The protocol's frames, one request to a libp2p stream, as on the socket. Open to the peers a daemon lets in. |
| `/mako/state/3` | A daemon's `NodeState`, compressed with zstd, one message to a stream. |
| `/mako/access/1` | A daemon asking a peer to let it in. |

The small protocols beside `/mako/3` carry length-prefixed messages (a
32-bit big-endian length, at most 1 MiB): JSON on `/mako/trust/1` and
`/mako/access/1`, a protobuf `NodeState` compressed with zstd on
`/mako/state/3`.

**Introductions.** libp2p's own handshake proves each side holds its peer
ID's key. The host that dialed then opens `/mako/trust/1`, and each side
writes, then reads:

```
{"node": "<node ID>", "name": "<node name>", "os": "darwin", "arch": "arm64", "cert": {"owner": …, "sig": …, "expires": …}}
{"trusts": true}
```

`os` and `arch` are what the host runs on, as Go names them, so even a node
that lets in neither has them. `cert` is there when an SSH key vouches for
the host (see [Access](#access)). Each side decides from the other's introduction
whether it lets the other in, and the second message says so. The two
introduce themselves again whenever either's keys change, or its
certificate is renewed, so a key let in or taken out holds at once. A host
that cannot introduce itself is hung up on.

**Requests.** A daemon reaches a peer on libp2p, dialing its `multiaddrs` if
they are not linked, and only a peer that lets it in. Until a peer is let
in, its `/mako/3` streams are reset.

**State.** Each daemon sends its `NodeState` to its peers, checking every
second and sending when its state has changed, and every 5 seconds
regardless, and to a peer at once when the two introduce themselves:

```proto
message NodeState {
  Node node = 1;                     // the daemon itself
  repeated SessionInfo sessions = 2; // its own, and those of the nodes it reaches
  bool watching_approvals = 3;       // a front-end of its answers approvals
  repeated Node peers = 4;           // the other nodes it knows of, with how it reaches them
  Workspace workspace = 5;           // its state, or its digest; see Workspaces
}
```

What a peer is sent depends on how the two stand. A peer the daemon lets in
gets it all; a peer that lets the daemon in gets the node, without its
agents, and `watching_approvals`, without the workspace; a peer with
neither gets nothing. A state
is taken only as its own node's: its record must name the peer that sent
it. A peer's workspace is taken only when the two let each other in; see
[Workspaces](#workspaces). A daemon keeps each peer's latest sessions, and answers a front-end's
`list_sessions` with those sent in the last 20 seconds. A peer's `watching_approvals` counts
as a front-end watching here when this daemon lets that peer in, so an
approval waits for an answer in a window on any node that may answer it. A
change to sessions' `last_output` alone does not send at once; the next
regular state carries it.

A daemon tells its watching front-ends (`watch`) when a peer's state
changes its nodes or sessions, so a window hears of a session started on
another machine within a second or two.

**Relays.** A node behind NAT is reached through a relay: a mako daemon
with a public address and `p2p.relay` set, whose multiaddr the others list
in `p2p.relays`. A node reserves a slot on each of its relays and gives the
relayed address in its `multiaddrs`; once linked through a relay, the two
try to punch through to a direct connection. A relay keeps slots for the
hosts it lets in alone, and joins two hosts only when it lets both in. It
does not limit a circuit's length or data, as a session may run over one for
hours.

```json
"p2p": {
  "port": 4243,
  "relay": false,
  "relays": ["/ip4/203.0.113.7/udp/4243/quic-v1/p2p/12D3KooW..."]
}
```

`p2p.disabled` leaves a daemon without peers.

## Compatibility

The libp2p protocol IDs name the framing. Nodes whose names
differ fail the handshake and do not see each other, so every machine of a
mesh moves to a new name together. That has happened three times:

- **JSON to protobuf.** The first version framed JSON envelopes. Version 2
  frames protobuf.
- **sanbo to mako.** The project was called sanbo, and its builds speak
  `sanbo/2`, `/sanbo/auth/1` and `_sanbo._udp`. A mako build speaks the same
  protocol under mako's names, and does not see them. Its daemon moves an
  existing `sanbo` configuration directory to `mako` on its first run,
  keeping its node ID, key and identity.
- **A shared key to keys of their own, and a socket.** The first mako builds proved a
  mesh-wide pre-shared key on `/mako/auth/1` and published their state on
  a gossipsub topic. Builds since speak `/mako/trust/1` and
  `/mako/state/2` instead: the two link, but never introduce themselves,
  and are hung up on. Front-ends reached their daemon on QUIC, UDP port
  `4242`, with ALPN `mako/2` and a token; they now use its socket.
- **Compressed frames.** Builds since revision 12 compress the larger frames
  and the state between daemons, and speak `/mako/3` and `/mako/state/3`
  for it, in place of `/mako/2` and `/mako/state/2`: an older build links,
  but neither asks the other anything.

The names change again only for a break that no addition can bridge.

Within version 2, the protocol changes by addition, as protobuf allows:

- **Fields** are added with new numbers, never reusing or renumbering one.
  A reader skips fields it does not know, and a missing field reads as its
  zero value, which must mean what the older behaviour did.
  `buf breaking --against '.git#branch=main'` checks a schema change against
  the last committed one.
- **Message types** are added as new fields of the envelope's `body`. A
  daemon answers one it does not know with code `unsupported`.
- **Features** (`Node.features`) name extensions a peer must speak before
  they are used on it. None are defined in version 2 yet.
- **Revision** (`Node.revision`, `protocol.Revision`, currently 53) counts what
  a build's daemon reports. A front-end that finds a daemon on a lower
  revision tells the user it is out of date, rather than leaving the newer
  parts blank. Bump it when a daemon starts reporting something a front-end
  shows.

## Limits

| Limit | Value |
|---|---|
| Frame | 8 MiB |
| `read_file`, text | 4 MiB, or `max` if smaller |
| `read_file`, image | 5 MiB |
| `read_file`, `raw` piece | 4 MiB, or `max` if smaller |
| Replay screen piece | 256 KiB |
| Conversation log read for `read_chat` | 2 MiB before `before` (or the end), at most 200 messages |
| `stage_file` | 512 MiB a file |
| `search`, files listed | 50 000 |
| `search`, matches | 1 000, 100 per file, 300 bytes of each line |
| `search`, time | 20 s |
| Session name | 200 bytes |
| Git operations | 2 minutes |
| Forwarded peer requests | 5 s to dial |
| `tunnel` | 5 s to reach the port |
| `list_devices` with `mesh` | 5 s for each node |
| A device's claim | 30 s after its stream ends |
| Approval watch | 15 s after the last `list_sessions` with `approvals` |
| Introductions on libp2p | 10 s |
| A message on `/mako/trust/1`, `/mako/state/3`, `/mako/access/1` | 1 MiB |
| Access requests waiting | 32, each note 500 bytes |
| An SSH key's certificate | 30 days, renewed a week before |
| A peer's sessions | taken as current for 20 s after its state |
| Relinking a peer on libp2p | at most every 30 s |
| Peer expiry | 5 minutes unseen |
| Hook report | 1 MiB |
| A plugin block's tree | 2 000 nodes, 24 deep, 512 KiB of JSON, 256 KiB of text (8 KiB a node, 32 KiB a markdown or code node) |
| A plugin block's updates | one each 100 ms on a stream, the renders between sent as one change |
| A plugin's events | 20 a second from the front-ends, a value 8 KiB |
| An agent's blocks | 4 a session, updated 10 times a second, 20 events a second each; an event's values 32 KiB |
| A registry's index | 4 MiB, 20 s to read; 5 000 plugins, 64 versions each, 5 000 blocklist entries; 32 registries a node |
| A plugin package | 8 MiB, 60 s to download, 3 redirects, each https; 256 files, 2 MiB each and 8 MiB in all once read, 8 folders deep |

## Layout documents

Two JSON documents lay a window out. Each is carried whole, as the `doc` of
a `LayoutEntry` in the workspace, so that every front-end reads the same.

**Pane trees** (`WorkspaceState.layouts`, by `spaceID|context`) are the
tab groups of the panes and their splits: a node is a pane when it has a
`Group`, `{"ID": 1, "Tabs": ["<tab>", …], "Active": "<tab>"}`, or else a
split of `First` and `Second`, beside each other or, with `Vertical`, one
over the other, `First` taking `Ratio` of the room. Every node has an `ID`.

**Window layout documents** lay out the whole window: the sidebar, the
workspace and the panels, and where the pane tree goes. They are named,
in `WorkspaceState.window_layouts[name]`, one register each: of two
nodes' edits at once the later wins whole, and a document's revision is
its entry's `clock`, written `wall_time.logical.node_id`. A space's
`layout` names the document it shows; one that names none shows
`default`'s, and while there is no `default`, the window's built-in
document below. The window edits them as it edits the rest of the
workspace, through its document (`window_layouts` and each space's
`layout`). The desktop window takes in a `layout.json` beside `gui.json`,
the one document it read before revision 24: once, as `default`, when
the first workspace it takes holds none, the file then renamed
`layout.json.imported`; and again on the palette's *Reload window
layout*, as the document of the space shown. *Reset window layout* has
the space shown use `default` again, or, using it, removes `default`, so
that the built-in document shows; it takes effect in the window at once,
whether its daemon is reachable or not. The built-in document:

```json
{"version": 1, "root": {"id": "window", "row": [
  {"id": "sidebar", "block": "mako.sidebar", "size": 272, "collapsible": true},
  {"id": "workspace", "column": [
    {"id": "header", "block": "mako.header"},
    {"id": "main", "panes": "main", "grow": 1}
  ], "grow": 1},
  {"id": "details", "stack": ["mako.files", "mako.git", "mako.agents", "mako.forge"],
   "size": 320, "collapsible": true}
]}}
```

`version` is 1, or 2 for a document with `variants` (below). A node is
an object with one of these fields, its kind,
and an `id`, unique in the document, of letters, digits and `._:/-`, by
which everything that edits or remembers a node finds it:

| Kind | Holds | Other fields |
|---|---|---|
| `row` | its children, side by side | `id`, `gap`, and those of a child |
| `column` | its children, one over another | `id`, `gap`, and those of a child |
| `stack` | blocks over each other, one shown, picked by a strip of tabs | `id`, `active`, the index of the one shown until another is picked, and those of a child |
| `block` | the name of a registered block | `id`, by default the name; `props`, which the block's schema checks; those of a child |
| `panes` | `"main"`: the pane tree, held apart | `id`, by default `main`; `size`, `grow` |

A row's or column's child takes `size`, its width in a row or height in a
column, in pixels, with an edge toward the growing child that drags to
resize it, or `grow`, a share of the room the others leave; neither, it is
as large as it draws. A sized child that is `collapsible` slides shut and
open again. Two children neither of which is sized are divided by a line.
In a list of children, a block's name alone, such as `"mako.git"`, is a
block of that name and `id`. A document's root holds one `panes` node.

A block's name is `mako.<name>` for the window's own, and will be
`plugin:<id>/<block>` and `agent:<session>/<id>`. The window's:
`mako.sidebar`, `mako.header` (the session's title, menus and the panels'
toggles over the panes), `mako.files`, `mako.git`, `mako.agents` and
`mako.forge`, none taking props. A block a window does not hold is drawn
as missing in its place, and kept. A document that does not read, with a
value of the wrong type, sizes where they mean nothing, no `panes` or
two, or more than 256 nodes or 16 deep, is not used: the window draws its
default.

**Versions.** A reader reads a document of its own `version`, or an
earlier one, strictly: a field or kind of node it does not know at that
version is an error. One of a later version, which a newer node wrote
and the workspace brought, is read leniently: fields it does not know
are left out, and a node of a kind it does not know is kept as a leaf,
drawn as missing; what it does know is checked as strictly. A node
writes documents of its own version at most, each at the earliest
version that holds it, and does not patch one of a later version. So a
field added later must be one an older window can leave out, and
anything it cannot is a new version.

**Variants** (version 2, revision 30). A device whose screen is narrow,
the Android app, does not lay the window out: it shows blocks a page
each. A document may say which, in `variants`:

```json
{"version": 2, "root": {…},
 "variants": {"compact": {"id": "phone", "column": [
   {"id": "ci", "block": "plugin:ci-status/ci"}, "mako.git"]}}}
```

`compact` is a root of the same nodes, checked as the root is, its IDs
unique within it (it may use the root's again), but for the panes, of
which it holds one or none. A document with variants is written at
version 2, one without at version 1, so a window of a build before
revision 30, which reads version 1 strictly, reads a document with
variants leniently, as a later version: it leaves `variants` out and
draws the root as ever, and does not patch the document. A window draws
`root`, and `patch_layout` changes it alone; the compact root changes
with the document whole, as the user writes it: an agent's `set_layout`
keeps the compact root the document has and may not bring another
([agent-mcp.md](agent-mcp.md)), since the window asking the user to allow
a change draws the root alone. A later version's variant a reader cannot read,
or that does not check, is left out, as any field it does not know.

The **pages** of a document are those of its compact root, or, without
one, of its root folded: its blocks in document order, depth first, a
row's or column's children in turn and a stack's blocks in the order of
its tabs; but for the panes, which a narrow screen shows in its own way,
the window's own frame (`mako.sidebar`, `mako.header`), and nodes of a
kind the reader does not know. A size, `grow`, collapsing and a stack's
`active` mean nothing on a page. A document a window would not draw has
the default's pages, as the window draws the default (`internal/layoutdoc`,
`Pages`). The app shows those it can draw, plugins' blocks, and after
them the agents' blocks of the same node (`list_agent_blocks`): it has
screens of its own for the window's other blocks. The desktop window lists the
pages of the document of the space it shows in Settings ▸ Spaces, *On a
phone*, as the phone lists them, and lets the user move a page, hide one
or show it again: the window writes the order as the document's compact
root (`{"id": "phone", "column": [...]}`, its pages' own IDs), an edit of
the workspace as any other of the user's, and takes the compact root
away again when the order is the root's own (*Use the window's order*).

**Changes an agent asks for** (revision 24) go through the user, on the
node whose daemon the agent asked (see [agent-mcp.md](agent-mcp.md#the-windows-layout)):

- The daemon stages the change, one at a time, in its memory alone: it is
  in `Workspace.stage`, a `LayoutStage`, of the answers the daemon gives
  its own front-ends, never in `state`, never to a peer, and the daemon
  tells its front-ends with `changed.workspace` when it comes, moves on
  or goes. A window shows `doc` in place of the document `document`
  while the space shown uses that one, and a bar over the window, which
  it draws itself, outside any document.
- In phase `approve`, unless the user allowed such changes before, the
  agent's session holds an `Approval` of kind `layout` and the stage's
  `id`, with options `space` and `always`; it is answered with
  `AnswerApproval`, as any is, but from a front-end of that node only.
  `deny` (or `terminal`) turns it down. `space` allows, from then on,
  changes of that document without asking, `always` of any; the daemon
  keeps them in `layout-allow.json`, shares them with no node, and
  forgets them on `LayoutDecision{decision: forget}`.
- A whole document set (`set_layout`) is then in phase `keep` until
  `deadline`: the window asks whether to keep it, and answers with
  `LayoutDecision{stage_id, decision: keep}` or `revert`; at the
  deadline it reverts by itself.
- Only then is the change written into the state, as an edit of the
  daemon's, and so sent to the peers; a change turned down or reverted
  never was. It is refused if the document changed since it was read.
- A front-end that shows the stage says so in `Watch{layouts: true}`;
  while none watches, a change the user is to see is refused at once.
- **A plugin's change** (`layout:write`, revision 32) takes the same
  flow, `patch_layout`'s alone: the stage has `plugin_id` and no
  `session_id`, `agent` naming the plugin for an older window. No session
  holds an approval of it: a front-end allows or denies it with
  `LayoutDecision{stage_id, decision: allow|deny, option}`. What the user
  allows a plugin, `space` or `always`, is that plugin's alone, apart from
  what agents and other plugins are allowed (`layout-allow.json`'s
  `plugins`), forgotten with the plugin or its permissions, and on
  `forget`. A plugin, as an agent, changes the root alone, never
  `variants`.

A daemon keeps, for each document, the versions this node's edits
replaced, the user's or an agent's, 20 of them, which `undo_layout` goes
back to; it shares them with no node.

A `size` is a default. What a device makes of the document stays on it,
by node ID: the sizes its edges were dragged to (`gui.json`'s
`sidebar_width` and `details_width` for the nodes `sidebar` and `details`,
and `layout_sizes` for the rest), which nodes are shut, and the tab a
stack shows. The window's toggles and shortcuts for the sidebar and the
details panel act on the nodes of those IDs.

## Plugin blocks

A plugin draws a block, and an agent shows one, as a tree of `UiNode`s, of a fixed vocabulary:
`column`, `row`, `stack`, `card`, `scroll`, `wrap`, `grid`, `split`,
`collapsible`, `tree`, `tree_item`, `list`, `table`, `menu`, `dialog` (which hold children),
`breadcrumb`, `text`, `icon`, `pill`, `chip`, `button`, `input`, `textarea`,
`toggle`, `checkbox`, `select`, `tabs`, `markdown`, `code`, `progress`,
`divider`, `menu_item`, `chart` (revision 33; `donut` and `pie` from revision 44),
`graph` (revision 44), and `dag` (revision 46). A colour is a token (`tone`: `muted`, `faint`,
`accent`, `success`, `warning`, `danger`), never a value; the front-end
draws each kind with its own components in its own theme. The daemon reads
what the plugin's render returned strictly, and refuses a tree with an
unknown kind or field, a tone not a token, more nodes or depth than
[Limits](#limits) allows, or text holding control or reordering
characters; the block then shows the error, and the last tree that read.

Every node has an `id`, its path from the root by its own and its
ancestors' keys (a child without a key has `#` and its index): `""` for the
root, `/runs/1234/#0` for the first child of the child keyed `1234` of the
child keyed `runs`. A node that answers a tap has `tap`; one whose value
changing (or a `markdown` link click, `breadcrumb` segment click, `graph`
node click, or `collapsible`/`tree_item` disclosure toggle) is answered has
`change`. What it sends is the daemon's to know.

A `chart` node (revision 33) has `chart`, `line`, `bar`, `sparkline`, or
(revision 44) `donut` / `pie`; `series`, 1 to 4 `UiSeries` of a `name`, a
`tone` and 1 to 200 finite `values` (600 in all; for `donut` and `pie`, 1
series of 1 to 16 non-negative values with matching `labels`); and `labels`,
the points' or slices' names. Its `text` is a caption. It sends nothing. A
front-end that does not know a kind draws nothing in its place, so a
daemon may send newer kinds to an older window or phone.

Revision 44 adds rich layout fields on `UiNode` (`variant` for `button`/`card`,
`columns` 1..6 for `grid`, `ratio` 0.1..0.9 and `direction` for `split`, and
`fill` on root `column`/`row`/`split` to skip outer auto-scroll), interactive
`markdown` links (`change = true` firing `BlockEvent{kind: "change", value: target}`),
declarative `graph` canvases (`graph_nodes` of `UiGraphNode` up to 120,
`graph_edges` of `UiGraphEdge` up to 400, optional `max_height` 120..1200,
`value` focused node ID, `change = true` firing clicked node ID), and optional
BCP-47 `locale` fields on `WatchBlock` (field 4), `BlockEvent` (field 8), and
`PluginAction` (field 7) for plugin localization (empty defaults to `"en"`).

Revision 45 (`RevisionPluginAssist`) adds `PluginAgentAsk.kind` (field 9: `""` for
`sessions:write` agent starts, `"ask"` for `agents:ask` one-shot questions),
expands `UiNode.variant` tokens per node kind (`card`: `"soft"`; `pill` and
`chip`: `"soft"`, `"solid"`, `"outline"` with optional `icon`; `progress`:
`"spinner"` with optional `text`; `scroll`: `"follow"`; `tree_item`: `"badge"`),
and adds the `decision` and `agents:ask` Host API services with `SessionEvent`
fields 9–14 (`output`, `truncated`, `response_json`, `elapsed_ms`, `tag`, `code`).

Revision 46 (`RevisionPluginGitAndChrome`) adds standalone panel chrome, bundled
first-party plugins, the `folder` scope, gesture window intents, and the Git and
Forge plugin Host APIs:
- **Standalone panel & chrome**: `WatchBlock.standalone` (field 5, bool) signals
  that the viewer displays the block solo rather than tabbed; the runtime
  aggregates `standalone` with logical AND across active viewers of the same
  `(block, scope)` instance and re-renders on transitions. `BlockUpdate.chrome`
  (field 14: `"auto"` or `"toolbar"`) carries the block manifest's chrome hint.
- **Bundled plugins & folder scope**: `PluginInfo.bundled` (field 26, bool) marks
  plugins embedded in mako itself. `ScopeFolder` (`"folder"`) isolates blocks and
  settings to one folder on one node using the canonical scope key
  `folder:<node>/<base64url-nopad(path)>` (`<node>` is `"local"` or a peer node ID).
- **Window intents**: `PluginDone` adds `copy_text` (field 3) and
  `repeated PluginOpen open` (field 4), where `PluginOpen` carries `kind`
  (`"diff"`, `"file"`, `"pr"`, `"runs"`, or `"create_pr"`), `node`, `folder`,
  `repo`, `path`, `from`, `status`, `added`, `removed`, `binary`, `commit`,
  `base`, `number`, `preview`, and `line`.
- **UIBlock extensions**: `UiNode` adds fields 34–40: `repeated UiBadge badges`
  (34; `UiBadge{text, tone, variant, icon}` with `variant` in `"soft"`, `"solid"`,
  `"outline"`, `"mono"`), `string check` (35: `""`, `"on"`, `"off"`, `"mixed"`),
  `bool checks` (36), `string context_menu` (37), `string context_arg` (38),
  `repeated UiRow rows` (39; `UiRow{id, parents, text, detail, meta, badges, tone, strong, icon}`),
  and `string placeholder` (40: input/textarea hint or tooltip/accessible label on
  buttons, menus, `tree_item` checkboxes, checkboxes, icons, pills, and chips).
  New kinds are `menu` (`variant`: `""`, `"ghost"`, `"outline"`, `"context"`),
  `menu_item`, `checkbox`, `dialog` (`variant`: `"danger"`), and `dag`; `row`
  gains `variant: "toolbar"`, and `split` gains `direction: "auto"`.
  `BlockEvent.kind` adds `UiCheck` (`"check"`, value `"on"` or `"off"`) for
  toggling a `tree_item`'s check state; context-menu `menu_item` taps carry the
  opener's `context_arg` (or a `dag` row's `id`) in `BlockEvent.value`, verified
  against the tree's active openers.
- **Git & Forge**: `GitChanges.worktree` (field 12) attaches the session
  `Worktree` at `path` when present. The plugin Host API adds `GitService`
  (`git:read`, `git:write`) and `ForgeService` (`forge:read`).

`watch_block` answers with a `block_update` holding the `tree`, then one
whenever the block changes, holding `ops` that make the tree the last
update left into the new one, in order:

| `op` | |
|---|---|
| `replace` | `node` in place of the node of `id` (the root's, `""`, too) |
| `update` | the fields of the node of `id` set to `node`'s, its children kept |
| `insert` | `node` as the `index`-th child of the node of `parent` |
| `remove` | the node of `id` taken out |
| `move` | the node of `id` moved to be the `index`-th child of its parent |

A child is matched by id, so a keyed child that moves among its
siblings is moved, not drawn again. An update with neither `tree` nor
`ops` changes only how the block stands: `plugin` and `title`, which the
front-end frames the block with, `state`, `error` and `stale`, as in
`list_plugins`. A front-end that cannot apply an op starts the stream
again, for a whole tree. Updates come at most every 100 ms; renders in
between go as one change. A front-end of another node watches and acts
on a block with `node_id` set, through its own daemon (revision 30); the
Android app applies the ops with a port of the daemon's own
(`protocol.ApplyUi`), checked against what the Go makes.

## The local hook socket

Separate from the mesh, a daemon listens on a Unix socket in its private run
directory, `hooks.sock`, for the agents it runs. It gives each Claude
and Codex session hooks that run the daemon's own program as
`<program> hook <socket>`, and gives Claude a status line that runs as
`<program> statusline <socket>`. Each run makes one connection and sends one
JSON object:

```json
{"session": "<MAKO_SESSION>", "seq": 1761475200000000000, "kind": "", "input": { ...the agent's own hook input... }}
```

`seq` is when the hook ran, in Unix nanoseconds, so a report that arrives
late changes nothing. `kind` is `statusline` for the status line's input, from
which the daemon takes the context window and the account's rate limits, and
empty for a hook event. Only a `PermissionRequest` gets an answer back on the
same connection, once the user decides:

```json
{"decision": "allow", "message": ""}
```

An empty decision leaves the question to the agent's own prompt. The status
line forwards its input and then prints the user's own status line, so
mako's does not take its place on screen.

The socket is reachable by the user's own processes only and is not part of
the network protocol.

## Access

Trust runs one way. A daemon lets a peer in when the peer's key is in its
`authorized_keys` file, in its configuration directory, in OpenSSH's format:
the peer's own key, or the SSH key that vouches for it. An SSH key in the
user's own `~/.ssh/authorized_keys`, one that may log in to the machine,
lets in the peers it vouches for too. A daemon serves the
requests of the peers it lets in, so a peer it lets in can start programs and
read and write files on its machine; that peer must let it in for the
reverse.

**Vouching with an SSH key.** With `ssh_key` set to an SSH public key (left
out, the first of `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub` and `id_rsa.pub`), a
daemon has the key's private half, in the SSH agent or beside it without a
passphrase, sign a certificate that its peer ID is the key owner's:

```
"mako-node-cert/1" 0x00 <peer ID> 0x00 <expiry, Unix seconds, decimal>
```

The certificate holds for 30 days and is signed again a week before it
ends. A daemon lets in a peer whose certificate holds and whose owner's key
is in its `authorized_keys`, and a peer vouched for by its own owner: the
machines one SSH key vouches for let each other in without a word.

**Lines in `authorized_keys`.** Lines that do not parse are skipped, as are
keys bound to a command (`command=`, `restrict`), which a mako link cannot
honour. Of `~/.ssh/authorized_keys`, a line with any options at all
(`from=`, `no-pty`, …) is skipped, and mako only reads it; its keys are
listed in `access` with the file as their `source`, and taken out there. Both files are read again when they change.

**Asking to be let in.** A daemon a peer does not let in may ask it to, on
`/mako/access/1`, with a short note. The peer keeps the request, in memory,
for a front-end of its own to allow, by the asker's own key or by the SSH
key vouching for it, or to deny. At most 32 requests wait; a new one past
that drops the oldest.

**`access`.** A front-end lists and changes who its daemon lets in with
`access`; a peer may not send it. `op` says what to do:

| `op` | |
|---|---|
| `""` | Nothing: the reply says where things stand. |
| `request` | Ask `node_id` to let this node in, saying `note`. |
| `allow` | Let in `node_id`, which asked: by its own key, or with `owner` by the SSH key vouching for it. |
| `deny` | Drop `node_id`'s request. |
| `add` | Let in `key`, an `authorized_keys` line. |
| `revoke` | Take out the key whose fingerprint is `key`. |
| `relink` | Try the link to `node_id` again now, rather than when its wait ends. |

A link that drops is tried again at once, and one that fails then waits
2 seconds, twice as long each time it fails again, up to 30; `list_nodes`
says how each stands in `link`, `link_error` and `relink_at`.

The reply, `AccessInfo`, gives this node's own key and its owner's, each with
the line another node's `authorized_keys` takes, the requests waiting, and
the keys let in.

## Security

A front-end reaches its daemon on the daemon's socket, which only its user
can open: anything running as that user can drive the daemon, and so the
mesh as far as the daemon is let in.

Between daemons on libp2p, each side is held to its peer ID, and a peer is
let in by its key alone: a host whose key is not let in gets nothing from
a daemon, no requests, no sessions, no relay. Keys are let in one way, and
taken out at once. The libp2p port may face the internet.

Letting a key in hands its holder the machine: anyone it lets in can start
programs and read and write files there, and reach any port of its
loopback through a tunnel, even one a server there keeps from the network.
Letting a node in lets in, through it,
every node it lets in, and so on along the chain. A daemon routes through a
peer on that peer's word of what it reaches, so a node that lets yours in
sees what yours sends the nodes it says it reaches, and answers for them.
Let in the keys of your own machines, and an SSH key only when its private
half is kept safe.
