# mako

**Run coding agents on all your computers, and drive them from one window.**

mako runs terminal agents (Claude Code, Codex, Antigravity, pi and its
offshoots, OpenCode, or a plain shell; see [Agents](#agents)) on any
machine you own: your laptop, a desktop at home, a build box
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
  Ghostty's core underneath. Sessions of the agents mako reads also have a
  Chat view that renders the agent's own conversation log: Markdown
  replies, folded tool calls, running subagents, and token and context
  usage.
- **The project around the agent.** Each session comes with the files,
  git changes, history and search of its project, on whichever machine it
  runs. There is also an editor with syntax highlighting from Zed's
  extensions, a side-by-side diff that can send selected lines to an agent,
  and git worktrees so that agents on one repository don't step on each
  other.
- **A private mesh, wherever the machines are.** Daemons find each other on
  the local network and on your tailnet, or through addresses you list.
  They connect over [libp2p](https://libp2p.io) directly where they can,
  and through a relay you run where NAT is in the way.
- **Trust you give, one way, by key.** Each machine has its own key, and
  lets in only the keys you allow, as SSH does with `authorized_keys`. Let
  your laptop drive a server without the server driving your laptop. A
  machine can ask another to let it in, and you allow it with a click. Or
  vouch for all your machines with one SSH key, and they trust each other
  at once.

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

Daemons on the same network or tailnet find each other on their own, but
let nothing in until you say so. Either:

- **Ask and allow.** In Settings ▸ Nodes, click **Ask to be let in** next to
  the other machine (or run `mako request <node>`). On that machine,
  Settings ▸ Access shows the request: **Allow** it (or run `mako allow
  <node>`).
- **Vouch with your SSH key.** Set the same SSH public key, such as
  `~/.ssh/id_ed25519.pub`, under Settings ▸ Access on each machine (its
  private half in the SSH agent). Machines vouched for by one key let each
  other in, and a machine that lets the key in lets them all in.

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
mako access                   # this machine's key, requests, and the keys let in
mako request <node> [note]    # ask another machine to let this one in
mako allow <node>             # let in a machine that asked
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

## Agents

mako knows each agent through a driver: how its command line picks a
conversation, how it is told mako's hooks and MCP servers, and how its
logs read. Any other program runs as a plain terminal session.

### Support status

| Agent | Status | How far it has been tried |
|---|---|---|
| Claude Code | Stable | Used daily: chat, subagents, hooks, approvals, status line, compaction |
| Codex | Stable | Used daily; its subagents' display is read from Codex's source, not yet from a live run |
| Antigravity (`agy`) | Beta | Hooks checked on a live run; chat read back from real transcripts |
| pi, omp, omo | Experimental | Written from their published formats; the extension runs under Node in tests, not yet in the agents |
| OpenCode, OmO on it | Experimental | Written from OpenCode's source; the plugin runs under Node in tests, not yet in OpenCode |
| Shell | Stable | A terminal, nothing more to follow |

| Platform | Status |
|---|---|
| macOS | Stable: where mako is used day to day |
| Linux | Supported: built and tested, less used |
| Windows | Experimental: sessions run on ConPTY, built and its tests compiled, not yet run on Windows |

### What each agent gets

| Agent | Command | Chat view | Status | Approvals in the app | MCP servers | Resumes with | Conversation ID |
|---|---|---|---|---|---|---|---|
| Claude Code | `claude` | ✓, with subagents | hooks, through `--settings`; status line for context and limits | ✓ | `--mcp-config` | `--resume` | set by mako (`--session-id`) |
| Codex | `codex` | ✓, with subagents | hooks, through `-c` (versions with the hooks feature) | ✓ | `-c mcp_servers…` | `codex resume` | learned from its hook or log |
| Antigravity | `agy` | ✓ | hooks, in agy's global `hooks.json` | — | — | `--conversation` | learned from its hook |
| pi | `pi` | ✓ | an extension mako writes (`--extension`) | — | the same extension | `--session` | set by mako (`--session-id`) |
| oh-my-pi | `omp` | ✓ | an extension mako writes (`--extension`) | — | — | `--resume` | learned from its extension |
| OmO (own edition) | `omo` | ✓, with OmO's agent tools | an extension mako writes (`--extension`) | — | the same extension | `--session` | set by mako (`--session-id`) |
| OpenCode, OmO on it | `opencode` | ✓, as mako's plugin writes it down | a plugin mako writes, in `OPENCODE_CONFIG_CONTENT` | — | the same variable | `--session` | learned from its plugin |
| Shell, anything else | `$SHELL`, any | — | — | — | — | — | — |

Status is the session's dot: working, waiting for you, or done. Every
agent above reports when it works and when its turn ends; only Claude
also says when it waits on you.

### Limitations

- **Claude Code.** None beyond Claude's own; its approvals and status line
  need a launch that brings no `--settings` of its own.
- **Codex.** Its log keeps a tool only once it has finished, so a running
  tool does not show in Chat. It does not say when it waits on you.
  Versions without the hooks feature are followed through their log alone.
- **Antigravity (`agy`).** agy takes hooks from no command line, so mako
  keeps an entry named `mako-…` in `~/.gemini/config/hooks.json`. It runs
  in every agy, does nothing outside mako's sessions, and stays if mako is
  removed. agy's approvals are its own, in its terminal, and its subagents
  do not show. Conversations older than agy's prompt history have no
  working directory, so only their hook binds them.
- **pi, omp, omo.** A session's log keeps every branch taken back from an
  earlier message; Chat shows them in the order they were written, the
  abandoned with the current. omp takes no MCP servers from a launch, and
  on Linux and macOS a `$XDG_DATA_HOME/omp` that exists moves its
  sessions where mako does not look.
- **OpenCode and OmO.** OpenCode keeps conversations in a database mako
  does not read; mako's plugin writes each one down as it goes, in
  `$XDG_DATA_HOME/opencode/mako` (`~/.local/share/opencode/mako`). Chat
  shows a session only once the plugin has seen it, with what came before
  copied in then, and a part only once it is finished. Subagents' sessions
  are not shown, OpenCode says nothing when a session ends, and
  `opencode --pure`, which loads no plugins, leaves mako's out. OmO is a
  plugin of OpenCode's, so it runs as `opencode`.
- **All of them.** mako's drivers read what each agent writes as of the
  versions they were written for; an agent that changes its log or hooks
  may show less until its driver catches up. The drivers for pi, omp,
  omo and OpenCode are new and have been tried against their published
  formats rather than long use.

## Configuration

Each daemon reads `config.json` from its configuration directory:
`~/Library/Application Support/mako` on macOS and `$XDG_CONFIG_HOME/mako`
on Linux, or the folder `MAKO_HOME` names. It is written with defaults on
the first run, and most of it can be edited in the app's Settings.

```json
{
  "name": "desktop",
  "ssh_key": "~/.ssh/id_ed25519.pub",
  "peers": ["build-box.example.com"],
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
built-ins `claude`, `codex`, `agy`, `pi`, `omp`, `omo`, `opencode` and
`shell` work without any configuration. Other nodes see only your agents' and profiles' names, never
their flags or environment.

**MCP servers.** Sessions of the agents that take them (see
[Agents](#agents)) get the MCP servers of the Zed extensions installed on
their node (Settings ▸ Extensions), plus any you
list under `context_servers`. A profile can choose a subset of them.

```json
"context_servers": {
  "mcp-server-github": {"settings": {"github_personal_access_token": "ghp_…"}},
  "postgres-context-server": {"enabled": false},
  "my-server": {"command": {"path": "~/bin/my-mcp", "args": ["--stdio"]}}
}
```

**Access.** The keys a machine lets in are in `authorized_keys` beside
`config.json`, in OpenSSH's format: other machines' own keys, which
`mako access` prints, and SSH keys. Edit it by hand or with `mako access`,
`allow`, `deny`, `trust` and `revoke`; a change holds at once.

**Networking.** The CLI and the app talk to their daemon on a Unix socket
in the configuration folder, and daemons talk to each other on port 4243
(UDP and TCP), which `p2p.listen` can change. When a machine
is behind NAT that no hole can be punched through, run a relay on a node
with a public address: set `"p2p": {"relay": true}` there, run `mako addrs`
on it, and add the address it prints to the other nodes' `p2p.relays`.

## Security

Letting a key in hands its holder the machine: a machine you let in can
start programs and read and write files on yours. Trust runs one way, so
letting a server's key in on your laptop is a separate decision from
letting the laptop's in on the server.

Between daemons, each side is held to its own key, and one whose key is not
let in gets nothing: no requests, no sessions, no relay. The CLI and the app
talk to their own daemon on a socket only your user can open, so nothing
but port 4243 faces the network, and it can face the internet.

## Learn more

- [DEVELOP.md](DEVELOP.md): building, the code's layout, and how the pieces
  work
- [docs/protocol.md](docs/protocol.md): the protocol between front-ends and
  daemons, and across the mesh

mako is built on [gostty](https://github.com/ironpark/gostty) (the
terminal), [ggui](https://github.com/ironpark/ggui) (the UI),
[libp2p](https://libp2p.io) (the mesh), and
[wasmtime](https://wasmtime.dev) (Zed's extensions).
