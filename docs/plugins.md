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
- [For mako's developers](#for-makos-developers)

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
development (see [Developing one](#developing-one)). Signed packages, a
registry and installing from it come later.

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
| `contributes.settings` | At most 32, by key (lowercase letters, digits, `_`): `type` (`string`, `secret`, `boolean`, `number`, `select`), `label`, `description`, `options` (a select's, and only a select's), `default` (of the type; none for a secret). |

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

Options (`opts`, or the node's methods of the same names): `key`, `tone`,
`weight`, `size`, `grow`, `gap`, `icon`, `disabled`, `onTap`, `onChange`,
`onSubmit`, `placeholder`.

**Colours are tokens**: `tone` is `muted`, `faint`, `accent`, `success`,
`warning` or `danger`, which the theme in force gives a colour; a node has
no other colour. A tree is refused, and the block shows why, for a kind or
field not in the vocabulary, a raw colour, more than 2 000 nodes or 24
levels, two siblings of one key, or text with control or reordering
characters.

**Keys.** Give a list's rows a `key` (a string or number, without `/` or
`#`): a node's id is its path by keys, so a row that moves is moved, not
redrawn, and a message's node is the one the user saw.

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
| `http.fetch({url, method, headers, body})` | `net:<host>` | `{status, headers, body}`; see [Permissions](#permissions). |
| `commands.runCommand({id, arg})` | none | `mako.openUrl` with an http(s) link opens it in the browser, only in answer to the user's tap or command. |
| `messages.sendMessage` | `sessions:write` | Not available yet: it fails. |
| `files.readFile` | `fs:project` | Not available yet. |
| `secrets.getSecret` | `secrets` | Not available yet. |
| `layout.patchLayout` | `layout:write` | Not available yet. |
| `exec.runProcess` | `exec` | Not available: no manifest may ask for `exec` in this version. |

## Permissions

A plugin may draw its blocks, read its own settings and write to the log
from the start. Anything else is a permission its manifest names, which
the user allows, all of them at once, before the plugin runs at all:

| Permission | Lets it |
|---|---|
| `storage` | Keep keys and values on this node, 1 MiB in all. |
| `sessions:read` | List this node's sessions and how their agents stand. |
| `net:<host>` | Make HTTP requests to that host: by https, on the default port, to exactly that name (`net:api.github.com` is not `uploads.github.com`). No IP address, no `localhost`, no wildcard. |
| `sessions:write`, `fs:project`, `layout:write`, `secrets` | Declared for what comes; their methods are not available yet. |

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
installed.

## For mako's developers

| | |
|---|---|
| `proto/mako/plugin/v1` | The Host API's schema: services, each method with its permission (`options.proto`). The only definition. |
| `internal/plugin/apigen` | The generator, run by buf as `protoc-gen-makoplugin` (`make proto`). It takes only what WIT can say: see its package comment. |
| `internal/plugin/hostapi` | Generated: the messages, the dispatcher that checks a permission before reading a call (`dispatch_gen.go`), `mako.js`, `mako.d.ts`, `mako.wit`, and the schema kept (`hostapi.binpb`), from which a test generates everything again and fails on a difference. |
| `internal/plugin` | Manifests, the runtime (qngg), limits, the Host API's handlers, trees and the folder's reading. |
| `internal/daemon/plugins.go` | The requests of [protocol.md](protocol.md#plugins). |
| `internal/gui/plugin*.go` | The blocks' frames and trees, the bar asking, Settings ▸ Plugins. |

To add a method: add it to the schema with its permission, run `make
proto`, and implement it on the handler (`internal/plugin/host.go`); the
build fails until you do. The WIT is checked by a small parser in the
tests, not by a WIT toolchain: none is to be had from Go without cgo.
`MAKO_PLUGIN_NET_TEST=host=http://127.0.0.1:port` sends a granted host's
requests to a test's server, for end-to-end runs; the daemon logs that it
does.
