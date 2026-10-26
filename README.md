# sanbo

Run terminal agents (`claude`, `codex`, `agy`, a plain shell) on any of your
computers and drive them from one place.

## Layout

    cmd/sanbod        daemon: owns PTYs, joins the mesh
    cmd/sanbo         front-end CLI: nodes, sessions, attach
    cmd/sanbo-gui     desktop front-end (ggui), laid out after Paseo
    internal/protocol messages, and their wire format (length-prefixed protobuf over QUIC)
    proto/sanbo/v2    the protocol's protobuf schema (buf: make proto)
    internal/transport QUIC listener/dialer, self-signed node certificate
    internal/discovery Bonjour (mDNS) + Tailscale MagicDNS ping search
    internal/session   PTY process plus gostty terminal state
    internal/daemon    session registry, peer table, gossip, relaying
    internal/client    front-end connection and attach loop
    internal/gui       the desktop window: sidebar, tabs, terminal view, editor
    internal/syntax    tree-sitter in wasm under wasmtime: grammars and highlight queries
    internal/zed       Zed extensions: the store, their manifests and languages
    internal/zedhost   Zed extensions' own wasm components, run under wasmtime's C API
    internal/mcp       MCP servers for the agents: resolved from extensions, passed on the command line
    internal/config    node identity and configuration

The protocol between front-ends and daemons is described in
[docs/protocol.md](docs/protocol.md).

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
    sanbo rename <session-id> api  # name it, as its tab shows it
    sanbo-gui                      # the same, in a window

## Desktop app

