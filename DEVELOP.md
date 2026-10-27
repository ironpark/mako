# Developing mako

This is the guide for working on mako itself: how the code is laid out, how
the pieces work, and how to build, test and debug them. What mako is and how
to use it is in the [README](README.md); the wire protocol between its parts
is in [docs/protocol.md](docs/protocol.md).

## Contents

- [Building](#building)
- [Layout](#layout)
- [Architecture](#architecture)
- [How the parts work](#how-the-parts-work)
- [The desktop app](#the-desktop-app)
- [Changing the protocol](#changing-the-protocol)
- [Testing](#testing)
- [Debugging](#debugging)

## Building

mako is Go with cgo. You need:

- Go (the version in `go.mod`) and a C toolchain
- [buf](https://buf.build) and `protoc-gen-go`, only to change the protocol's
  schema

wasmtime comes as the static library
[wasmtime-go](https://github.com/bytecodealliance/wasmtime-go) carries for
macOS, Linux and Windows on x86-64 and arm64. `internal/zedhost` calls its C
API directly, with the headers of the same release in
`internal/zedhost/include`; `include/sync.sh` copies them again after
wasmtime-go moves.

| Target | |
|---|---|
| `make build` | `bin/mako`, `bin/makod` and `bin/mako-gui`, stamped with `git describe` as their version |
| `make test` | `go test ./...` |
| `make vet` | `go vet ./...` |
| `make fmt` | `gofmt` over the tree |
| `make proto` | Lints the schema in `proto/` and generates `internal/protocol` |
| `make clean` | Removes `bin/` |

The UI comes from [ggui](https://github.com/ironpark/ggui) and
[ggfx](https://github.com/ironpark/ggfx), and the terminal from
[gostty](https://github.com/ironpark/gostty). To work on one of them alongside
mako, point `go.mod` at a local checkout with a `replace` directive, and
take it out before you commit.

## Layout

    cmd/makod            the daemon
    cmd/mako             the CLI front-end
    cmd/mako-gui         the desktop front-end
    proto/mako/v2        the protocol's protobuf schema

    internal/protocol    the generated messages, their framing and constants
    internal/transport   frames on a stream; the daemon's Unix socket
    internal/p2p         the libp2p host: identity, keys and one-way trust, relays
    internal/discovery   Bonjour (mDNS) and Tailscale peer search
    internal/daemon      sessions, the peer table, mesh state, request routing, hooks
    internal/client      a front-end's connection to its daemon, and attach
    internal/session     a PTY process and its gostty terminal
    internal/activity    OSC 9;4 progress reports
    internal/agentspec   agent names and profiles to commands and environments
    internal/transcript  Claude's and Codex's native conversation logs
    internal/gitstat     git status, diffs, history and operations
    internal/gui         the window: sidebar, panes and tabs, terminal, chat, editor
    internal/syntax      tree-sitter compiled to wasm, under wasmtime
    internal/zed         Zed extensions: the store, manifests and languages
    internal/zedhost     Zed extensions' own wasm components, under wasmtime's C API
    internal/mcp         MCP servers for the agents, from extensions or the config
    internal/config      the node's identity and configuration

## Architecture

```
 mako, mako-gui ──socket──▶ makod (this machine) ──libp2p──▶ makod (another)
                                  │                                │
                             PTY sessions                     PTY sessions
```

**The daemon owns the sessions.** `makod` runs on every machine and owns
the agent processes, so sessions outlive any window. The front-ends are
clients: closing one leaves its agents running.

**One connection reaches the whole mesh.** A front-end talks to its own
daemon alone. A request that names another node is forwarded by that
daemon, and an attach to a remote session is spliced stream to stream, so a
front-end never dials a remote machine.

**Daemons link over libp2p, and trust one way.** Each daemon is a libp2p
host with its own Ed25519 identity (`internal/p2p`). Two hosts that link
introduce themselves and each decides, from its `authorized_keys`, whether
it lets the other in: by the other's own key, or by the SSH key that signed
a certificate for it. A daemon serves only the peers it lets in, and sends
its sessions only to them. A host behind NAT is reached through a relay run
by a mako node with a public address. Each daemon pushes its state to its
peers directly, not over gossip, which could not keep it from a peer it
does not let in.

**Front-ends reach their daemon on its socket.** The daemon listens on a
Unix socket in its configuration folder that only its user can open, one
connection to a request. The socket stands for a token: there is none.

**Discovery feeds one peer table.** Bonjour finds daemons on the same
link, Tailscale's peer list finds those on the tailnet, and `peers` in the
config names the rest. An address without a peer ID is probed on its
libp2p port, whose TLS handshake shows the ID. A node found any of these
ways is linked over libp2p, and a daemon tells the peers it lets in of the
nodes it knows.

**Terminal state lives on both ends.** Every session feeds its PTY output
into a [gostty](https://github.com/ironpark/gostty) terminal as well as to
the clients attached, so a late attach is handed the current screen first.
The window runs a gostty terminal of its own per tab, fed from that stream,
and draws and encodes keys locally.

## How the parts work

### Agent state and hooks

A session's dot in the sidebar comes from the agent's own hooks. Claude
gets them through `--settings`, alongside the user's own. Codex gets them as
`-c hooks.…` overrides with their trust (`-c hooks.state=…`, the hash Codex
itself records on trusting a hook), so they run without the user trusting
them in `/hooks`. This works on Codex versions whose `codex features list`
has hooks. Each hook runs the daemon's own program, which reports to the
daemon on a private Unix socket; see
[the local hook socket](docs/protocol.md#the-local-hook-socket).

An agent said to be working that prints nothing for a minute is taken to
have stopped, since an interrupted turn reports no end. Programs that send
OSC 9;4 progress reports (`internal/activity`) mark their session busy.
Reports expire after 15 seconds without a refresh, as Ghostty's do.

### Approvals

An agent's `PermissionRequest` hook holds its request in the daemon while a
front-end that answers approvals is watching, here or on a peer this daemon
lets in (a peer's state says whether it has one). The window shows the
request as a card in Chat and a bar over the terminal. With no watcher, or once the
windows stop, the hook gives the question back to the agent's own prompt.

### Chat and conversations

Chat reads Claude's and Codex's native JSONL logs on the session's node
(`internal/transcript`). The daemon binds each session to its native
conversation and persists that with the launch:

- Claude is given a fixed session ID.
- Its `SessionStart` hook, and Codex's, report each new, resumed, cleared or
  forked conversation. Reports carry when the hook ran, so a late one never
  replaces a newer one.
- Until the first report, the agent's own writable log identifies the
  conversation: through `lsof` on macOS and `/proc` on Linux, launcher
  children included. Modern Codex is launched with `--no-daemon`, so the
  log's writer is the session and not a shared app server.

`CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `HOME` overrides in the session's
environment are respected. The reader takes the last 2 MiB of a log (up to
200 messages, long content capped), and the subagents a conversation started
from the logs they keep of their own.

### Saved sessions

Each daemon saves its sessions' names, agents, profiles, launch arguments
and folders in `sessions.json` beside `config.json`. On restart it restores
the list without starting anything. A session starts again when its tab
becomes visible, or when a CLI client attaches. Claude and Codex resume
their saved conversation (`--resume ID`, `resume ID`), with the current
profile and MCP settings; the first prompt is not sent again. A saved
conversation whose log is missing fails visibly rather than starting another.
Older saved sessions without a native ID are migrated when exactly one
unclaimed log was made during that launch; an ambiguous match is reported,
not guessed.

### Languages and extensions

The editor's languages come from [Zed](https://zed.dev)'s extensions,
installed under `extensions/` beside `config.json`. mako reads an extension
as Zed does: its languages' `config.toml` for file names, comments and tab
size, and the tree-sitter grammar it carries, compiled to wasm. That grammar
is run by the tree-sitter runtime, itself compiled to wasm, under
[wasmtime](https://wasmtime.dev) (`internal/syntax`), and coloured by the
language's `highlights.scm`. The languages Zed builds in, Go, Rust, Python,
JavaScript, TypeScript and the like, come with mako, built from their
grammars' own repositories (`internal/syntax/builtin`). Extensions'
language servers are not run yet.

### MCP servers

An extension's MCP server (a Zed context server) is started by the
extension's own wasm, the component Zed would run. `internal/zedhost` runs
it and implements the parts of Zed's extension API a context server uses:
settings, downloading and unpacking releases, Node.js and npm, GitHub
releases, the platform, processes the manifest allows, and HTTP. The
extension installs its server under `extension-work/`. The daemon asks
again after six hours, or when the extension or its settings change, and
asks once at start so a session need not wait for an install. A server
missing what it needs is left out, with the reason in the daemon's log.

Claude gets the servers in an `--mcp-config` file under `run/`, removed when
the session ends; Codex gets them as `-c mcp_servers.…` overrides.

## The desktop app

`mako-gui` is built with ggui (`internal/gui`) and laid out after
[Paseo](https://github.com/getpaseo/paseo). It talks to its daemon through
`internal/client` like the CLI does, and watches it (`watch`) for changes to
the nodes and sessions, with a slow poll as a check.

The app can run the daemon itself (`internal/gui/embedded.go`), as Settings ▸
Daemon chooses: **Auto** uses a `makod` already running and otherwise starts
one in the app, **In this app** always does, and **External** only talks to
a `makod` started some other way. A daemon in the app listens like any
other, but its sessions end when the app quits.

Its settings are kept in `gui.json` beside `config.json`. Sessions run with
`TERM_PROGRAM=ghostty`, since the terminal under them is Ghostty's core.

## Changing the protocol

The schema in `proto/mako/v2` is the only definition of the messages; the
generated Go in `internal/protocol` is committed.

1. Edit the `.proto` files. Add fields and message types only; never reuse
   or renumber a field.
2. Run `make proto`, then `buf breaking --against '.git#branch=main'`.
3. For a new request, add its constant to `protocol.Type` and its case to the
   daemon's dispatch, and describe it in `docs/protocol.md`.
4. When a daemon starts reporting something a front-end shows, bump
   `protocol.Revision`, so a window can tell an older daemon is out of date.

See [Compatibility](docs/protocol.md#compatibility) for the rules.

## Testing

`make test` runs everything. Most tests start real daemons on loopback,
with their own configuration directories, and talk to them as a front-end
would. Two need Zed extensions downloaded first, and are skipped otherwise:

| Variable | |
|---|---|
| `MAKO_ZED_EXTENSIONS` | A folder of installed Zed extensions with context servers, for `internal/mcp` and `internal/zedhost` |

Tests set `MAKO_HOME` to a temporary folder, so they never touch your own
configuration.

## Debugging

| | |
|---|---|
| `makod -v` | Debug logging |
| `mako-gui -render-dir <dir>` | Renders the window against the running daemon to PNGs, without showing it, then exits: for reviewing layout changes |
| `mako-gui -pprof 127.0.0.1:6060`, or `MAKO_PPROF` | Serves Go's profiles |
| `mako-gui -daemon <socket>` | Talks to another daemon, as one forwarded with `ssh -L` |
| `MAKO_HOME` | Uses another configuration directory, for a second daemon on one machine |
