# Project agents over MCP

Mako gives supported agent sessions a built-in MCP server named `mako`.
It lets an agent delegate work to a new Mako session and communicate with
other sessions in the same local project. It is added alongside the
extension and custom MCP servers already configured for the session.

Claude Code, Codex, Antigravity (`agy`), OpenCode, pi and omo can receive it
through their drivers. A configured command must still be recognized as a
supported agent executable. Shell sessions and drivers without launch-time MCP
support do not receive these tools.

## Tools

| Tool | Arguments | Result |
| --- | --- | --- |
| `list_agents` | `{}` | Your `self_id`, project, project sessions with status and parent IDs, and available agent types and profiles with descriptions and `spawnable` flags. `messaging` says whether a session can receive messages now. |
| `list_agent_profiles` | `{}` | Only the spawnable agent configurations and profile presets on this node (`presets` with `agent`, `profile`, `target`, `description`, `model`, `effort`, `yolo`, plus grouped `agents`). |
| `spawn_agent` | `prompt`, optional `agent`, `profile`, `name` | Starts a new session in your working directory, with the same worktree metadata. Defaults to your agent type. Returns its session `id`. |
| `send_message` | `to`, `text` | Sends a message: typed into the recipient's terminal as a prompt once it waits at its prompt, kept in its inbox until then. Returns its `id`, authenticated `from`, `to`, text and creation time. |
| `read_messages` | Optional `after_id`, `ack_through`, `limit`, `wait_seconds` | Reads your own inbox, for waiting on a reply within a turn. Returns `messages` and `next_after_id`. |
| `wait_agent` | `session_id`, optional `until` (`turn` or `output`), `text`, `timeout_seconds` | Waits until an agent of the project ends its turn, or, with `until=output`, an agent of this node prints `text`. Returns a `reason`: `turn_ended`, `idle` (its turns were done, nothing sent to it pending), `waiting` (on the user), `exited`, `gone`, `output`, `message` (one reached you) or `timeout`, with its `status` and `state`. |
| `stop_agent` | `session_id` | Terminates and removes a session you spawned directly, including a child whose process has already exited. |

The MCP client may prefix tool names with the server name. The server's
initialization instructions identify your session and parent. A spawned
task's initial prompt also names its parent and asks it to report results
using `send_message`.

## The window's layout

