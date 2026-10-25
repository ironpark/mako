# sanbo

Run terminal agents (`claude`, `codex`, `agy`, a plain shell) on any of your
computers and drive them from one place.

## Layout

    cmd/sanbod        daemon: owns PTYs, joins the mesh
    cmd/sanbo         front-end CLI: nodes, sessions, attach
    cmd/sanbo-gui     desktop front-end (ggui), laid out after Paseo
    internal/protocol wire format (length-prefixed JSON over QUIC)
    internal/transport QUIC listener/dialer, self-signed node certificate
    internal/discovery Bonjour (mDNS) + Tailscale MagicDNS ping search
    internal/session   PTY process plus gostty terminal state
    internal/daemon    session registry, peer table, gossip, relaying
    internal/client    front-end connection and attach loop
    internal/gui       the desktop window: sidebar, tabs, terminal view, editor
    internal/syntax    tree-sitter in wasm under wazero: grammars and highlight queries
    internal/zed       Zed extensions: the store, their manifests and languages
    internal/config    node identity and configuration

## Design

**Daemon / front-end split.** `sanbod` runs on every machine and owns the agent
processes, so sessions outlive any UI. `sanbo` is a thin client; closing it
leaves the agent running.

**One connection, whole mesh.** A front-end talks to a single daemon. If a
request names another node, that daemon relays it — `forwardAttach` splices the
two streams together, so the front-end never has to reach a remote machine
directly.

**Discovery.** Two backends run side by side and feed one peer table:

- *Bonjour/mDNS* — each daemon advertises `_sanbo._udp` with its node ID; peers
  on the same link are found immediately.
- *Tailscale MagicDNS* — peer names come from `tailscale status --json` (or a
  configured list) and each is probed on the sanbo port. A name becomes a node
  only once a handshake succeeds.

Nodes learned on either path are gossiped between daemons, so a machine visible
to one node becomes visible to all of them.

