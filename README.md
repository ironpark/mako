# mako

**Run coding agents on all your computers, and drive them from one window.**

mako runs terminal agents (Claude Code, Codex, Antigravity, or a plain
shell) on any machine you own: your laptop, a desktop at home, a build box
in the cloud. It links those machines into one private mesh, so a single
app, or a single CLI, sees every session on every machine. You can start
an agent on the desktop from the laptop, answer its approval from wherever
you are, and come back to it later with its screen intact.

```
 ┌──────────── laptop ────────────┐          ┌─────────── desktop ───────────┐
 │ mako-gui ──▶ makod             │◀────────▶│ makod                         │
 │              ├ claude  ~/api   │  libp2p  │ ├ codex   ~/web               │
 │              └ shell   ~/notes │  mesh    │ └ claude  ~/infra  (waiting)  │
 └────────────────────────────────┘          └───────────────────────────────┘
```

## Why mako

- **Sessions outlive the window.** A daemon, `makod`, owns the agent
  processes on each machine. Close the app, sleep the laptop, or lose the
  network: the agents keep working, and you reattach to the same screen.
- **One place for every machine.** Sessions from the whole mesh appear in
  one sidebar, grouped by project, by host, or as a flat list. A session on
  another computer opens in a tab just like a local one.
- **Know what each agent is doing.** Agents report through their own hooks,
  and each session shows a dot: green while working, amber when waiting for
  you, blue when a turn has finished. A finished or waiting session you
  were not looking at is marked unread, and the desktop is notified.
- **Approve from the window.** When Claude or Codex asks to run a tool, the
  question can wait in the app as a card with **Allow**, **Deny**, or **In
  terminal**, so you can answer it from any node of the mesh.
- **Terminal or chat, per tab.** Every session is a real terminal, with
  Ghostty's core underneath. Claude and Codex sessions also have a Chat
  view that renders the agent's own conversation log: Markdown replies,
  folded tool calls, running subagents, and token and context usage.
- **The project around the agent.** Each session comes with the files,
  git changes, history and search of its project, on whichever machine it
  runs. There is also an editor with syntax highlighting from Zed's
  extensions, a side-by-side diff that can send selected lines to an agent,
  and git worktrees so that agents on one repository don't step on each
  other.
