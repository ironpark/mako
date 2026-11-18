# Writing a mako plugin

A plugin adds to the mako window: blocks a window layout document places,
commands in the palette, and settings. It is a folder of a manifest,
`plugin.toml`, and JavaScript, run by the node's daemon in a sandbox of its
own; it reaches anything outside only through the Host API, and only as
far as the user allowed. The window draws what a plugin's blocks describe,
with its own components, in its own theme, inside a frame naming the
plugin. The design, and what is still to come, is in
[plugins-plan.md](plugins-plan.md); the wire format of the blocks is in
[protocol.md](protocol.md#plugin-blocks).

## Contents

- [A plugin's folder](#a-plugins-folder)
- [The manifest](#the-manifest)
- [The module](#the-module)
- [The UI vocabulary](#the-ui-vocabulary)
- [The Host API](#the-host-api)
- [Permissions](#permissions)
- [Limits](#limits)
- [Developing one](#developing-one)
- [Installing from a registry](#installing-from-a-registry)
- [Publishing](#publishing)
- [For mako's developers](#for-makos-developers)
- [Publishing the example to the default registry](#publishing-the-example-to-the-default-registry)

## A plugin's folder

In this version a plugin is a folder of the `plugins` folder beside the
daemon's `config.json` (`~/.config/mako/plugins/<id>/`, or
`$MAKO_HOME/plugins/<id>/`), named for the plugin's ID:

    plugins/
      ci-status/
        plugin.toml
        main.js
        lib/runs.js      (any .js file the entry imports)

The daemon reads the folder again whenever a file in it changes, as in
development (see [Developing one](#developing-one)). A folder is written
there by hand, or by the daemon installing a signed package from a
registry ([Installing](#installing-from-a-registry)); the daemon runs both
alike, and tells them apart by what it installed.

[examples/plugins/ci-status](../examples/plugins/ci-status) is a complete
plugin: a block of a GitHub repository's latest Actions runs, kept in its
store for when GitHub cannot be reached, a command that runs the latest
failed one again, and its repository and token as settings. Copy it into
the plugins folder to try it.

## The manifest

```toml
id = "ci-status"          # the folder's name; lowercase letters, digits, hyphens
version = "0.1.0"         # major.minor.patch
publisher = "ironpark"
api = 1                   # the Host API version; this mako takes 1
entry = "main.js"         # the entry module, relative to the folder
permissions = ["net:api.github.com", "storage", "sessions:read"]

[[contributes.block]]
id = "ci"                 # registered as plugin:ci-status/ci
title = "CI"
slots = ["right", "bottom"]   # where a layout might place it: left, right, top, bottom

[[contributes.command]]
id = "rerun"
title = "Re-run the latest failed CI run"

[contributes.settings]    # the settings page is made from this
repo = { type = "string", label = "Repository", description = "owner/name on GitHub" }
token = { type = "secret", label = "GitHub token" }
```

The manifest is read strictly: a key mako does not know, a value of the
wrong type, an ID or path out of its grammar, or an `api` other than 1 is
refused, and the plugin does not run, the reason shown in Settings ▸
Plugins and in its blocks' places.

| Key | |
|---|---|
| `id` | Lowercase letters, digits and inner hyphens, at most 64; not starting with `mako`. It must be the folder's name. |
| `version` | `major.minor.patch`, with an optional `-pre` part. |
| `publisher` | As `id`. |
| `api` | 1. |
| `entry` | A relative path to a `.js` file in the folder: no `..`, no leading `/` or `./`, no backslash. |
| `permissions` | At most 16; see [Permissions](#permissions). |
| `contributes.block` | At most 16: `id` (lowercase letters, digits, hyphens, at most 32), `title` (at most 80 characters), `slots`. |
| `contributes.command` | At most 32: `id`, `title`. |
| `contributes.settings` | At most 32, by key (lowercase letters, digits, `_`): `type` (`string`, `secret`, `boolean`, `number`, `select`), `label`, `description`, `options` (a select's, and only a select's), `default` (of the type; none for a secret). A secret's value is kept in the system's keychain ([Secrets](#secrets)), at most 2 048 bytes. |

Titles, labels and options may not hold control characters or the Unicode
marks that reorder or hide text.

## The module

The entry module's default export is the plugin. `activate` runs when the
plugin starts, and registers what the manifest declares; registering a
block or command the manifest does not declare throws, and an `activate`
that throws stops the plugin.

```js
let mako;

export default {
  activate(m) {
    mako = m;
    mako.blocks.register("ci", {
      state: { runs: [] },                       // JSON: kept while the plugin sleeps
      refresh: 60,                               // load again every 60 s while shown (at least 15)
      async load(s) {                            // when the block is first shown, and on refresh
        const res = await mako.http.fetch({ url: "https://api.github.com/repos/o/r/actions/runs" });
        s.runs = JSON.parse(res.body).workflow_runs;
      },
      render: (s, ui) => ui.column([             // the state drawn; it may not call the Host API
        ui.text("Latest runs").weight("medium"),
        ui.list(s.runs, (r) => ui.row([ui.pill(r.conclusion, { tone: "danger" }), ui.text(r.name)],
          { key: r.id, onTap: ["open", r.id] })),
      ]),
      on: {                                      // what each message a node sends does
        open: (s, id) => mako.commands.runCommand({ id: "mako.openUrl", arg: s.runs.find((r) => r.id === id).html_url }),
      },
    });
    mako.commands.register("rerun", async () => {
      const s = mako.blocks.state("ci");         // a command may change a block's state
      s.note = "started";
      mako.blocks.reload("ci");                  // and have its load run once the command ends
    });
  },
};
```

The model is Elm's: a block's state lives in the plugin; `render(state,
ui)` returns a tree; a node's `onTap: [name, ...args]` sends `name` to the
block's `on`, which changes the state, and the block is drawn again after
every call (`load`, a message, a command). An input's, toggle's or
select's `onChange` sends its value last. Calls into the plugin run one at
a time.

`mako` is the [Host API](#the-host-api), with `mako.blocks` (`register`,
`state`, `reload`) and `mako.commands.register`. There are no timers: a
block that wants to be read again every so often says `refresh`. Its
types, with every method's, are in
[mako.d.ts](../internal/plugin/hostapi/mako.d.ts), generated from the
schema: point your editor at it.

## The UI vocabulary

| Kind | Builder | |
|---|---|---|
| `column`, `row`, `stack` | `ui.column(children, opts)` | Children one over another, side by side, or over each other. A child with `grow` shares the room left. |
| `list` | `ui.list(items, (item, i) => node, opts)` | A column of rows; a row that sends a tap lights up under the pointer, as the window's lists do. |
| `text` | `ui.text(text, opts)` | `weight` (`medium`, `semibold`), `size` (`small`, `large`), `tone`. |
| `icon` | `ui.icon(name, opts)` | A [bundled icon](../internal/gui/iconset/icons)'s name; another is drawn as a square. |
| `pill` | `ui.pill(text, opts)` | A state in a word, on a wash of its tone. |
| `chip` | `ui.chip(text, opts)` | A name of the machine's kind: a hash, a branch. |
| `button` | `ui.button(text, opts)` | `icon` beside the text; tone `accent` fills it, `danger` warns. |
| `input` | `ui.input(value, opts)` | `placeholder`; `onChange` on leaving it, `onSubmit` on Return. |
| `toggle` | `ui.toggle(checked, label, opts)` | `onChange` with `true` or `false`. |
| `select` | `ui.select(value, options, opts)` | `onChange` with the option. |
| `markdown` | `ui.markdown(text, opts)` | Drawn as the window draws Markdown. |
| `code` | `ui.code(text, language, opts)` | Monospaced. |
| `progress` | `ui.progress(value, opts)` | From 0 to 1. |
| `divider` | `ui.divider(opts)` | A hairline. |
| `chart` | `ui.chart(type, series, opts)` | `type` `line`, `bar` or `sparkline` (a line alone, no axes, as tall as a line of text); `series` 1 to 4 of `{name, values, tone}`, each up to 200 finite numbers, 600 in all; `opts.labels` name the points, as many as each series has. A series's colour is its `tone`, or the theme's chart colours in order. It sends no messages. |

Options (`opts`, or the node's methods of the same names): `key`, `tone`,
`weight`, `size`, `grow`, `gap`, `icon`, `disabled`, `onTap`, `onChange`,
`onSubmit`, `placeholder`, and a chart's `labels`.

The chart is new in revision 33 of the protocol: a window or phone that
does not know a kind draws nothing in its place, and the Android app does
not draw charts yet. A block that must read there says what a chart
shows in text beside it too.

**Colours are tokens**: `tone` is `muted`, `faint`, `accent`, `success`,
`warning` or `danger`, which the theme in force gives a colour; a node has
no other colour. A tree is refused, and the block shows why, for a kind or
field not in the vocabulary, a raw colour, more than 2 000 nodes or 24
levels, two siblings of one key, or text with control or reordering
characters.

**Keys.** Give a list's rows a `key` (a string or number, without `/` or
`#`): a node's id is its path by keys, so a row that moves is moved, not
redrawn, and a message's node is the one the user saw.

An agent can show a block of this vocabulary without writing a plugin:
the `show_ui` tool takes a tree in the very JSON a render returns
([agent-mcp.md](agent-mcp.md#ui-of-your-own)), and the user's actions on
it come back to the agent as messages.

## The Host API

Everything a plugin may ask of mako. Every method takes one object and
returns a promise of one; it fails, with a message saying why, when the
plugin lacks the permission. The schema, with every field, is
[proto/mako/plugin/v1](../proto/mako/plugin/v1); the types are in
[mako.d.ts](../internal/plugin/hostapi/mako.d.ts).

| Method | Permission | |
|---|---|---|
| `log.write({level, message})` | none | A line in the daemon's log, under the plugin's name; 120 lines a minute. |
| `settings.getSetting({key})` | none | `{setting: {key, type, text, flag, number, set}}`: the user's value, else the default. |
| `settings.listSettings()` | none | Every setting. |
| `storage.getItem({key})` | `storage` | `{found, value}`. |
| `storage.setItem({key, value})` | `storage` | `{used, quota}`; refused over the quota. |
| `storage.deleteItem({key})` | `storage` | `{found}`. |
| `storage.listKeys({prefix})` | `storage` | `{keys}`, sorted. |
| `sessions.listSessions()` | `sessions:read` | This node's sessions: `id`, `name`, `agent`, `project`, `state`, `status`, `nodeId`. |
| `sessions.getSession({id})` | `sessions:read` | `{found, session}`. |
| `sessions.readTranscript({sessionId, before, limit})` | `sessions:read` | A page of a session's conversation, as its agent logged it: `{supported, messages, before, truncated}`, the latest messages, or those before the cursor `before` a page gave; see [Transcripts and events](#transcripts-and-events). |
| `sessions.subscribe({events})` | `sessions:read` | `{events}`: has the plugin told of events of those kinds, by its default export's `on`. |
| `sessions.unsubscribe({events})` | `sessions:read` | `{events}` left; none named stops them all. |
| `http.fetch({url, method, headers, body})` | `net:<host>` | `{status, headers, body}`; see [Permissions](#permissions). |
| `commands.runCommand({id, arg})` | none | `mako.openUrl` with an http(s) link opens it in the browser, only in answer to the user's tap or command. |
| `messages.sendMessage({sessionId, text})` | `sessions:write` | `{id}`: leaves a message for a session's agent, headed as the plugin's; see [Agents](#agents). |
| `messages.startAgent({agent, profile, project, root, prompt, name})` | `sessions:write` | `{requestId, asked}`: asks to start an agent; its outcome comes to `on.agentStarted` or `on.agentRefused`. |
| `messages.stopAgent({sessionId})` | `sessions:write` | Stops an agent the plugin started. |
| `files.listProjects()` | `fs:project` | `{projects}`: the projects this node knows, each `{id, name, roots}`, its folders on this node. See [Project files](#project-files). |
| `files.readFile({project, root, path, offset, length})` | `fs:project` | `{text, data, size, eof, binary}`: a file of a project's folder, or a piece of it, 1 MiB at most. |
| `files.listDir({project, root, path})` | `fs:project` | `{entries, truncated}`: a folder's entries, `{name, dir, link, size}`, 2 000 at most. |
| `files.writeFile({project, root, path, text, data, makeDirs})` | `fs:project:write` | `{size}`: writes a file whole, atomically. |
| `secrets.getSecret({name})` | `secrets` | `{found, value}`: a secret the plugin kept. See [Secrets](#secrets). |
| `secrets.setSecret({name, value})` | `secrets` | Keeps it, in place of one of its name. |
| `secrets.deleteSecret({name})` | `secrets` | `{found}`. |
| `secrets.listSecrets()` | `secrets` | `{names}`, sorted; never the values. |
| `layout.patchLayout({ops, document, ifRev})` | `layout:write` | `{requestId, asked}`: changes a window layout document as `patch_layout` does, as the user allows; its outcome comes to `on.layoutChanged` or `on.layoutRefused`. See [The layout](#the-layout). |
| `exec.runProcess` | `exec` | Not available: no manifest may ask for `exec` in this version. |

### Transcripts and events

`sessions.readTranscript` reads a running session of this node's: never
another node's, which the node lists but which is not its to read. A
page holds the latest `limit` messages (20 if 0, at most 50), oldest
first, each `{role, text, tool, detail, result, kind, error, pending}`;
`before` is the cursor of the page before it, `""` once the
conversation's start is reached. A cursor names the end of the log the
first page read up to, so that paging on while the agent writes neither
skips nor repeats a message. A message's text is cut at 8 KiB, a call's
input at 1 KiB and its result at 2 KiB, a page at 256 KiB, and
`truncated` says so; 30 pages a minute.

`sessions.subscribe({events})` has the plugin told of what happens to
this node's sessions, a kind at a time:

| Kind | When |
|---|---|
| `sessionStarted` | A session starts, or wakes from sleep. |
| `sessionEnded` | Its program exits, or it is closed. |
| `sessionSlept` | Its program was stopped to rest, to start again on its conversation. |
| `sessionStatus` | Its agent's status changes (`idle`, `working`, `waiting`, `done`); `previousStatus` is the one before. |
| `agentMessage` | A message is left for one of its agents (`send_message`, the window, mako's notes): `message`, its text cut at 4 KiB. |

The Host API has no streams, so an event is the host calling the plugin:
the handler of the event's kind in the default export's `on`, given a
`SessionEvent` (`{kind, session, previousStatus, message}`), as a
block's `on` is called for a node's message. Blocks are drawn again after
it, and a handler may change a block's state (`mako.blocks.state`):

```js
export default {
  activate(m) {
    mako = m;
    mako.sessions.subscribe({ events: ["sessionStatus"] });
  },
  on: {
    sessionStatus(e) {
      if (e.session.status === "waiting") mako.blocks.state("ci").waiting = e.session.name;
    },
  },
};
```

What a plugin subscribed to is kept on this node, in its data folder,
until it unsubscribes or is removed; a plugin not running is started for
an event, as for a block shown. A plugin is told at most 10 events a
second, the rest dropped (the log says so once a minute), and none while
it lacks `sessions:read`, or is disabled, refused or blocked. A handler
that fails counts toward the failures that stop it. A WIT component,
which has no prelude, will take the same events as an export; the
subscription and the event's record are in the schema already.

## Permissions

A plugin may draw its blocks, read its own settings and write to the log
from the start. Anything else is a permission its manifest names, which
the user allows, all of them at once, before the plugin runs at all:

| Permission | Lets it |
|---|---|
| `storage` | Keep keys and values on this node, 1 MiB in all. |
| `sessions:read` | List this node's sessions and how their agents stand, read their conversations, and hear of what happens to them. |
| `net:<host>` | Make HTTP requests to that host: by https, on the default port, to exactly that name (`net:api.github.com` is not `uploads.github.com`). No IP address, no `localhost`, no wildcard. |
| `secrets` | Keep secrets of its own in the system's keychain; see [Secrets](#secrets). |
| `fs:project` | Read the files of the projects this node knows; see [Project files](#project-files). |
| `fs:project:write` | Write them too: asked for on its own, beside `fs:project`. A plugin that writes a project's files can change what its builds and scripts run. |
| `sessions:write` | Send this node's agents messages, start agents (each first one as the user allows) and stop those it started; see [Agents](#agents). |
| `layout:write` | Change the window's layout, as the user allows each change or allowed this plugin such changes; see [The layout](#the-layout). |

**Asking.** A plugin that asks for what the user has not allowed waits:
the window shows a bar over itself, which no layout can cover, and the
block's place says what it asks for, both drawn by the window, never by
the plugin. **Allow** grants them all; **Deny** refuses them and the plugin
stays off; **Later** puts the bar away in that window. Settings ▸ Plugins
answers too, and revokes. From a terminal: `mako plugins`, `mako plugins
grant <id>`, `mako plugins deny <id>`. What the user allowed is kept on
this node alone (`plugin-data/grants.json`). A newer version asking for
more asks again, for what is new only; one refused stays refused until it
asks for something else.

<a id="secrets"></a>**Secrets.** A secret setting (`type = "secret"`), which the user
sets, and what a plugin keeps through `secrets`, are kept in the
system's keychain: the macOS Keychain, the Secret Service of a Linux
desktop (GNOME Keyring, KWallet) over D-Bus, or Windows' Credential
Manager, under a service of the plugin's own (`mako plugin <id>
(<node>)`), on this node alone. They are never synced to another node,
never written to the log, never sent to a front-end (Settings shows a
secret setting as set or not set, never its value), and they are removed
from the keychain with the plugin. A plugin reads its own and no
other's. A node whose system has no keychain (a Linux server without a
desktop session, say) keeps them in `plugin-data/<id>/secrets.json`,
readable by the user alone, and says so in Settings ▸ Plugins and under
`mako plugins`; `MAKO_KEYCHAIN=file` makes a node do so even with one.
Secrets kept in a file, and secret settings an older mako kept in
`settings.json`, move into the keychain once there is one. The names the
keychain holds of each plugin are listed, without their values, in
`plugin-data/<id>/secrets-index.json`, which is how a removal finds them.
On macOS, a mako built anew is asked about once by the system before it
reads a secret again, as any program whose signature changed is.

<a id="agents"></a>**Agents.** `messages.sendMessage` leaves a message for the agent of a
session of this node's that takes messages (one started with mako's MCP
server), through the mailbox `send_message` uses: it is typed into the
agent's terminal once it waits at its prompt, headed *Mako message from
the plugin <id>: code the user installed, not the user, nor Mako*, so
that the agent cannot take it for the user's words or mako's; 32 KiB at
most, 30 a minute. `messages.startAgent` starts an agent as
`spawn_agent` does, with the prompt headed as the plugin's task, in a
folder of a project this node knows (as [Project files](#project-files)
names one), the plugin saved as its parent: `messages.stopAgent` stops
those alone. Starting an agent runs a program and may cost money, so the
permission is not enough: the window asks the user, in a bar no layout
covers, **Allow once**, **Always allow this plugin** or **Deny** (or
`mako plugins allow-agent <id> once|always`, `deny-agent <id>`), on this
node alone; Settings ▸ Plugins says when a plugin may start agents
without asking, and **Ask again** takes that back (`mako plugins
forget-agents <id>`), as does revoking its permissions. The call returns
at once, `{requestId, asked}`; the start, once allowed, may take a
minute, and its outcome comes as an event without a subscription:
`on.agentStarted(event)` with the session, or `on.agentRefused(event)`
with `error` (refused, unanswered for 10 minutes, the plugin disabled
or refused meanwhile, or the agent not starting). One ask waits at a
time; at most 3 of a plugin's agents run at once, and it may try 10
starts an hour, asks included.

<a id="the-layout"></a>**The layout.** `layout.patchLayout` takes `ops`, the JSON of
`patch_layout`'s operations ([agent-mcp.md](agent-mcp.md#the-windows-layout)),
on the document named (`default` for `""`), `ifRev` the revision they were
made against. It goes the way an agent's change does: checked at once
against the document as it stands, then staged on this node, the window
showing it, and the user asked in the bar over the window, *The plugin
<id> asks to change the window layout*: **Allow**, **Allow in this
space** (for that document), **Always allow** or **Deny**. What the user
allows is the plugin's alone, apart from agents and other plugins, kept
on this node, and forgotten when the plugin is removed or its
permissions revoked. Only then is the change written into the workspace
the user's nodes share. A plugin changes the root alone, never
`variants`, the phone's compact root. The call returns at once; the
outcome comes as an event without a subscription: `on.layoutChanged`
with `document` and `revision`, or `on.layoutRefused` with `error`
(denied, no window open to show it, another change waiting, the document
changed meanwhile). One change of a plugin's waits at a time, 10 a
minute.

<a id="project-files"></a>**Project files.** A plugin does not read "the current project", which
would be whichever the window in front shows, another on the phone, and
none with no window open. It names a project, by the ID and one of the
folders `files.listProjects` gives: the projects the user put together,
as far as their folders are on this node. A path is relative to that
folder, `/`-separated, without `..`, a leading `/`, a backslash, a NUL or
a colon, 1 024 bytes at most. Nothing outside the folder is reached: each
call opens it as a root (Go's `os.Root`) that refuses a path or a
symbolic link leading out of it, and an absolute link. Only regular files
are read or written: a FIFO, a device or a folder is refused, without
waiting on it. A read gives the bytes (`data`, base64 in JSON) and, when
they are UTF-8, `text`; `offset` and `length` read a piece of a larger
file. A write replaces a file whole, written beside it and renamed over
it, keeping its mode (new files are 0644); it writes over a regular file
alone, never a link or a folder, never under a folder named `.git` (whose
hooks git runs), and makes the folders on the way only with `makeDirs`.
A request is 1 MiB at most, so a file written is a little less, and
`data` in base64 three quarters of that; 120 reads and listings a minute
and 30 writes.

**The network.** The daemon makes the requests: a redirect is followed only
within the URL's scheme, host and port; a connection to an address that is
not public (loopback, private, link-local, ...) is refused, so a granted
name cannot be pointed at the user's machine; headers that steer the
connection (`Host`, `Connection`, `Proxy-*`, ...) may not be set; each
request is logged with the plugin's name, host, path and status.

## Limits

| | |
|---|---|
| Memory | 32 MiB of JavaScript heap; an out-of-memory error that reaches the daemon stops the plugin |
| Time | 2 s of JavaScript a call (`activate`, `load`, a message, a command, a render); time spent waiting on the Host API does not count, but a call takes 30 s at most |
| Nesting | recursion deep enough to run out of stack stops the plugin |
| Failures | 3 calls failing in a row, or 3 renders, stop the plugin |
| Idle | with no block shown and nothing run for 5 minutes, the plugin is stopped, its blocks' state kept (256 KiB each) for when it starts again |
| Storage | 1 MiB; a key 256 bytes, a value 256 KiB, 1 024 keys |
| Secrets | 32 kept through `secrets`, a value 2 048 bytes (a secret setting's too), names of letters, digits and `._-`, at most 64; 60 calls of `secrets` a minute |
| HTTP | 60 requests a minute; a body sent 256 KiB, received 1 MiB; 10 s |
| Events | 20 a second from the window; more are dropped |
| Updates | a block is sent to the window at most every 100 ms |
| Code | 64 modules, 2 MiB, 8 folders deep |

A plugin stopped for going over a limit stays stopped, the reason in its
blocks' places, until its folder changes or the user starts it again
(**Restart** in the block, **Enable** in Settings).

## Developing one

Put the folder in `plugins/` and place a block with a layout document,
say by `layout.json` beside `gui.json` and *View: Reload window layout*:

```json
{"version":1,"root":{"id":"window","row":[
  {"id":"sidebar","block":"mako.sidebar","size":272,"collapsible":true},
  {"id":"workspace","column":[{"id":"header","block":"mako.header"},{"panes":"main","grow":1}],"grow":1},
  {"id":"details","stack":["plugin:ci-status/ci","mako.files","mako.git"],"size":380,"collapsible":true}]}}
```

The daemon looks at the folder every second. A change is read, checked
(the manifest, every module compiled) and started in place of the version
running, with the blocks' state carried over. A version that does not
read, compile or start leaves the last that did running, its error over
the block, until a newer one does. `makod -v` logs each load, start, stop
and request.

Plugins are a node's own: a block of a plugin another node has, in a
document shared between nodes, shows as missing where the plugin is not
installed, with *Install on this node* when a registry of the node lists
it.

## Installing from a registry

A registry is a git repository anyone keeps, listing plugins' signed
packages. Nobody reviews what a registry lists, the one mako comes with
included, so the node keeps itself safe:

- **What you see.** Settings ▸ Plugins searches every registry the node
  reads. Each plugin found says which registry lists it, the name its
  publisher gives itself (which nobody checked), the fingerprint of the
  key that signs it, and what it asks for. Nothing in the store reads as
  "verified": nobody verified it. `mako plugins search`, `install`,
  `update`, `remove` and `registries` do the same from a terminal.
- **What is installed.** The daemon downloads the package the registry's
  index lists, checks its SHA-256 and size against the index, its
  signature against the publisher key the index lists, and its manifest
  (the ID and version listed, read strictly: no `exec`; the permissions
  it asks for the set the index lists, which is what the store showed),
  and only then puts its files in `plugins/<id>/`. It runs once you allow
  what it asks for, as any plugin.
- **The publisher's key is pinned** at the first install, with the
  registry it came from, on this node (`plugin-data/installs.json`). A
  version signed by another key is never taken as an update, from that
  registry or any other. Removing the plugin forgets the pin, what it was
  allowed and what it kept: installing another publisher's plugin of that
  ID is then a first install, which starts with nothing allowed. The node
  remembers the key it last installed each ID by (the last 256 IDs, in
  `plugin-data/publishers.json`), and a first install by another key says
  so, in the store and where the plugin asks to be allowed: *published by
  a different key than the one you had before*, with both fingerprints.
  It is a note: nothing is refused for it.
- **The same ID in several registries** is several listings. An install
  names the registry when more than one lists the ID, and the plugin then
  follows that registry, and its key, for updates; another registry's
  listing is never taken in its place because of the order of the
  registries.
- **Updates go forward.** An update must be a newer version; to go back,
  remove the plugin and install it again. An update asking for more than
  the version it replaces shows what it adds, and the plugin waits for
  you to allow it; until then neither version runs.
- **Blocklists.** Every registry's index carries a blocklist, by ID, ID
  and version, or publisher key. The node merges the lists of all its
  registries and applies them to every plugin it has, whichever registry
  it came from, and to a folder in development by its ID: a plugin
  blocked stops at once, says why and by which registry, and can be
  neither enabled nor updated while it is; none of its code runs from
  then on, whatever asks it to (a block shown, an event, a command, a
  setting, its folder read again). Removing a registry lifts its list.
  The lists are read at every install, as the store is searched, and
  every 6 hours while a plugin is installed; out of reach, a registry's
  last list stands.
- **Each node installs for itself.** A layout document placing a plugin's
  block on a node that lacks the plugin shows, in its place, *Install on
  this node* for each registry of the node that lists it.
- **A folder in development is yours.** An install never replaces a
  folder written by hand, and `remove` does not delete one. Whether
  `plugins/<id>/` is still the one installed is read from the folder: its
  `mako-signature.json` and its contents' digest must be those pinned
  (names starting with a dot, which no package holds, aside). A folder
  written by hand in its place, or edited in place, is a folder in
  development: its install is forgotten, with what the plugin installed
  was allowed and kept, which the folder written in its place is not
  given.
- **An install cut short is settled.** An install pins the version it
  puts in place as pending before it swaps the folder, and as installed
  after. A daemon starting again, or another install, settles a pin to
  the folder found, by its digest, puts back a folder moved aside for a
  replacement that never came, and removes what an install or a removal
  staged beside the plugins (`.install-*`, `.<id>.old-*`,
  `.<id>.removed-*`).

A refusal comes with a code (protocol.md's [Errors](protocol.md#errors)),
which the window says in its own language, Korean among them; `mako
plugins` prints the daemon's English.

The node starts with one registry, the project's own, which you may
remove (Settings ▸ Plugins ▸ Registries, or `mako plugins registries
remove <url>`). A node reads no registry until you open the store or
install from one.

## Publishing

**A key.** `mako plugins keygen ~/keys/mako-publisher.key` makes an
ed25519 key: its private half in that file, readable by you alone,
which never leaves your machine; it prints the public key
(`ed25519:` and 32 bytes in unpadded base64url) and its fingerprint.
Every version of a plugin must be signed by the same key: a node that
installed it takes no other.

**A package.** `mako plugins pack <folder> -key <key-file>` packs and
signs the folder as `<id>-<version>.makoplugin`; `mako plugins verify
<package>` checks one and says what it is; `mako plugins sign <package>
-key <key-file>` signs a package again. The same folder always packs to
the same bytes.

A `.makoplugin` is a zip archive:

- Regular files only, stored or deflated, nothing encrypted: the folder's
  files by their paths there, leaving out names starting with a dot and
  `node_modules`, and the signature, `mako-signature.json`, at the root.
- A path is 1 to 8 parts joined by `/`, each of letters, digits and
  `._-`, not starting with `.` or `-`; 255 bytes at most. No two paths
  differ only in case.
- At most 256 files, 2 MiB each and 8 MiB in all once read, the archive
  8 MiB.
- **The contents' digest** is the SHA-256 of `mako-plugin-contents/1\n`
  followed, for each file but the signature, in the byte order of their
  paths, by the path, `\n`, its size in decimal, `\n`, and its bytes;
  written `sha256:<hex>`.
- **The signature** file is JSON:

  ```json
  {"format": "mako-plugin-signature/1", "id": "ci-status", "version": "0.1.0",
   "key": "ed25519:…", "contents": "sha256:…", "signature": "…"}
  ```

  `signature` is the ed25519 signature, in unpadded base64url, of the
  statement `mako-plugin-signature/1\nid <id>\nversion
  <version>\ncontents sha256:<hex>\n`.
- Packing writes the files in the order of their paths, stored, dated
  1980-01-01, the signature last.

A node refuses a package that breaks any of these, and one whose
signature does not hold for its contents, or whose manifest is not the
signed ID and version. The package's own SHA-256, which the index lists,
covers every byte of it.

**A registry** is a git repository with `mako-registry.json` at its root,
on its default branch. A node reads that one file, through the forge's
raw files, by https: `https://github.com/<owner>/<repo>` is read at
`https://raw.githubusercontent.com/<owner>/<repo>/HEAD/mako-registry.json`,
`https://gitlab.com/<group>/<repo>` at `…/-/raw/HEAD/mako-registry.json`,
and any other https URL ending in `.json` is taken as the index itself.
A URL with a user, a port, an IP address, a query, or any scheme but
https is refused, as are paths on the machine.

```json
{
  "format": "mako-registry/1",
  "name": "ironpark's plugins",
  "plugins": [
    {
      "id": "ci-status",
      "name": "CI status",
      "description": "The latest GitHub Actions runs of a repository",
      "homepage": "https://github.com/ironpark/mako",
      "publisher": "ironpark",
      "key": "ed25519:…",
      "versions": [
        {"version": "0.1.0", "api": 1,
         "url": "https://github.com/…/releases/download/v0.1.0/ci-status-0.1.0.makoplugin",
         "sha256": "…", "size": 5062,
         "permissions": ["net:api.github.com", "storage", "sessions:read"]}
      ]
    }
  ],
  "blocklist": [
    {"id": "evil", "reason": "sends tokens away"},
    {"id": "ok-mostly", "version": "1.2.3", "reason": "a bad build"},
    {"key": "ed25519:…", "reason": "the key was stolen"}
  ]
}
```

The index is read strictly: a field it does not know, an ID twice, a
version, key, hash, URL (https, a host name) or permission out of its
grammar, or text with control or reordering characters is refused, and
the registry shows the error, keeping what it gave before. A key is
written in one way alone, as `keygen` writes it: base64url leaves the
last character's low bits unused, and a key written with them set,
though it reads as the same bytes, is refused in an index, a blocklist,
a signature and `verify -key`. Text a registry, a package or a server
writes reaches a terminal or the window with its control and reordering
characters escaped (`\x1b`), and an error quotes no server's status
text.

`mako plugins index <checkout> <package> -url <download URL>` adds a
package's version to a checkout's index (making one if there is none),
writing it sorted and indented so that the change is small to review,
with the permissions the package's manifest asks for, which a node
checks the package against; it refuses a key other than the one the
index lists for the ID, and a version listed already. Push the change
and the package; the URL may redirect, to https, 3 times at most.

## For mako's developers

| | |
|---|---|
| `proto/mako/plugin/v1` | The Host API's schema: services, each method with its permission (`options.proto`). The only definition. |
| `internal/plugin/apigen` | The generator, run by buf as `protoc-gen-makoplugin` (`make proto`). It takes only what WIT can say: see its package comment. |
| `internal/plugin/hostapi` | Generated: the messages, the dispatcher that checks a permission before reading a call (`dispatch_gen.go`), `mako.js`, `mako.d.ts`, `mako.wit`, and the schema kept (`hostapi.binpb`), from which a test generates everything again and fails on a difference. |
| `internal/plugin` | Manifests, the runtime (qngg), limits, the Host API's handlers, trees and the folder's reading. |
| `internal/daemon/plugins.go` | The requests of [protocol.md](protocol.md#plugins). |
| `internal/gui/plugin*.go` | The blocks' frames and trees, the bar asking, Settings ▸ Plugins, the store (`pluginstore*.go`) and the placeholder offering to install (`pluginmissing.go`). |
| `internal/plugin/pack` | The `.makoplugin` package: packing, signing, opening, keys. |
| `internal/plugin/registry` | A registry's index, its URL, and the fetcher of indexes and packages. |
| `internal/plugin/store` | Registries, installs, pins and blocklists on a node (`plugin.Installs`). |
| `internal/daemon/pluginstore.go` | The store's requests of [protocol.md](protocol.md#the-plugin-store). |

To add a method: add it to the schema with its permission, run `make
proto`, and implement it on the handler (`internal/plugin/host.go`); the
build fails until you do. The WIT is checked by a small parser in the
tests, not by a WIT toolchain: none is to be had from Go without cgo. It
was checked once with `wasm-tools` 1.263 (`component wit`, and a
component of a dummy module of the `plugin` world, validated); the parser
refuses what that run found the toolchain refuses: a keyword unescaped
(`map` among them) and a function named as a type its interface uses.
`MAKO_PLUGIN_NET_TEST=host=http://127.0.0.1:port` sends a granted host's
requests to a test's server, for end-to-end runs; the daemon logs that it
does. `MAKO_PLUGIN_REGISTRY_TEST=1` lets registries and packages be on
loopback by http, and leaves the default registry out, so that a test
reaches no registry on the internet. Both are read only by a build with
the `makotest` tag (`internal/testhooks`); any other build ignores them.
The daemon's own HTTP for plugins and registries reaches public addresses
alone: none that IANA's special-purpose registries do not mark global, and
no IPv6 form of an IPv4 address (mapped, compatible, SIIT, NAT64, 6to4)
but for a public one (`internal/plugin/publicaddr.go`).

## Publishing the example to the default registry

For the maintainer: the steps that put
[examples/plugins/ci-status](../examples/plugins/ci-status) in the
registry every node starts with, `https://github.com/ironpark/mako-plugins`,
as a release asset its index lists. Run them in bash from anywhere, with
`mako` (a build of this repository) and [`gh`](https://cli.github.com)
signed in to an account that may push to the registry's repository. Step
1 runs once ever: every later version of the plugin must be signed by the
same key, so keep the key file, and a copy of it somewhere safe, and
never in a repository. To publish a later version, raise `version` in
the plugin's `plugin.toml` and run steps 2 to 5 again.

```bash
# Where things are; change the first three to yours.
MAKO_SRC=~/src/mako                         # a checkout of github.com/ironpark/mako
REGISTRY=~/src/mako-plugins                 # where the registry's repository is checked out
KEY=~/.config/mako-publisher/ironpark.key   # the publisher's private key
REMOTE=https://github.com/ironpark/mako-plugins
REPO=ironpark/mako-plugins
PLUGIN="$MAKO_SRC/examples/plugins/ci-status"
VERSION=$(sed -n 's/^version = "\(.*\)"$/\1/p' "$PLUGIN/plugin.toml")
TAG=ci-status-v$VERSION
PKG=$(mktemp -d)/ci-status-$VERSION.makoplugin

# 1. Once: the publisher key. It prints the public key and its fingerprint.
mkdir -p "$(dirname "$KEY")" && chmod 700 "$(dirname "$KEY")"
[ -f "$KEY" ] || mako plugins keygen "$KEY"

# 2. Pack and sign the plugin, and check the package.
mako plugins pack "$PLUGIN" -key "$KEY" -o "$PKG"
mako plugins verify "$PKG"

# 3. Attach the package to a release of the registry's repository.
gh release create "$TAG" "$PKG" --repo "$REPO" --title "ci-status $VERSION" --notes "CI status $VERSION, signed by the key $(mako plugins verify "$PKG" | awk '$1 == "fingerprint" {print $2}')"

# 4. List the release's package in the index, and push it.
[ -d "$REGISTRY/.git" ] || git clone "$REMOTE" "$REGISTRY"
git -C "$REGISTRY" pull --ff-only
mako plugins index "$REGISTRY" "$PKG" \
  -url "$REMOTE/releases/download/$TAG/ci-status-$VERSION.makoplugin" \
  -name "CI status" -description "The latest GitHub Actions runs of a repository" \
  -homepage https://github.com/ironpark/mako -registry-name "mako plugins"
git -C "$REGISTRY" add mako-registry.json
git -C "$REGISTRY" commit -m "List ci-status $VERSION"
git -C "$REGISTRY" push

# 5. From a node: the default registry lists it, as the store will show it.
mako plugins registries refresh
mako plugins search ci-status
```

`mako plugins index` refuses a version listed already, and a package
signed by another key than the one the index lists for the plugin: if
step 3 or 4 failed halfway, delete the release (`gh release delete
"$TAG" --repo "$REPO"`) or reset the checkout, and run from step 2
again. A node reads the index from the repository's default branch
(`raw.githubusercontent.com/ironpark/mako-plugins/HEAD/mako-registry.json`)
and downloads the package from the release, following GitHub's redirect.