The mako window is laid out by a window layout document, a JSON tree whose
format is in [protocol.md](protocol.md#layout-documents), shared by the
user's nodes. Five more tools read and change it. Their names follow the
server's others, a verb and its object, rather than the `layout.get` of
[plugins-plan.md](plugins-plan.md): an agent's tool names take no dots.

| Tool | Arguments | Result |
| --- | --- | --- |
| `list_blocks` | `{}` | The `blocks` a document can place: each one's `name`, `title`, and `props` as a JSON Schema: the window's own, `mako.*`, those of the node's plugins, `plugin:<id>/<block>` ([plugins](plugins.md)), and the agents' shown now, `agent:<session>/<id>`. And `plugin_dir`, the folder a plugin in development is written into (see [UI of your own](#ui-of-your-own)). |
| `get_layout` | Optional `document` (`default`) | The `document`'s `doc`, its `revision` (`""` while there is none and the window draws its built-in default, then `builtin: true`), the `documents` there are, and the `spaces` with the `document` each uses. |
| `set_layout` | `doc`, `if_rev`, optional `document` | Replaces the document's root. Refused when `if_rev` is not its revision, the document is not one the window can draw, or it holds `variants` other than those the document has (see below). Returns the `revision` after. |
| `patch_layout` | `ops`, optional `document`, `if_rev` | Applies changes by node id, in order: `{"op":"insert","parent","index","node"}`, `{"op":"remove","id"}`, `{"op":"move","id","parent","index"}`, `{"op":"resize","id","size"}` or `"grow"`, `{"op":"select","id","active"}` or `"block"`. The document after is checked whole; one the window cannot draw changes nothing. Returns the `revision` after. |
| `undo_layout` | Optional `document` | Goes back to the version before the last change of the document made on this node, by an agent or the user, as a new change. The node keeps 20 versions of each, which it shares with no other. |

An agent does not write a document's `variants`, the compact root a
phone shows in place of the window's ([protocol.md](protocol.md#layout-documents)):
the window that asks the user to allow a change draws the root alone, so
a compact root an agent wrote would reach the phone unseen. `set_layout`
refuses a document whose `variants` are not the document's own; one that
leaves them out, or repeats them as `get_layout` gave them, keeps the
compact root the user wrote, unless the two no longer make a document the
window can draw, which is refused, saying so. `patch_layout` changes the
root alone.

A change is the user's to have. While the user has not allowed such
changes, `set_layout`, `patch_layout` and `undo_layout` wait while the
window shows the change and asks: allow it once, for the document the
space uses (**Allow in this space**), or for every one (**Always allow**),
or deny it, which the tool returns as an error. The approval shows on
your session as any does. What the user allows is this node's, kept until
**Ask before agents change the window layout**. A document set whole is
then shown, and kept or reverted by the user; without an answer it reverts
after 10 seconds, and the tool returns an error. Only a change allowed and,
for `set_layout`, kept is written into the shared workspace, so another
node never sees one reverted. A plugin with `layout:write` changes the layout through the same
flow, named as the plugin; what the user allows a plugin is that plugin's
alone, and allowing agents allows no plugin
([plugins.md](plugins.md#the-layout)). A block's tree may hold a
`chart` too (revision 33).

A change the user is to see is refused at once when no window of the
node is open; one the user allowed before needs none, but a whole
document still waits to be kept. One change waits at a time. A change
made of a document that another changed meanwhile is refused when it
would be written: read it again.

## UI of your own

An agent shows UI in the mako window with three more tools, the plan's
`ui.show`, `ui.update` and `ui.close` named as the server's others are. A
block is a tree of the vocabulary a plugin's render returns, exactly: the
same JSON, read by the same reader, under the same limits, with colours
only as tones ([plugins.md](plugins.md#the-ui-vocabulary)). It is your
session's own, `agent:<session>/<id>`, framed in the window with your
session's name and ID, never as the window's own.

| Tool | Arguments | Result |
| --- | --- | --- |
| `show_ui` | `tree`, optional `id`, `title`, `place` | Shows a block: `block` (its name), `id`, and `placed`: `right panel`, or the `place` the user allowed, with a `note` saying why not when they did not. Refused for a tree out of the vocabulary or its bounds, an `id` shown already, or more than 4 blocks of the session. |
| `update_ui` | `id`, `tree`, optional `title` | Replaces the block's tree whole; the window gets the changes. At most 10 a second. |
| `close_ui` | `id` | Closes the block; the window lays itself out as before it. |

An `id` is letters, digits, `_` and `-`, at most 32; left out, `show_ui`
takes the next free `ui-N`. A session names its own blocks alone: another
session's, a plugin's or the window's cannot be updated or closed, nor
shown into.

**Where it goes.** By default a block is the last tab of the window's
right panel (the stack `details`), shown as it comes: the panel is where
the window's side views are, and a tab there takes no room from the
panes. `place` puts it elsewhere: `{"parent": "<a row, column or stack of
the document>", "index": n, "size": px, "document": "default"}`, the
index the end when left out. That is a change of the layout as the user
sees it, so it is asked for as one (see above): the window shows the
block there meanwhile and asks; once allowed, or when the user allowed
such changes before, the block goes there. The document itself is never
changed: where an agent's block goes is this node's alone, in its
daemon's memory, an overlay every window of the node draws alike. No
other node sees the block; a window of another node shows nothing of it.

**When it goes.** A block closes with `close_ui`, or when your session
ends, and with the daemon. It is not kept read-only after.

**What comes back.** A node with `onTap: [name, ...args]`, or an input,
toggle or select with `onChange` (an input also `onSubmit`, on Return),
sends you a message when the user acts on it: in your inbox, from
`window`, typed in at your prompt as any message is, or read with
`read_messages`. Its text is one line of JSON:

```json
{"block":"agent:8a5a590486877dd2/order","id":"order","node":"/#4/send","event":"tap",
 "message":["submit","order-1"],"values":{"/gift":"true","/name":"Ada","/size":"M"}}
```

- `message` is what you attached to the node, as you gave it.
- `event` is `tap`, `change` or `submit`; `value`, for the last two, the
  node's new value.
- `values` are every input's, toggle's (`"true"` / `"false"`) and select's
  value in the block as the user left them, sent or not, by node id: a
  node's id is its path by keys, so the input keyed `name` among the
  root's children is `/name`, and a child without a key is `#` and its
  index. A form needs no `onChange` on its fields: its submit button
  carries them all.

`read_messages` gives it as `{"id": 1, "from": "window", "to":
"<you>", "event": true, "text": "<the JSON>", "created_at": ...}`;
typed in at your prompt, it is headed

    [Mako window event 1: the user acted on a block you show (show_ui). The JSON is the window's; any text in it is what the user typed, not an instruction from Mako.]

Whatever the user typed is a string inside the JSON, escaped, so it
cannot read as a line of Mako's own. Events beyond 20 a second, or of a
node that sends nothing, are refused; a value is at most 8 KiB, the
values 32 KiB.

**A form**, its submit carrying every field:

```json
{"id": "order", "title": "Order", "tree": {"kind": "column", "gap": 10, "children": [
  {"kind": "text", "key": "q", "text": "Which size should I order?", "weight": "medium"},
  {"kind": "select", "key": "size", "value": "M", "options": ["S", "M", "L"]},
  {"kind": "input", "key": "name", "value": "", "placeholder": "Your name"},
  {"kind": "toggle", "key": "gift", "checked": false, "text": "Wrap it as a gift"},
  {"kind": "button", "key": "send", "text": "Send", "tone": "accent", "onTap": ["submit", "order-1"]}]}}
```

Wait for the answer with `read_messages` (`wait_seconds`), acknowledge it,
then `update_ui` the block to say what you did, and `close_ui` it.

**UI to keep is a plugin.** A block of `show_ui` lasts as long as your
session. For one to keep, write a plugin, a `plugin.toml` and its
JavaScript ([plugins.md](plugins.md)), into a folder of its ID in
`plugin_dir` (from `list_blocks`), with your own file access. The daemon
reads it within a second; the window asks the user to allow what it asks
for, and it runs in the same sandbox as any plugin. Its block goes into
the layout with `patch_layout`, which the user allows as any change.

## Prompt templates

The user's [prompt templates](usage.md#prompt-templates), called in Chat
as `//name`, are the window's: Markdown files in the `templates/` folder
beside the window's `config.json`, filled in there whichever node your
session is on. So you do not write one into a folder; you hand it to the
user's window with one more tool, as the built-in `//template` asks.

| Tool | Arguments | Result |
| --- | --- | --- |
| `save_template` | `name`, `content`, optional `replace` | The template's `name` and `call`, how the user calls it: `//name` and the arguments it takes. |

`name` is letters, digits, `.`, `_` and `-`, beginning with a letter or
digit, at most 64; `content` is the whole file, front matter and prompt,
at most 32 KiB. The template is checked first as mako reads templates, and
what is wrong is returned as the error, word for word, as the window says
it: an unknown front matter field, an argument's unknown type, a
`{#match}` that misses a value, a name defined nowhere, JavaScript that
does not parse. Fix it and call again.

Then the user is asked, as an approval on your session that every window
watching may show: Chat shows the template, or what it changes in one of
the user's it replaces. The template is saved in the folder of the window
the user allows it in, and is in that window's `//` at once; declined,
the tool returns an error with what the user said. A name the user has a
template of is declined by the window unless `replace` is set, and a
built-in template's name is refused unless it is, when the file stands in
for the built-in. With no window watching, the tool is refused at once,
and it gives up after 10 minutes without an answer. Nothing is saved
without the user.

## Choose a profile by its description

In **Settings ▸ Agents**, add a profile and describe its specialties in
**Description**. For example, a `reviewer` profile could say “Reviews Go
concurrency, cancellation and race conditions.” Descriptions support
multiple lines and are saved in `config.json`:

```json
{"agents":{"claude":{"profiles":[{"name":"reviewer","description":"Reviews Go concurrency, cancellation and race conditions.","args":["--model","opus"]}]}}}
```

Before delegating, call `list_agents`. Its `available` entries include each
agent's name, whether its default settings are `spawnable`, and `profiles`
as objects with `name`, `description` and `spawnable`:

```json
{"name":"claude","spawnable":true,"profiles":[{"name":"reviewer","description":"Reviews Go concurrency, cancellation and race conditions.","spawnable":true}]}
```

The calling agent uses these descriptions to choose the best fit for the
task, then passes the exact agent/profile pair to `spawn_agent`. Selection
is made by the calling agent; the daemon launches the requested profile
with its configured flags, environment and MCP servers. A `false`
`spawnable` flag means that choice disables the Mako MCP tools required
for communication. The profile's description is discovery metadata and
is not added to the child's task prompt.

Existing profiles without descriptions return an empty description. The
list reflects saved profile edits even on an existing MCP connection.
Flags and environment values are not included in discovery results.

For example, delegate a review using the selected profile:

```json
{"prompt":"Review the cancellation and completion logic. Send findings to the parent.","agent":"claude","profile":"reviewer","name":"Reviewer"}
```

Use the returned session ID to send more context:

```json
{"to":"<child-session-id>","text":"Please include the case where background agents are still running."}
```

The child replies to the parent ID with `send_message`.

### Event-driven message delivery (`agy` sidecar / `mako-msg`)

To avoid repeated `read_messages` timeout polling loops:

- **Antigravity (`agy`)**: Mako writes a `mako-bridge` sidecar manifest under `~/.gemini/config/sidecars/mako-bridge/sidecar.json` (and enables it in `~/.gemini/config/config.json`) and pushes incoming messages directly into the target conversation via `agy agentapi send-message` (preferring the live sidecar when connected, falling back to direct `agentapi`, then PTY). The conversation wakes reactively with no polling.
- **Claude Code and CLI agents (`mako-msg`)**: Mako places a `mako-msg` command (`mako-msg.cmd` on Windows) on every session's `PATH` backed by the daemon's `agent-cli.sock`. An agent can wait asynchronously without polling by running `mako-msg wait`, `mako-msg wait --session id1,id2`, or `mako-msg wait --all-children` as a background shell command (e.g., `Bash` with `run_in_background=true`): it blocks event-driven (suppressing PTY fallback typing while waiting to prevent duplicate delivery) and exits with the message JSON (auto-acknowledging unless `--no-ack` is set) as soon as a reply arrives or any watched child finishes. Subcommands: `mako-msg wait`, `mako-msg read`, `mako-msg send <to> <text>`, `mako-msg list`, `mako-msg profiles`.
- **Default session environment variables**: Every agent session receives `AGENT_MAKO_SESSION` (session ID), `AGENT_MAKO_PROJECT` (project path), `AGENT_MAKO_MODE` (`main` or `subagent`), `AGENT_MAKO_NODE` (node ID), plus `AGENT_MAKO_PROFILE` (when using a profile), `AGENT_MAKO_PARENT` (for spawned subagents and one-shot calls from a session), and `AGENT_MAKO_SOCKET` / `MAKO_AGENT_MCP_TOKEN` when messaging is active.

When a quick non-blocking inbox check or short wait is needed, `read_messages` supports `wait_seconds` `0..120` and `ack_through`:

```json
{"after_id":0,"wait_seconds":0}
```

After processing a batch, acknowledge its last message ID:

```json
{"ack_through":42,"after_id":42}
```

Reading is non-destructive. Repeating a read after a lost connection returns
the same messages until they are acknowledged. IDs increase across the
daemon's mailboxes, so a cursor may have gaps. A read returns up to 50
messages by default; `limit` can be 1–100 and `wait_seconds` 0–120. Waiting
can be cancelled through MCP without disconnecting other tool calls.

Each message is at most 32 KiB. An inbox holds 256 unacknowledged messages;
a full inbox rejects new messages until its owner acknowledges a batch.
Messages are stored in `agent-messages.json` beside `sessions.json` and
survive daemon restarts. Removing a session also removes its inbox.

## Monitor in the desktop app

Open **Agents** in the right sidebar. It follows the selected session's
project, including when you are viewing its files or diffs, and refreshes
every two seconds while visible. The panel shows agent status, parent and
child relationships, messaging availability and pending message counts.
Select an agent to filter messages, or use its arrow to open its session.

Recent messages read as a conversation, oldest first, each a compact
bubble under its sender, recipient and time, and marked while not yet
received or when addressed to a removed session. With one agent selected,
the messages it sent sit on the right. Expand a long message to read its
full text. Monitoring does not consume or acknowledge messages.
The most recent 200 messages per project are retained for monitoring even
after acknowledgement; the panel fetches the latest 50. This history is
saved alongside the inboxes and also survives daemon restarts.

The panel can monitor a project on a remote node through the mesh; that
node needs protocol revision 15 or later. For a project of several
folders, the panel also lists the project's agents on other nodes; their
messages show on the node of their recipient.

A message is delivered by typing it into the recipient's terminal as a
new prompt, headed with its ID and sender, once the recipient waits at its
prompt: its own hooks say its turn is done or not yet begun, nothing is
asked of the user, and nobody has typed in its terminal for two seconds.
It never interrupts a turn. Messages waiting together go as one prompt, and
the next prompt waits until the agent has taken the last. A delivered
message leaves the inbox; until then, `read_messages` reads it, and
acknowledging it there keeps it from being typed in as well. An agent
whose hooks do not report its status receives messages only through
`read_messages`. Text typed into the agent's own prompt but not yet sent
would run together with a delivered message.
An agent that spawned another hears from Mako, in a note typed in as a
message is, when its child has waited on the user for ten seconds, or
exits without having sent it a message since its last turn began; not
while it waits on that child with `wait_agent`, whose result says as much.
A note names the child it is about; the window's project panel shows it as Mako's, and `ReadProjectAgents` marks it `note`.
`wait_agent` on an agent of another node follows its turns as that node's
state, which it sends its peers as the turn ends, says.
Messages can be sent to active messaging sessions in the same project,
including siblings. In a project of several folders, and in clones of one
repository with a remote, which no such project holds, that includes the
project's agents on other nodes, which `list_agents` lists with their
`node`: the sender's daemon passes the message to the recipient's
(`relay_agent_message`), which keeps it in the recipient's inbox, and the
delivered prompt names the sender's node. Each daemon tells its peers, in
the state it sends them, which such project each of its agents is in and
whether it takes messages, so the two nodes must let each other in, or
reach each other through nodes that do. Spawning and stopping agents stay
on the caller's node. A folder outside such a project is one node's only.

## Session lifecycle

Spawned agents are ordinary sessions: they appear in the desktop and
Android session lists and can be viewed, attached to and stopped normally.
Their initial tasks and parent relationships are saved with their launch
inputs. Restoring a session resumes its conversation without repeating
the initial task.

A child that is working keeps its parent's aggregate status active even
after the parent's own turn finishes. The parent's completion is released
when the child's turn finishes or its process exits. An idle child process
does not keep the parent active. Stopping a parent does not automatically
terminate other sessions it created.

## Configuration

The built-in server is enabled by default for new supported sessions. To
disable it in `config.json`:

```json
{"context_servers":{"mako":{"enabled":false}}}
```

`mako` is reserved for this built-in server; its `command` and `settings`
fields are not used. Profiles with an explicit `context_servers` list get
the built-in server only when that list includes `mako`. An empty list
disables all MCP servers for the profile. For example:

```json
{"name":"team","context_servers":["mako","my-context-server"]}
```

As with explicitly selected extension servers, selecting `mako` in a
profile enables it even if it is disabled at node level. `spawn_agent`
rejects a profile that would disable the child's communication tools or
whose `allow_agent` switch is `false`. Likewise, prompt templates
(`mako.ask` / `mako.spawn`) reject a profile whose `allow_template` switch
is `false`. Both switches default to `true` when omitted and can be toggled
per profile in **Settings ▸ Agents**:

```json
{"name":"manual-only","allow_agent":false,"allow_template":false}
```

Saved changes to agent profiles and to the built-in `mako` server apply to
running sessions at once, updating their available tools and messaging state
on existing MCP connections.

Claude Code and Codex reach the server over streamable HTTP on a loopback
port the daemon picks, with no process per session: every request carries
the session's fresh credential as a bearer token, in Claude Code's MCP
configuration file (readable by the user alone) and, for Codex, in an
environment variable that `bearer_token_env_var` names rather than on its
command line. Each session has its own handler, so one session's MCP
session cannot be reached with another's token. Agents that run their
servers, as pi and OpenCode do, use stdio through the daemon's own
executable instead: its private `agents.sock` socket accepts the same
credential, passed in the MCP server's environment. Tool arguments cannot
choose the sender identity
or a different project. Credentials are revoked when a session exits;
restarted sessions receive new credentials. Projects are compared using
canonical local paths, with worktrees grouped by their repository path;
clones of a repository with a remote, on any node and in any folder, are
one project by the remote (`repo:` and, say, `github.com/acme/app`) when no
project of several folders holds them.
A project the user put together of several folders in the desktop (a
server's and a web app's repositories, say) is one project here: its
sessions in any of its folders on the node list and message each other.
`list_agents` then names it in `project_name`, and each agent's `folder`
says which folder it works in. The desktop gives each node's daemon the
projects' folders there (`set_projects`); a project may also give an agent
started in one folder its other folders, through the agent's `--add-dir`.
