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
| Stream | a connection to a request | `/mako/2`, a libp2p stream to a request |
| Access | the socket's file mode: this user alone | keys, one way: `/mako/trust/1` |
| Frames | length-prefixed protobuf `Envelope`s | the same |
| Pushed state | `watch` → `changed` | `NodeState` on `/mako/state/2` |

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
connection to the daemon's socket, or a libp2p stream of `/mako/2` between
daemons.
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
hello   { name: "mako-cli", version: "v0.3.0" }
welcome { node: { ...the daemon's Node... } }
```

| `Hello` field | |
|---|---|
| `name` | The caller's name: a daemon's node name, or `mako-cli` from the CLI and the app alike. |
| `version` | The caller's build. |

A stream is a peer's or a front-end's by where it comes from, not by what
its hello says: one on libp2p is a peer's, which gets only the daemon's own
sessions from `list_sessions` and may not send `access`, and one on the
socket a front-end's.

A first frame that is not a `hello` gets an `error`, and the stream is then
done.

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
and only to those that let it in; another fails with "has not let this node
in". Each node's state tells the nodes it lets in of the nodes it knows, so
the table fills with the whole mesh.

`list_sessions` is not routed by node. A daemon asked by a front-end answers
with its own sessions and those every peer that lets it in
[sent](#the-mesh-over-libp2p) in the last 20 seconds; it asks no one. A
daemon asked by a peer answers with its own sessions only. Sessions a peer
sends for any node other than itself are dropped.

`list_nodes`, `watch` and `access` are answered by the daemon itself.

## Message reference

Requests are grouped by area. The reply column is the `type` of the frame
that answers on success.

### Mesh

| Request | Payload | Reply | |
|---|---|---|---|
| `list_nodes` | `{}` | `nodes` | The daemon itself, then its peers, each with how the daemon stands with it. |
| `watch` | `{}` | `changed`, then more | Turns the stream into a feed: a `changed` at once, then one whenever the mesh's nodes or sessions, or the daemon's access, change, until the front-end closes the stream. |
| `access` | `Access` | `access_info` | Who the daemon lets in, and changes to it; see [Access](#access). From a front-end only. |

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
}

message Changed {
  bool nodes = 1;    // the nodes changed: ask list_nodes again
  bool sessions = 2; // the sessions changed: ask list_sessions again
  bool access = 3;   // a request came, or the keys changed: ask access again
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
| `/mako/2` | The protocol's frames, one request to a libp2p stream, as on the socket. Open to the peers a daemon lets in. |
| `/mako/state/2` | A daemon's `NodeState`, one message to a stream. |
| `/mako/access/1` | A daemon asking a peer to let it in. |

The small protocols beside `/mako/2` carry length-prefixed messages (a
32-bit big-endian length, at most 1 MiB): JSON on `/mako/trust/1` and
`/mako/access/1`, a protobuf `NodeState` on `/mako/state/2`.

**Introductions.** libp2p's own handshake proves each side holds its peer
ID's key. The host that dialed then opens `/mako/trust/1`, and each side
writes, then reads:

```
{"node": "<node ID>", "name": "<node name>", "cert": {"owner": …, "sig": …, "expires": …}}
{"trusts": true}
```

`cert` is there when an SSH key vouches for the host (see
[Access](#access)). Each side decides from the other's introduction
whether it lets the other in, and the second message says so. The two
introduce themselves again whenever either's keys change, or its
certificate is renewed, so a key let in or taken out holds at once. A host
that cannot introduce itself is hung up on.

**Requests.** A daemon reaches a peer on libp2p, dialing its `multiaddrs` if
they are not linked, and only a peer that lets it in. Until a peer is let
in, its `/mako/2` streams are reset.

**State.** Each daemon sends its `NodeState` to its peers, checking every
second and sending when its state has changed, and every 5 seconds
regardless, and to a peer at once when the two introduce themselves:

```proto
message NodeState {
  Node node = 1;                     // the daemon itself
  repeated SessionInfo sessions = 2; // its own sessions
  bool watching_approvals = 3;       // a front-end of its answers approvals
  repeated Node peers = 4;           // the other nodes it knows of
}
```

What a peer is sent depends on how the two stand. A peer the daemon lets in
gets it all; a peer that lets the daemon in gets the node, without its
agents, and `watching_approvals`; a peer with neither gets nothing. A state
is taken only as its own node's: its record must name the peer that sent
it. A daemon keeps each peer's latest sessions, and answers a front-end's
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
- **Revision** (`Node.revision`, `protocol.Revision`, currently 5) counts what
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
| Forwarded peer requests | 5 s to dial |
| Approval watch | 15 s after the last `list_sessions` with `approvals` |
| Introductions on libp2p | 10 s |
| A message on `/mako/trust/1`, `/mako/state/2`, `/mako/access/1` | 1 MiB |
| Access requests waiting | 32, each note 500 bytes |
| An SSH key's certificate | 30 days, renewed a week before |
| A peer's sessions | taken as current for 20 s after its state |
| Relinking a peer on libp2p | at most every 30 s |
| Peer expiry | 5 minutes unseen |
| Hook report | 1 MiB |

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
the peer's own key, or the SSH key that vouches for it. A daemon serves the
requests of the peers it lets in, so a peer it lets in can start programs and
read and write files on its machine; that peer must let it in for the
reverse.

**Vouching with an SSH key.** With `ssh_key` set to an SSH public key, a
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
honour. The file is read again when it changes.

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
programs and read and write files there. Let in the keys of your own
machines, and an SSH key only when its private half is kept safe.