**Terminal state.** Every session feeds its PTY output into a
[gostty](https://github.com/ironpark/gostty) terminal as well as to attached
clients, so a late attach can be handed the current screen (`Attach.Replay`)
instead of a blank window.

## Usage

    sanbod &                       # start the daemon
    sanbo nodes                    # daemons on the mesh
    sanbo agents                   # agents and profiles this node can launch
    sanbo run claude               # start claude in the current directory
    sanbo run claude:opus          # ...with its "opus" profile
    sanbo -node <id> run codex     # ...on another machine
    sanbo ls                       # sessions across the mesh
    sanbo attach <session-id>
    sanbo kill <session-id>
    sanbo-gui                      # the same, in a window

## Desktop app

`sanbo-gui` is a window onto the mesh, built with
[ggui](https://github.com/ironpark/ggui) and laid out after
[Paseo](https://github.com/getpaseo/paseo): projects and their sessions in a
sidebar, open sessions as tabs, a composer that types a line into the
selected session, and a panel on the right with the project's files, the
session's details and the nodes. The sidebar groups its sessions by project
(a session started in a folder inside another's project, as a worktree or a
package is, goes under that project), by host, or lists them flat, as the
buttons in its header choose.

Closing a tab ends its session, asking first while it runs (Settings ▸
General can turn the question off); "Detach tab" in the header's menu closes
the tab and leaves the session running in the sidebar. Tabs arrange into
panes by dragging: onto a pane's left, right, top or
bottom edge to split it there, onto its middle or its tab bar to move the
tab into it, along a tab bar to reorder. A session in the sidebar drags in
the same way, dividers between panes resize them, and a pane closes with its
last tab. The Files tab browses the project of the session shown, on
whichever node it runs, through that node's daemon: folders open as they are
read, dotfiles show or hide with the eye button, what .gitignore leaves out
is dimmed, and a text file chosen opens in a tab of its own, beside the
terminals and arranged into panes as they are, while an image is previewed
under the tree and enlarged with a click. Files and folders dragged from the Files tab or the desktop onto
a terminal are typed as shell-quoted paths, onto the composer inserted, and
a folder dropped anywhere else opens a new session in it. The Changes tab
shows the project's git changes against HEAD, read by the daemon of the node
the session runs on (git status when git is installed, go-git otherwise): a tree of
the changed files, each folder adding up its files' lines added and removed,
refreshed every few seconds while shown, and the diff of a file chosen
under it. A tab dragged over a tab bar opens the gap it would drop into, and
the side panels slide open and closed.

A file's tab is an editor over the whole file (up to 4 MB; a larger one
shows its start, read-only), read and saved (⌘S) through the daemon of its
node: lines numbered and coloured by their language, Enter keeping the
line's indent, Tab and Shift-Tab indenting the lines selected, ⌘/ toggling
their comment, the platform IME for typing Korean and the like, and undo.
A save of a file changed on disk since it was read is refused, and the
banner offers to overwrite it or read it again; closing a file, or quitting,
with unsaved changes asks first. A line under the tab gives the file's size
and lines, its language, line endings and the caret's line and column,
leaving out what a narrow pane has no room for. A Markdown file switches
there between its source and its page (⌘⇧V), rendered from its syntax tree
as GitHub shows one: headings, emphasis, code and links, lists and task
lists, quotes and alerts, tables, code blocks highlighted in their
language, and the images beside the file, read through its node; a link to
another file opens it in a tab.

The command palette (⌘K or ⌘⇧P) searches the window's actions, the open
tabs and the sessions; its Extensions… opens Settings ▸ Extensions in a
dialog over the window.

The languages come from [Zed](https://zed.dev)'s extensions, which
Settings ▸ Extensions finds in Zed's store and installs (under
`extensions/` beside `config.json`): sanbo reads an extension as Zed does,
its languages' `config.toml` for their file names, comment and tab size,
and parses with the tree-sitter grammar it carries, compiled to wasm, run
by the tree-sitter runtime itself compiled to wasm under
[wazero](https://wazero.io) (`internal/syntax`), and colours by the
language's `highlights.scm`. The languages Zed builds in rather than
taking from extensions -- Go, Rust, Python, JavaScript, TypeScript and the
like -- come with sanbo, built from their grammars' own repositories
(`internal/syntax/builtin`). An extension's language servers, which its
own wasm starts, are not run yet.

Each pane's tab bar ends in a launcher for every agent the session's node
can start, drawn with the agent's icon: a click starts it in the project of
the tab shown, or with no tab, in the default working directory here and in
the home folder on another node; an agent with profiles opens a menu of them
first. The new-session dialog takes a working directory too, but it can be
left empty for the node's home. A session's icon, on its tab and in the sidebar, hops
while its program reports progress (OSC 9;4), as claude does while it works.
Sessions run with `TERM_PROGRAM=ghostty`, the terminal under them being
Ghostty's core, so programs find what it supports, and a program that
selects text itself, as claude does with the mouse, copies it to the
clipboard (OSC 52).

Each tab runs its own terminal on the GUI side, fed from the daemon's stream,
so the screen is drawn and keys are encoded locally under the modes the
program set: colours, bold and italic, wide characters, the IME's
composition at the cursor, drag/double/triple-click selection, and the mouse
for programs that ask for it (Shift takes it back for selecting). The
terminal keeps Tab and the Ctrl and Alt chords for the program; ⌘ chords stay
with the app (⌘T new session, ⌘W close the tab and its session, ⌘S save
the file shown, ⌘K command palette, ⌘B sidebar, ⌘, settings, and ⌘C/⌘V/⌘A for the terminal's
clipboard).

Settings (⌘,) cover the app -- language (English or Korean, or the
system's), theme (system, light, dark), terminal font
size, line height, cursor, scrollback, copy-on-select, the composer and the
daemon address -- kept in `gui.json` beside `config.json`, and this node's
daemon configuration: name, listen address, key, discovery and agents,
written to `config.json`. Settings ▸ Agents gives each agent its command,
default flags and environment, and its profiles; the new-session picker
lists every agent and profile the chosen node has. The daemon follows the
agents as `config.json` changes, so they apply to the next session; the rest
is read when it starts.

The app can run the daemon itself, so a machine needs nothing but the app.
Settings ▸ Daemon chooses: **Auto** (the default) uses a sanbod that is
already running and otherwise starts one inside the app; **In this app**
always does; **External** only talks to a sanbod started some other way. A
daemon in the app listens on the configured address like any other, so the
CLI and the rest of the mesh reach it, but it owns its sessions' processes:
they end when it stops or the app quits, and the app asks before stopping it
with sessions running. Run `sanbod` separately for sessions that outlive the
window.

`sanbo-gui -render-dir <dir>` renders the window against the running daemon
to PNGs without showing it, for reviewing layout changes.

## Configuration

`$XDG_CONFIG_HOME/sanbo/config.json` (override the directory with `SANBO_HOME`)
is written with defaults on first run: node ID and name, listen address,
discovery toggles, static peers, an optional `psk` shared by all your daemons,
and `agents`: how this node launches each agent by name.

```json
"agents": {
  "claude": {
    "command": ["claude"],
    "args": ["--verbose"],
    "env": {"DISABLE_TELEMETRY": "1"},
    "profiles": [
      {"name": "opus", "args": ["--model", "opus"]},
      {"name": "work", "env": {"CLAUDE_CONFIG_DIR": "~/.claude-work"}}
    ]
  },
  "codex": {"profiles": [{"name": "auto", "args": ["--full-auto"]}]}
}
```

A session of `claude:work` runs the command with the agent's flags, then the
profile's, then any given after `--`, and the agent's environment with the
profile's over it. No shell starts it, so configured flags and values expand
`$NAME`, `${NAME}` (from the layer before: the daemon's environment for the
agent's, the agent's for a profile's) and a leading `~`; write `$$` for a `$`.
A built-in (`claude`, `codex`, `agy`, `shell`) without a `command` runs its
default one, and an agent written as a bare command array, as older
configurations have it, still reads. Nodes tell each other their agents' and
profiles' names, never their flags or environment.

## Security

Transport is QUIC with TLS 1.3 and a self-signed per-node certificate.
Certificates are not verified against a CA; peer identity comes from the sanbo
handshake, optionally gated by the shared `psk`. Do not expose the daemon port
to an untrusted network — keep it on a LAN or a tailnet.