`sanbo-gui` is a window onto the mesh, built with
[ggui](https://github.com/ironpark/ggui) and laid out after
[Paseo](https://github.com/getpaseo/paseo): projects and their sessions in a
sidebar, open sessions as tabs, a Chat view whose composer types a line into
the session, and a panel on the right with the project's files, the session's
details and the nodes. The sidebar groups its sessions by project (a session
started in a folder inside another's project, as a worktree or a package is,
goes under that project), by host, or lists them flat, as the buttons in its
header choose. Each session there shows its host and project, the branch
checked out when the project is in a git repository, how long ago its program
last printed, and on hover a button that closes it as its tab's does. Two
project folders of the same name are told apart by the end of their paths, or
by their hosts when the paths are the same. Sessions pinned from their menu
come first, and the header's search button filters the list by title,
project, branch, agent or host. A session dropped on a section's heading
starts again there: its agent in that folder, or on that host in its own
folder. A project folder outlives its last session: killing the
sessions leaves the folder, with its button to start another, and the folder's
menu removes it from the sidebar.

Each session's dot says what its agent is doing, as the agent's own hooks
report it: green while it works, amber while it waits for the user (an
approval, or a question), blue once it finished a turn the user has not seen,
and grey at rest. A session that finishes or asks while it is not in view is
marked unread until it is looked at; the Dock's icon counts them, the window
asks for attention when an approval comes in behind it, and, unless Settings ▸
General turns it off, the desktop gets a notification (through `osascript`
on macOS, `notify-send` on Linux). Claude takes the hooks through
`--settings`, alongside the user's own. Codex takes them as `-c hooks.…`
overrides with their trust (`-c hooks.state=…`, the hash Codex itself records
on trusting a hook), so they run without the user trusting them in `/hooks`,
on Codex versions whose `codex features list` has hooks. An agent said to be
working that prints nothing for a minute is taken to have stopped, as an
interrupted turn reports no end. Terminal titles lose the spinner glyphs
agents lead them with; the dot says the agent is at work.

While the window is open (Settings ▸ General ▸ Approve in the window), an
approval Claude or Codex asks for waits for an answer in the window: a card at
the foot of Chat, and a bar over the terminal, with **Allow**, **Deny** and
**In terminal**, which hands the question back to the agent's own prompt.
The agent's `PermissionRequest` hook holds the request in the daemon while a
window that shows approvals is polling; with none, or once the windows stop,
the agent asks in its terminal as it would without sanbo. A daemon from an
older build than the window's (`protocol.Revision`) leaves agents' state and
approvals out of what it reports; the sidebar's footer and the Nodes tab mark
it out of date, to be restarted with the new build.

Each Claude and Codex session has a **Terminal / Chat** switch at the right of
its pane's tab bar (⌘⇧J, also in the command palette); a plain shell, or an agent without a native
conversation, keeps its terminal alone and draws no switch. Switching keeps
the same live PTY connection and remembers the mode per tab, including when it
moves between panes. Chat reads Claude and Codex's native JSONL conversation
logs on the session's node, refreshes every two
seconds, and renders the user's turns as bubbles, the agent's replies as
Markdown, and each tool call as a compact row naming the tool and its command
or path, with its arguments and output folded away; a failed tool opens to
show its error. A run of three tool calls or more folds into one line counting
them and naming their tools, all but the last run. Text in the conversation
can be selected by dragging and copied, and a message's copy button shows
while the pointer is over it. Under the conversation, the tokens it has spent
and how full its context is, read from the whole log. The daemon
binds each session to its native conversation automatically and
persists that identity with the launch. Claude receives a fixed session ID;
its `SessionStart` hook, and Codex's, run the daemon's program, which reports
each new, resumed, cleared or forked conversation over a private Unix socket.
Reports carry when the hook ran, so a late one never replaces a newer one.
Until the first report, the agent's own writable log identifies its
conversation. On macOS this uses
`lsof`, and on Linux `/proc`; launcher children are included. Modern Codex
versions are launched with `--no-daemon` so the conversation's writer belongs
to the session rather than a shared app server. The process's
`CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `HOME` overrides are respected. Chat has
no history picker: it always shows the conversation belonging to the live PTY.

The composer belongs to Chat alone and sends to the same live terminal
session, even when viewing history; Terminal mode has no second box because
the terminal itself takes the keys, and each pane keeps its own draft. Use
Terminal for interactive menus and special keys; shell and other
agents keep their terminal view. Logs update when the agent saves them,
rather than token by token. Chat shows the recent 2 MB of a log (up to 200
messages, with long content capped), marks omitted content, follows new
messages when at the bottom, and has
a **Latest** button to return there. A daemon upgrade is required for chat on
remote nodes as well.

Closing a tab ends its session, asking first while it runs (Settings ▸
General can turn the question off), except a shell at its prompt, with no
command running in front of it, which closes at once; "Detach tab" in the header's menu closes
the tab and leaves the session running in the sidebar. Tabs arrange into
panes by dragging: onto a pane's left, right, top or
bottom edge to split it there, onto its middle or its tab bar to move the
tab into it, along a tab bar to reorder. A session in the sidebar drags in
the same way, dividers between panes resize them, and a pane closes with its
last tab. The Files tab browses the project of the session shown, on
whichever node it runs, through that node's daemon: folders open as they are
read, dotfiles show or hide with the eye button, what .gitignore leaves out
is dimmed, and each file shows its type's icon, by the associations of Zed's
default icon theme (language logos from Simple Icons, the rest Lucide). A text
file chosen opens in a tab of its own, beside the
terminals and arranged into panes as they are, while an image is previewed
under the tree and enlarged with a click. A row's context menu opens or
previews the file, inserts or copies its path (absolute or relative to the
project), starts a session in a folder, and makes, renames or deletes files and
folders on the node, asking for the name or a confirmation first; the header's
**+** makes a file or folder in the project. Files and folders dragged from the Files tab or the desktop onto
a terminal are typed as shell-quoted paths, onto the composer inserted, and
a folder dropped anywhere else opens a new session in it. The Changes tab
shows the project's git changes against HEAD, read by the daemon of the node
the session runs on (git status when git is installed, go-git otherwise): a tree of
the changed files, each folder adding up its files' lines added and removed,
refreshed every few seconds while shown; a file chosen opens its diff in a
tab of its own, as a file does, read again as the changes are. A diff opened
with a click is a preview, dimmed, whose tab the next file clicked takes,
until it is kept with a double click on its tab or its menu. A diff shows in
one column or side by side, its long lines scrolling sideways or wrapped; the
words that differ between a removed line and the line replacing it are
marked. ⌥⌘↓ and ⌥⌘↑ step to the next and previous changed file. Lines picked
with a click, Shift extending the pick, or a hunk's send button, go to an
agent of the project with a note: into its chat's composer as `path:lines`
and the lines as a diff, for the user to finish and send. A hunk of the work
tree's changes can be undone from its header, once confirmed; the file must
still read as the diff says, or nothing is written. Its header switches branches or makes a new one, and pulls
(fast-forward only), pushes and fetches, showing the commits ahead and behind
the upstream; a branch without an upstream is pushed to its only remote, or
`origin`, and tracks it. Each changed file has a box: the commit box under the
tree commits the files ticked with the message typed, or amends the last
commit. The History tab lists the newest 500 commits of the branches, remote
branches and tags, with their graph, branches and tags; the commit chosen shows
its message, author and files, each file's diff opening in a tab. Git operations run
the git program on the node, with the user's hooks and credentials, and never
wait on a password prompt. A tab dragged over a tab bar opens the gap it would drop into, and
the side panels slide open and closed; the right panel's left edge drags to
resize it, and its width is kept. Tabs that do not fit a pane scroll
sideways, and the pane's tab list menu names them all; a narrow pane folds
its launchers into its **+** menu.

The new-session dialog can start a session in a git worktree of its own: a
new branch, made from the branch checked out, in a folder beside the
repository (`../<repo>.worktrees/<branch>`), so agents working on one
repository do not write over each other's files. The Changes tab of such a
session compares its work with HEAD, or since the branch it started from,
its commits included; its menu merges the branch into that branch in the
main work tree, pushes it and opens a pull request (with the GitHub CLI where
the node has it), or removes the worktree once it has no changes left.

⌘P finds a file of the project shown by the letters of its path, in order,
and opens it; ⌘⇧F searches the project's files for a text in the right
panel's Search tab, a capital letter matching the case, and opens a file at
the line found. Both run on the project's node, with ripgrep or `git grep`
where it has them and walking the folder otherwise, leaving out what
`.gitignore` does.

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
tabs and the sessions, and pins sessions; its Extensions… opens Settings ▸ Extensions in a
dialog over the window.

The languages come from [Zed](https://zed.dev)'s extensions, which
Settings ▸ Extensions finds in Zed's store and installs (under
`extensions/` beside `config.json`): sanbo reads an extension as Zed does,
its languages' `config.toml` for their file names, comment and tab size,
and parses with the tree-sitter grammar it carries, compiled to wasm, run
by the tree-sitter runtime itself compiled to wasm under
[wasmtime](https://wasmtime.dev) (via wasmtime-go, `internal/syntax`), and
colours by the language's `highlights.scm`. The languages Zed builds in rather than
taking from extensions -- Go, Rust, Python, JavaScript, TypeScript and the
like -- come with sanbo, built from their grammars' own repositories
(`internal/syntax/builtin`). An extension's language servers, which its
own wasm starts, are not run yet.

The store lists extensions with MCP servers (Zed's context servers) as
well, and a node's claude and codex sessions get those of the extensions
installed there. To learn how to start one, the daemon runs the
extension's own wasm, the component Zed would run (`internal/zedhost`),
and implements the parts of Zed's extension API a context server uses:
its settings, downloading and unpacking a release, Node.js and npm,
GitHub releases, the platform, processes the manifest asks for, and HTTP.
The extension installs its server in its own folder under
`extension-work/`, beside `extensions/`; the daemon asks again after six
hours, or when the extension or its settings change, and asks first when
it starts, so a session need not wait for an install. Settings ▸
Extensions turns each server on or off and edits its settings (an API
key, a database URL), showing what the extension says they need and
filling in its defaults; they are kept in `config.json` under
`context_servers`, as Zed's own settings name them. A server missing what
it needs is left out, with the reason in the daemon's log. Claude Code
gets the servers in an `--mcp-config` file under `run/`, which is
removed when the session ends; Codex as `-c mcp_servers.…` overrides.

Each pane's tab bar ends in a launcher for every agent the session's node
can start, drawn with the agent's icon: a click starts it in the project of
the tab shown, or with no tab, in the default working directory here and in
the home folder on another node; an agent with profiles opens a menu of them
first. The new-session dialog takes a working directory too, but it can be
left empty for the node's home. A session's icon, on its tab and in the sidebar, hops
while its program reports progress (OSC 9;4), as claude does while it works,
and its dot in the sidebar glows a soft green the while.
Progress is restored on attach; live clear, error and pause reports take
precedence over older session listings. Like Ghostty, reports expire after
15 seconds without a refresh, so programs must send keep-alives during long
operations. Programs that do not emit OSC 9;4 do not show a busy indicator.
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
the file shown, ⌘K command palette, ⌘P go to file, ⌘⇧F search, ⌘B sidebar,
⌘, settings, and ⌘C/⌘V/⌘A for the terminal's clipboard).

Settings (⌘,) cover the app -- language (English or Korean, or the
system's), theme (system, light, dark), terminal font
size, line height, cursor, scrollback, copy-on-select and the
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

Each daemon saves its sessions' names, agents, profiles, launch arguments and
working folders in its private `sessions.json`, next to `config.json`. On restart
it restores the list without launching processes. A session starts again when
its tab becomes visible (the active tab in each split pane), or when a CLI
client attaches. Hidden tabs stay stopped until opened. Explicitly killing a
session removes its saved entry; closing a tab only detaches it.

Restoration resolves the current profile/MCP settings and starts Claude or
Codex with the saved native conversation ID (`--resume ID` / `resume ID`).
The original startup prompt is not sent again. Native IDs are tracked even
without opening Chat. Terminal scrollback and shell processes themselves are
not restored. A saved conversation whose log is missing fails visibly instead
of silently starting an unrelated conversation.

For older saved sessions without a native ID, the daemon can migrate a unique,
unclaimed conversation log created during that launch. Ambiguous or unmatched
existing history is reported rather than choosing the newest conversation.

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

In Settings → Agents, each profile can choose MCP servers from the installed
extensions and configured commands. Turn on its MCP selection switch and tick
the servers to use, then save. Leaving every box unchecked disables these
servers for that profile. In JSON, a profile's `"context_servers": []` selects
none, an array of server IDs selects those servers (even if disabled globally),
and omitting the field inherits all enabled servers. Changes apply to new
sessions.

`context_servers` configures the MCP servers claude and codex sessions get.
Those of the Zed extensions installed are on unless `enabled` is false;
`settings` are the extension's own. A server with a `command` is started as
written, extension or not:

```json
"context_servers": {
  "mcp-server-github": {"settings": {"github_personal_access_token": "ghp_…"}},
  "postgres-context-server": {"enabled": false},
  "my-server": {"command": {"path": "~/bin/my-mcp", "args": ["--stdio"]}}
}
```

## Building

sanbo builds with cgo: wasmtime comes as the static library
[wasmtime-go](https://github.com/bytecodealliance/wasmtime-go) carries for
macOS, Linux and Windows on x86-64 and arm64. `internal/zedhost` calls its
C API directly, with the headers of the same release in
`internal/zedhost/include` (`include/sync.sh` copies them anew after
wasmtime-go moves).

## Security

Transport is QUIC with TLS 1.3 and a self-signed per-node certificate.
Certificates are not verified against a CA; peer identity comes from the sanbo
handshake, optionally gated by the shared `psk`. Do not expose the daemon port
to an untrusted network — keep it on a LAN or a tailnet.
