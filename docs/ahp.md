# Agent Host Protocol endpoint

makod can serve its sessions to [Agent Host Protocol](https://microsoft.github.io/agent-host-protocol/) (AHP) clients. An AHP client is an editor, a web page or another program that speaks AHP 1.x, for example a client built on the official Go or TypeScript client. The endpoint uses [gosuda/go-ahp](https://github.com/gosuda/go-ahp) for the protocol work: state, order, replay and reconnect.

The endpoint is off by default.

## Turn it on

1. Add an `ahp` section to the daemon's `config.json`, with a loopback address:

   ```json
   "ahp": { "listen": "127.0.0.1:8787" }
   ```

2. Restart makod.
3. Read the token from `ahp-token` in the configuration directory. makod makes this file with mode `0600` when it starts the endpoint for the first time.
4. Connect the client to `ws://127.0.0.1:8787/ahp`. Send the token as `Authorization: Bearer <token>`, or as the `token` query parameter if the client cannot set headers.

makod also serves the endpoint on `ahp.sock` in its run directory, without a token. The socket has mode `0600`, so only its owner can connect.

| Key | What it does |
|---|---|
| `ahp.listen` | A loopback `host:port` for the WebSocket endpoint. makod refuses an address that is not loopback. An empty value turns the endpoint off. |
| `ahp.origins` | Browser origins, as `path.Match` patterns of `host:port`, that can connect besides the endpoint's own. Requests without an `Origin` header are not checked. |

To reach the endpoint from another machine, use an SSH tunnel or a tailnet. makod does not listen on a public interface.

## What a client sees

Each running agent session that the daemon reaches is an AHP session, on this node and on the other nodes of the mesh.

| Mako | AHP |
|---|---|
| Session `<id>` | Session `ahp-session:/<id>`, with one chat `ahp-chat:/<id>`. The provider is the session's agent, as `claude` or `codex`. |
| Name or title | `title` |
| Project folder | `workingDirectories` and `project` |
| A user message and what follows it | A turn: the user message, then a markdown part for each reply text and a tool call for each call. |
| The agent at work (`working`, `waiting`) | The turn stays active. When the agent stops, the turn completes. |
| A pending approval | A tool call `approval:<approval id>` that waits for confirmation. |
| The shared draft | The chat's `draft`, in both directions. |

The conversation is the window of latest messages that `read_chat` gives. Older turns are not loaded.

## What a client can do

| AHP action | Effect in Mako |
|---|---|
| `chat/turnStarted` | The message goes to the session as a prompt, typed as the composer types it. The next user message in the log becomes this turn. |
| `chat/turnCancelled` | makod sends Escape to the agent. |
| `chat/pendingMessageSet`, kind `steering` | The message goes to the agent at once, while it works. |
| `chat/pendingMessageSet`, kind `queued` | The message waits in the AHP chat. When the active turn ends, it starts the next turn. |
| `chat/toolCallConfirmed` on an `approval:` call | It answers the approval, `approved` as allow or deny. |
| `chat/draftChanged` | It sets the session's shared draft. |

The bridge follows each session as a front-end called `AHP`, so other front-ends show it among the session's clients. While an AHP client is active on a session (`session/activeClientSet`), the session's approvals wait for an answer in a window or in AHP. With no active client, they go to the agent's terminal, unless a Mako window holds them.

`createSession` is refused. Start sessions in Mako.

## Limits

- Input requests (`chat/inputRequested`), terminals, resources and changesets are not mapped yet.
- A text that the agent rewrites, rather than extends, keeps the first version in AHP.
- A turn that a client started ends empty if its message does not reach the log within 20 seconds of the agent going idle.