- **A private mesh, wherever the machines are.** Daemons find each other on
  the local network and on your tailnet, or through addresses you list.
  They connect over [libp2p](https://libp2p.io) directly where they can,
  and through a relay you run where NAT is in the way. Every node proves
  it knows the mesh's key before it is let in.

## Getting started

mako runs on macOS and Linux. Build it from source (Go and a C toolchain;
see [DEVELOP.md](DEVELOP.md#building)):

```sh
make build        # bin/mako, bin/makod, bin/mako-gui
```

On each machine, start the daemon, or just open the app, which starts one
itself when none is running:

```sh
makod &
mako-gui
```

Then set the same key on every machine, in Settings ▸ Host or in the
configuration file, so their daemons let each other in. Daemons on the same
network or tailnet find each other on their own.

### From the command line

```sh
mako nodes                    # the machines on the mesh
mako agents                   # the agents and profiles this machine can start
mako run claude               # start Claude in the current folder, and attach
mako run claude:opus          # ...with its "opus" profile
mako -node <id> run codex     # ...on another machine
mako ls                       # every session on the mesh
mako attach <session-id>      # pick a session up again
mako rename <session-id> api  # name it
mako kill <session-id>        # end it
mako addrs                    # this daemon's addresses, to give other nodes
```

## The desktop app

`mako-gui` is a window onto the mesh:

- **Sidebar.** Projects and their sessions, with each session's host,
  branch and last activity. Sessions can be pinned, searched and dragged
  into panes. Dropping a session onto another project or host starts the
  same agent there.
- **Tabs and panes.** Open sessions as tabs, and drag them to split, move
  or reorder panes. Each pane's tab bar has a launcher for every agent the
  node can start.
- **Terminal.** Colours, wide characters, the IME, the mouse, links that
  open with ⌘-click, image previews, pasted screenshots, and files dropped
  in as paths, even to a session on another machine.
- **Chat.** The conversation as the agent logged it, and a composer that
  types into the live session (Enter sends; Shift-Enter starts a new line;
  ↑ recalls what you sent).
- **Files, Changes, History, Search.** Browse and edit the project's files
  on its node. Review changes as a tree with per-file diffs, then commit,
  push, pull, stash or switch branches. Look through the commit graph, and
  search the project's text with ⌘⇧F.
- **Command palette** (⌘K), **go to file** (⌘P), and English and Korean
  interfaces with light and dark themes.

## Configuration

Each daemon reads `config.json` from its configuration directory:
`~/Library/Application Support/mako` on macOS and `$XDG_CONFIG_HOME/mako`
on Linux, or the folder `MAKO_HOME` names. It is written with defaults on
the first run, and most of it can be edited in the app's Settings.

```json
{
  "name": "desktop",
  "psk": "a long random key shared by every node",
  "peers": ["build-box.example.com:4242"],
  "discovery": {"mdns": true, "tailscale": true},
  "p2p": {"relays": ["/ip4/203.0.113.7/udp/4243/quic-v1/p2p/12D3KooW..."]},
  "agents": {
    "claude": {
      "args": ["--verbose"],
      "profiles": [
        {"name": "opus", "args": ["--model", "opus"]},
        {"name": "work", "env": {"CLAUDE_CONFIG_DIR": "~/.claude-work"}}
      ]
    },
    "codex": {"profiles": [{"name": "auto", "args": ["--full-auto"]}]}
  }
}
```

**Agents.** Each agent has a `command`, default `args` and `env`, and named
`profiles` that add to them. `claude:work` runs the command with the
agent's flags, then the profile's, then any given after `--`. Values expand
`$NAME`, `${NAME}` and a leading `~`; write `$$` for a literal `$`. The
built-ins `claude`, `codex`, `agy` and `shell` work without any
configuration. Other nodes see only your agents' and profiles' names, never
their flags or environment.

**MCP servers.** Claude and Codex sessions get the MCP servers of the Zed
extensions installed on their node (Settings ▸ Extensions), plus any you
list under `context_servers`. A profile can choose a subset of them.

```json
"context_servers": {
  "mcp-server-github": {"settings": {"github_personal_access_token": "ghp_…"}},
  "postgres-context-server": {"enabled": false},
  "my-server": {"command": {"path": "~/bin/my-mcp", "args": ["--stdio"]}}
}
```

**Networking.** The CLI and the app talk to their daemon on UDP port 4242,
and daemons talk to each other on port 4243 (UDP and TCP). When a machine
is behind NAT that no hole can be punched through, run a relay on a node
with a public address: set `"p2p": {"relay": true}` there, run `mako addrs`
on it, and add the address it prints to the other nodes' `p2p.relays`.

## Security

Set a long, random `psk`: anyone who knows it can start programs and read
and write files on every machine in the mesh.

Between daemons, each side proves it knows the key without sending it,
and a node that cannot prove it is refused: it gets no requests, no state
and no relay. The connection from the CLI or the app to its daemon is
plain QUIC with a self-signed certificate, and it carries the key, so keep
port 4242 on loopback, the LAN or a tailnet. Port 4243 can face the
internet.

## Learn more

- [DEVELOP.md](DEVELOP.md): building, the code's layout, and how the pieces
  work
- [docs/protocol.md](docs/protocol.md): the protocol between front-ends and
  daemons, and across the mesh

mako is built on [gostty](https://github.com/ironpark/gostty) (the
terminal), [ggui](https://github.com/ironpark/ggui) (the UI),
[libp2p](https://libp2p.io) (the mesh), and
[wasmtime](https://wasmtime.dev) (Zed's extensions).
