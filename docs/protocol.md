# mako protocol

This is the wire protocol spoken between mako front-ends (the `mako` CLI and
the desktop app), the daemon they connect to (`makod`, or the one the app
runs itself), and the daemons of the other machines on the mesh. Its messages
are protocol buffers: the schema is [`proto/mako/v2`](../proto/mako/v2),
and this document describes how the messages are used.

## At a glance

| | Front-end ↔ daemon | Daemon ↔ daemon | Daemon ↔ older daemon |
|---|---|---|---|
| Transport | QUIC, UDP `4242` | libp2p (QUIC or TCP), port `4243`, maybe relayed | QUIC, UDP `4242` |
| Stream protocol | ALPN `mako/2` | `/mako/2` | ALPN `mako/2` |
| Access | the key in `hello` | the key proved on `/mako/auth/1` | the key in `hello` |
| Frames | length-prefixed protobuf `Envelope`s | the same | the same |
| Pushed state | `watch` → `changed` | the `mako/state/1` topic | `gossip`, every 30 s |

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
- [Discovery and gossip](#discovery-and-gossip)
- [The mesh over libp2p](#the-mesh-over-libp2p)
- [Compatibility](#compatibility)
- [Limits](#limits)
- [The local hook socket](#the-local-hook-socket)
- [Security](#security)

## Roles

```
 front-end ──QUIC──▶ daemon A ──libp2p──▶ daemon B
 (CLI, app)          (local)   (QUIC to a  (peer)
                                peer from
                                an older
                                build)
```

- A **front-end** connects to one daemon, normally the one on its own machine,
  and does everything through it.
- A **daemon** runs the sessions of its machine and answers for them. When a
  request names another node, it forwards the request to that node's daemon
  and relays the answer, so one connection reaches the whole mesh.
- A **peer** is another daemon. Daemons find each other by discovery and by
  gossip, and talk to each other with the same protocol, marked as peers in
  their handshake.

## Transport

| | |
|---|---|
| Protocol | QUIC over UDP |
| Default port | `4242` (`config.DefaultPort`) |
| ALPN | `mako/2` (`protocol.ALPN`) |
| TLS | 1.3, with a self-signed certificate per node |
| Idle timeout | 60 s, with keep-alives every 15 s |
| Initial packet size | 1200 bytes |

Each daemon mints an ECDSA P-256 certificate on its first run and keeps it in
its config directory (`node.crt`, `node.key`). Dialers do not verify it:
identity comes from the [handshake](#handshake), and access from the
pre-shared key. The initial packet size is QUIC's minimum so that the
handshake fits a 1280-byte MTU, as Tailscale's interface has; path MTU
discovery grows it where the path allows.

A front-end keeps one QUIC connection to its daemon. Daemons link to each
other over libp2p (see [The mesh over libp2p](#the-mesh-over-libp2p)); a
daemon keeps one QUIC connection to a peer without libp2p, an older build,
dialed on demand and reused.

## Streams and framing

Every exchange happens on a bidirectional stream that the caller opens: a
QUIC stream, or a libp2p stream of `/mako/2` between daemons.
A stream carries **frames**, each one protobuf `Envelope` with a length
prefix:

```
┌──────────────────────┬──────────────────────────────────┐
│ length: uint32, BE   │ Envelope: protobuf, length bytes │
└──────────────────────┴──────────────────────────────────┘
```

A frame is at most 8 MiB (`protocol.MaxFrameSize`); a reader refuses a larger
length before allocating. The `Envelope` is an `id` and a `body` oneof, one
field for each message type:

```proto
message Envelope {
  string id = 1;
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
hello   { node_id: "", name: "mako-cli", version: "v0.3.0", is_peer: false, auth_tag: "the-psk" }
welcome { node: { ...the daemon's Node... } }
```

| `Hello` field | |
|---|---|
| `node_id` | The caller's node ID. Empty from a front-end. |
| `name` | The caller's name: a daemon's node name, or `mako-cli` from the CLI and the app alike. |
| `version` | The caller's build. |
| `is_peer` | Set by a daemon. It changes how the daemon answers `list_sessions`, and records the caller as an inbound peer. |
| `auth_tag` | The mesh's pre-shared key, when one is configured. Left empty on a libp2p stream, whose link has proved the key already. |

If the daemon has a pre-shared key and `auth_tag` differs, it answers with an
`error` ("authentication failed") instead; a libp2p stream is not checked
here, as only a host that proved the key can open one. A first frame that is not a
`hello` gets an `error` too. In either case the stream is then done.

When the hello is from a peer (`is_peer` with a `node_id` other than the
daemon's own), the daemon adds that node to its peer table with source
`inbound`: over QUIC at the caller's IP and the daemon's own service port,
over libp2p at the caller's peer ID and address.

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

A daemon forwarding a request to a peer passes the peer's `code` back
unchanged. The message gains a `remote: ` prefix at each hop.

An `error` does not end the stream: the daemon goes on reading requests.

## Routing across the mesh

Most requests have a `node_id`:

- **Empty, or the daemon's own ID:** the daemon serves the request itself.
- **Another node's ID:** the daemon opens a peer stream to that node, sends
  the request with `node_id` cleared, and relays the reply, or the error.
  A node it does not know fails with "unknown node".

Forwarding is one hop. A daemon only forwards to nodes in its own peer table,
which [gossip](#discovery-and-gossip) fills with the whole mesh.

`list_sessions` is not routed by node. A daemon asked by a front-end answers
with its own sessions and those of every known peer. For a peer on libp2p
it uses the sessions the peer [published](#the-mesh-over-libp2p) in the last
20 seconds. Any other peer is asked in turn, with a peer hello. A daemon
asked by a peer answers with its own sessions only, so the fan-out never
recurses around a cycle. Sessions a peer reports for any node other than
itself are dropped.

`list_nodes`, `gossip` and `watch` are answered by the daemon itself.

## Message reference

Requests are grouped by area. The reply column is the `type` of the frame
that answers on success.

### Mesh

| Request | Payload | Reply | |
|---|---|---|---|
| `list_nodes` | `{}` | `nodes` | The daemon itself, then its peers. |
| `gossip` | `Nodes` | `nodes` | A peer's view of the mesh; the reply is the daemon's peers. Sent between daemons, to a peer without libp2p. |
| `watch` | `{}` | `changed`, then more | Turns the stream into a feed: a `changed` at once, then one whenever the mesh's nodes or sessions change, until the front-end closes the stream. |

```proto
message Node {
  string id = 1;
  string name = 2;
  string addr = 3;
  string os = 4;
  string arch = 5;
  string version = 6;
  string source = 7; // mdns, tailscale, static, inbound, gossip, self
  google.protobuf.Timestamp last_seen = 8;
  repeated string features = 9; // protocol extensions it speaks
  repeated AgentInfo agents = 10; // what it can launch: {name, profiles}
  int64 revision = 11; // protocol.Revision of its build
  string peer_id = 12; // its libp2p identity; empty without libp2p
  repeated string multiaddrs = 13; // where it is on libp2p, relayed addresses included
}

message Changed {
  bool nodes = 1;    // the nodes changed: ask list_nodes again
  bool sessions = 2; // the sessions changed: ask list_sessions again
}
```

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
| `read_chat` | `ReadChat` | `chat` | The agent's native conversation, read from its log. |
| `attach` | `Attach` | — | Turns the stream into a session pipe; see [Attach streams](#attach-streams). |
| `resize` | `Resize` | `session_info` | Resizes a local session's window. |
| `input` | `Input` | — | Writes to a local session's PTY. No reply. |

`resize` and `input` on an ordinary stream reach only the daemon's own
sessions and are not forwarded. Front-ends send them on an attach stream
instead, which is routed.

**`OpenSession`.** `agent` names a program the node has configured (`claude`,
`codex`, `agy`, `shell`, …) and `profile` one of its profiles there. `project`
is the working directory; empty starts in the node's home. `args` and `env` add
to the node's own. `cols` and `rows` size the PTY. With `worktree` set, the
daemon first makes a git worktree on a new `branch` from `base` in the
repository `project` is in, and starts there.

**`SessionInfo`** is what listings and most replies describe a session with.
The fields a front-end usually needs:

| Field | |
|---|---|
| `id`, `node_id` | The session, and the node it runs on. |
| `agent`, `profile`, `command` | What it runs. |
| `project` | Its working directory. |
| `repo` | The main work tree of the repository `project` is in, the same for its subfolders and its worktrees; empty outside one. |
| `branch` | The branch checked out in `project`; empty outside a repository. |
| `state` | `stopped` (restored, starts on attach), `starting`, `running`, `exited`. |
| `exit_code` | Once exited. |
| `name`, `title` | The user's name for it, and the latest terminal title its program set. |
| `conversation_id` | The agent's own conversation, stable across restarts. |
| `busy`, `idle` | Printing on its own, as an agent at work does; nothing running in front of its program, as a shell at its prompt. |
| `status`, `status_at` | What the agent's hooks last reported: `idle`, `working`, `waiting`, `done`. Empty for an agent without hooks. |
| `approval` | The tool call the agent waits to have approved. |
| `worktree` | The worktree made for it. |
| `last_output`, `started_at`, `cols`, `rows` | |

**Approvals.** An agent's `PermissionRequest` hook holds its request in the
daemon only while some front-end is watching for approvals. A front-end
watches by sending `list_sessions` with `"approvals": true`, at least every
15 seconds. While a session waits, its `approval` is set. `answer_approval`
gives a `decision`:

| Decision | |
|---|---|
| `allow` | The tool call runs. |
| `deny` | It does not; `message` says why, to the agent. |
| `terminal` | The question goes back to the agent's own prompt in its terminal. |

When nobody is watching, the agent asks in its terminal, as it would without
mako.

**`ReadChat` and `Chat`.** The daemon reads the conversation bound to the
session from the agent's log (Claude and Codex). `chat.supported` is false for
an agent mako cannot read; `conversation` is empty until the agent has saved
one. `messages` are `user`, `assistant` and `tool` turns, with the oldest left
out past a size limit (`truncated`). `usage` is the whole conversation's tokens:

| `Usage` field | |
|---|---|
| `input`, `cached_input`, `output` | Tokens spent, cached reads apart from fresh input. |
| `context`, `window` | The latest prompt's size, and the model's context window when the agent says. |
| `limits` | The account's subscription limits, shortest window first: `{minutes, used, resets}`, where `used` is a percentage and `resets` Unix seconds. Claude's are its five-hour and weekly windows (300 and 10080 minutes). |

A `tool` turn that calls a tool carries its answer in `result`; one not
answered yet is `pending`. `agents` are the subagents the calls in
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
| `read_file` | `ReadFile` | `file` | The start of a file, or with `image` the whole of an image. |
| `write_file` | `WriteFile` | `file` | Replaces a file's contents, keeping its mode. |
| `file_op` | `FileOp` | `file` | `create`, `mkdir`, `rename` (to `to`) or `delete`. None replaces what exists. |
| `search` | `Search` | `search_result` | A project's files, or the lines holding `query`. Both leave out what `.gitignore` does. |
| `stage_file` | `StageFile` | `staged` | A piece of a file the front-end has, such as one dropped on a window, kept on the node so a session there can read it. |

Paths are absolute, on the node named, except that `read_file` also takes
one in the node's home written `~/…`, and answers with the path it read.
`read_file` sets `binary` and leaves `data` out for a file that is not text,
and `truncated` when `data` is only the start. Its `mod_time` is what a `write_file` hands back: a non-zero `mod_time`
in the write makes it fail with code `changed` if the file changed since.
Search matching is case-insensitive unless `query` has a capital.

`stage_file` sends a file in pieces of a few megabytes, each a request of its
own. The first gives the file's `name` and no `path`; the node keeps it under
a folder of its own in its temporary directory, as `mako-drops/<time>-<id>/<name>`,
and answers with the `path`. The rest give that `path` and the `offset` their
`data` goes at. A staged file is at most 512 MB, and the first piece staged a
day later removes the folders older than that.

### Git

| Request | Payload | Reply | |
|---|---|---|---|
| `git_status` | `GitStatus` | `git_changes` | The work tree's changes against HEAD, or with `base` since it left that branch. Also branch, upstream, ahead and behind, the local and remote branches, the stashes, and a merge, rebase, cherry-pick or revert under way (`operation`), whose unmerged files have status `conflicted`. |
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
| `merge`, `pull_request`, `remove_worktree` | Of a session's `worktree`: merge its branch into its base, push it and open a pull request, or remove it once nothing is left in it. |

Diff lines are `{op, text, old, new}`, where `op` is `+`, `-`, a space for
context, or `@` for a hunk header, and `old`/`new` are line numbers on each
side, 0 where a line has none.

## Attach streams

An `attach` request turns its stream into a live pipe to one session:

```proto
message Attach {
  string session_id = 1;
  string node_id = 2; // routed like any request
  bool replay = 3;    // send the current screen before live output
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
and rows), each naming the `session_id`. Anything else is ignored.

`output` and `input` carry their bytes as protobuf `bytes`, as they are,
which is why the attach stream needs no framing of its own. (The first
version of the protocol, in JSON, needed a separate binary framing for
it, since JSON carried the bytes in base64.) A 4 KiB piece of output costs
its session ID and a few bytes more on the wire.

A daemon attaching a front-end to a peer's session opens an attach stream to
the peer, then copies the bytes between the two streams in both directions
without reading them: both speak the same frames.

## Discovery and gossip

A daemon learns of its peers these ways, recorded as the node's `source`:

| Source | |
|---|---|
| `mdns` | Bonjour on the local network: service `_mako._udp`, with TXT records `id=`, `name=` and `version=`. |
| `tailscale` | The machines on the tailnet, by MagicDNS name (or the names configured), probed with a peer handshake. |
| `static` | The addresses in the config's `peers`, probed every 30 seconds until they answer. |
| `inbound` | A peer that greeted this daemon. |
| `gossip` | A node another daemon knows of. |
| `mesh` | A node's own state on the libp2p state topic. |

A daemon with libp2p adds `peer=` (its peer ID) and `p2p=` (its libp2p
port) to its Bonjour TXT record, and its `welcome` carries its `peer_id` and
`multiaddrs`, so a node found by any of the ways above is linked to over
libp2p. A static peer may also be given as a multiaddr ending in its
`/p2p/` ID, which is linked to over libp2p alone.

Probing a candidate is a full handshake: dial, `hello` as a peer, and take the
`welcome`'s node as its identity.

Every 30 seconds a daemon sends `gossip` to each peer without libp2p, with itself and its
whole peer table, and merges the peer's reply. A merge keeps the record with
the later `last_seen`, which is clamped to the receiver's clock so a peer
whose clock runs ahead cannot keep a node alive. A node not seen for five
minutes is dropped. A daemon never adds itself.

## The mesh over libp2p

Daemons link to each other over [libp2p](https://libp2p.io). Each has a
libp2p identity, an Ed25519 key kept as `peer.key` in its configuration
directory, which its peer ID names. It listens on QUIC and TCP, on port
`4243` (`config.DefaultP2PPort`) unless `p2p.port` says otherwise.

| Protocol | |
|---|---|
| `/mako/auth/1` | Each side proves to the other it knows the mesh's key, before anything else. |
| `/mako/2` | The protocol's frames, one request stream to a libp2p stream, as on QUIC. |
| `mako/state/1` | The gossipsub topic each daemon publishes its state on. |

**Proving the key.** libp2p's own handshake proves each side holds its
peer ID's key. The host that dialed then opens `/mako/auth/1` and sends a
32-byte nonce; the other answers with a nonce of its own and an
HMAC-SHA256, keyed with the pre-shared key, over its peer ID, the dialer's,
and the dialer's nonce; the dialer checks it and answers with a MAC over the
same the other way round, and the other says whether it passed. The key
itself never crosses the wire, and neither MAC proves anything to another
host or on another connection. A host that fails is hung up on. Until a host
has passed, its `/mako/2` streams are reset, and it takes no part in the
state topic or in relaying. A mesh without a key runs the same exchange
with an empty one.

**Requests.** A daemon reaches a peer with a `peer_id` on libp2p alone,
dialing its `multiaddrs` if they are not linked, and does not fall back to
QUIC, which would hand its key to whoever answers there. A peer without one,
an older build, is reached over QUIC as before.

**State.** Each daemon publishes a `NodeState` on `mako/state/1`. It checks
every second and publishes when its state has changed, or when more peers
have joined the topic since it last published, which a newcomer would
otherwise wait for. It also publishes every 5 seconds regardless:

```proto
message NodeState {
  Node node = 1;                     // the daemon itself
  repeated SessionInfo sessions = 2; // its own sessions
  bool watching_approvals = 3;       // a front-end of its answers approvals
}
```

A state is taken only as signed by the peer its node names. A daemon keeps
each peer's latest, and answers a front-end's `list_sessions` with the
sessions a peer published in the last 20 seconds, asking only a peer that
has not. A peer's `watching_approvals` counts as a front-end watching here,
so an approval waits for an answer in a window on any node. A change to
sessions' `last_output` alone does not publish at once; the next regular
state carries it.

A daemon tells its watching front-ends (`watch`) when a peer's state
changes its nodes or sessions, so a window hears of a session started on
another machine within a second or two.

**Relays.** A node behind NAT is reached through a relay: a mako daemon
with a public address and `p2p.relay` set, whose multiaddr the others list
in `p2p.relays`. A node reserves a slot on each of its relays and gives the
relayed address in its `multiaddrs`; once linked through a relay, the two
try to punch through to a direct connection. A mesh relay keeps slots and
circuits for hosts that proved the key alone, and does not limit a
circuit's length or data, as a session may run over one for hours.

```json
"p2p": {
  "port": 4243,
  "relay": false,
  "relays": ["/ip4/203.0.113.7/udp/4243/quic-v1/p2p/12D3KooW..."]
}
```

`p2p.disabled` leaves a daemon to QUIC alone, as an older build.

## Compatibility

The ALPN and the libp2p protocol IDs name the framing. Nodes whose names
differ fail the handshake and do not see each other, so every machine of a
mesh moves to a new name together. That has happened twice:

- **JSON to protobuf.** The first version framed JSON envelopes. Version 2
  frames protobuf.
- **sanbo to mako.** The project was called sanbo, and its builds speak
  `sanbo/2`, `/sanbo/auth/1` and `_sanbo._udp`. A mako build speaks the same
  protocol under mako's names, and does not see them. Its daemon moves an
  existing `sanbo` configuration directory to `mako` on its first run,
  keeping its node ID, key and identity.

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
- **Revision** (`Node.revision`, `protocol.Revision`, currently 4) counts what
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
| Replay screen piece | 256 KiB |
| Conversation log read for `read_chat` | the last 2 MiB, at most 200 messages |
| `stage_file` | 512 MiB a file |
| `search`, files listed | 50 000 |
| `search`, matches | 1 000, 100 per file, 300 bytes of each line |
| `search`, time | 20 s |
| Session name | 200 bytes |
| Git operations | 2 minutes |
| Forwarded peer requests | 5 s to dial; `list_sessions` and gossip 5 s each |
| Approval watch | 15 s after the last `list_sessions` with `approvals` |
| Key proof on libp2p | 10 s |
| Published state | taken as current for 20 s |
| Relinking a peer on libp2p | at most every 30 s |
| Peer expiry | 5 minutes unseen |
| Hook report | 1 MiB |

## The local hook socket

Separate from the mesh, a daemon listens on a Unix socket in its private run
directory, `hooks-<port>.sock`, for the agents it runs. It gives each Claude
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

## Security

Between daemons on libp2p, each side is held to its peer ID, and each proves
it knows the mesh's key without sending it (see
[The mesh over libp2p](#the-mesh-over-libp2p)). A host that does not know the
key gets nothing from the mesh: no requests, no state, no relay.

The QUIC listener is weaker. Its certificates are self-signed and not
verified, and the pre-shared key travels in the `hello`, so a host that can
pose as a daemon's address learns the key. A front-end talks to its daemon
over it, normally on the same machine, and a daemon talks over it to a peer
without libp2p alone. Keep the QUIC port on a LAN, a tailnet or loopback;
the libp2p port may face the internet, with a `psk` set.

Anyone who knows the key can start programs and read and write files on
every machine of the mesh. Set a long, random `psk`.
