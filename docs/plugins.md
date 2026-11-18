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
  (the ID and version listed, read strictly: no `exec`), and only then
  puts its files in `plugins/<id>/`. It runs once you allow what it asks
  for, as any plugin.
- **The publisher's key is pinned** at the first install, with the
  registry it came from, on this node (`plugin-data/installs.json`). A
  version signed by another key is never taken as an update, from that
  registry or any other. Removing the plugin forgets the pin, what it was
  allowed and what it kept: installing another publisher's plugin of that
  ID is then a first install, which starts with nothing allowed.
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
  blocked stops, says why and by which registry, and can be neither
  enabled nor updated while it is. Removing a registry lifts its list.
  The lists are read at every install and every 6 hours while a plugin
  is installed; out of reach, a registry's last list stands.
- **Each node installs for itself.** A layout document placing a plugin's
  block on a node that lacks the plugin shows, in its place, *Install on
  this node* for each registry of the node that lists it.
- **A folder in development is yours.** An install never replaces a
  folder written by hand, and `remove` does not delete one.

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
the registry shows the error, keeping what it gave before. `mako plugins
index <checkout> <package> -url <download URL>` adds a package's version
to a checkout's index (making one if there is none), writing it sorted
and indented so that the change is small to review; it refuses a key
other than the one the index lists for the ID, and a version listed
already. Push the change and the package; the URL may redirect, to https,
3 times at most.

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
tests, not by a WIT toolchain: none is to be had from Go without cgo.
`MAKO_PLUGIN_NET_TEST=host=http://127.0.0.1:port` sends a granted host's
requests to a test's server, for end-to-end runs; the daemon logs that it
does. `MAKO_PLUGIN_REGISTRY_TEST=1` lets registries and packages be on
loopback by http, and leaves the default registry out, so that a test
reaches no registry on the internet.
