# Project agents over MCP

Mako gives supported agent sessions a built-in MCP server named `mako`.
It lets an agent delegate work to a new Mako session and communicate with
other sessions in the same local project. It is added alongside the
extension and custom MCP servers already configured for the session.

Claude Code, Codex, OpenCode, pi and omo can receive it through their
drivers. A configured command must still be recognized as a supported
agent executable. Shell sessions and drivers without launch-time MCP
support do not receive these tools.

## Tools

| Tool | Arguments | Result |
| --- | --- | --- |
| `list_agents` | `{}` | Your `self_id`, project, project sessions with status and parent IDs, and available agent types and profiles with descriptions and `spawnable` flags. `messaging` says whether a session can receive messages now. |
| `spawn_agent` | `prompt`, optional `agent`, `profile`, `name` | Starts a new session in your working directory, with the same worktree metadata. Defaults to your agent type. Returns its session `id`. |
| `send_message` | `to`, `text` | Stores a message in the recipient's inbox. Returns its `id`, authenticated `from`, `to`, text and creation time. |
| `read_messages` | Optional `after_id`, `ack_through`, `limit`, `wait_seconds` | Reads your own inbox. Returns `messages` and `next_after_id`. |
| `stop_agent` | `session_id` | Terminates and removes a session you spawned directly, including a child whose process has already exited. |

The MCP client may prefix tool names with the server name. The server's
initialization instructions identify your session and parent. A spawned
task's initial prompt also names its parent and asks it to report results
using `send_message`.

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

The child replies to the parent ID with `send_message`. The parent can wait
for that reply with `read_messages`:

```json
{"after_id":0,"wait_seconds":30}
```

After processing a batch, acknowledge its last message ID:

```json
{"ack_through":42,"after_id":42,"wait_seconds":30}
```

Reading is non-destructive. Repeating a read after a lost connection returns
the same messages until they are acknowledged. IDs increase across the
daemon's mailboxes, so a cursor may have gaps. A read returns up to 50
messages by default; `limit` can be 1–100 and `wait_seconds` 0–30. Waiting
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

Recent messages show their sender, recipient, time and whether they are
pending, acknowledged, or addressed to a removed session. Expand a message
to read its full text. Monitoring does not consume or acknowledge messages.
The most recent 200 messages per project are retained for monitoring even
after acknowledgement; the panel fetches the latest 50. This history is
saved alongside the inboxes and also survives daemon restarts.

The panel can monitor a project on a remote node through the mesh; that
node needs protocol revision 15 or later. MCP agent messaging itself stays
local to each node.

Messages do not inject terminal input or interrupt the recipient's current
turn. A recipient needs to call `read_messages` to receive them. An agent
that should keep responding to requests can wait for messages in a loop.
Messages can be sent to active messaging sessions in the same project,
including siblings. Currently this server coordinates sessions on one
daemon; it does not relay messages to another node.

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
rejects a profile that would disable the child's communication tools.
Changes apply when a session is started or restored.

The server uses stdio through the daemon's own executable. Its private
`agents.sock` socket accepts a fresh per-session credential, passed in the
MCP server's environment. Tool arguments cannot choose the sender identity
or a different project. Credentials are revoked when a session exits;
restarted sessions receive new credentials. Projects are compared using
canonical local paths, with worktrees grouped by their repository path.
