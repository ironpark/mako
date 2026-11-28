# mako usage reference

[English README](../README.md) · [한국어 README](../README.ko.md)

## Command line

```sh
mako nodes                    # the machines on the mesh
mako agents                   # the agents and profiles this machine can start
mako run claude               # start Claude in the current folder, and attach
mako -worktree run claude     # ...in a new isolated git worktree
mako -branch feat/x run claude # ...in a new git worktree on branch feat/x
mako run claude:opus          # ...with its "opus" profile
mako -node <id> run codex     # ...on another machine
mako ls                       # every session on the mesh
mako attach <session-id>      # pick a session up again
mako rename <session-id> api  # name it
mako kill <session-id>        # end it
mako addrs                    # this daemon's addresses, to give other nodes
mako devices                  # devices attached to the mesh's machines
eval "$(mako borrow pixel)"   # use another machine's Android device with adb here
mako -device pixel run claude # ...or give it to an agent
mako access                   # this machine's key, requests, and the keys let in
mako request <node> [note]    # ask another machine to let this one in
mako allow <node>             # let in a machine that asked
```

## The desktop app

`mako-gui` is a window onto the mesh:

- **Sidebar.** Projects and their sessions, with each session's host,
  branch and last activity. A repository cloned on several machines, in
  folders of any name, is one project, by the repository its origin
  remote is of. Sessions can be pinned, searched and dragged
  into panes. Dropping a session onto another project or host starts the
  same agent there.
- **Tabs and panes.** Open sessions as tabs, and drag them to split, move
  or reorder panes. Each pane's tab bar has a launcher for every agent the
  node can start.
- **Terminal.** Colours, wide characters, the IME, the mouse, links that
  open with ⌘-click, image previews, pasted screenshots, and files dropped
  in as paths, even to a session on another machine. A link to a server
  on another machine's `localhost` opens here through a tunnel, on the same
  port when it is free; the sidebar lists the ports forwarded, and any
  port of a node can be forwarded by hand.
- **Chat.** The conversation as the agent logged it, and a composer that
  types into the live session (Enter sends; Shift-Enter starts a new line;
  ↑ recalls what you sent). When Claude Code suggests what to send next,
  the suggestion shows dimmed in the composer: Tab or → takes it, Alt-→
  takes its next word, and Esc hides it. When an agent asks a question,
  its live input screen opens inside Chat so you can select options or
  type an answer.
  Click the screen to use the agent's keyboard controls. A rail down the
  left edge has a tick for each prompt: hover one to see the prompt and
  how the reply ended, click it to go there. **Answer the
  agent** also opens it manually; **Back to message** restores the composer
  with your draft intact.
- **Files, Changes, History, Search.** Browse and edit the project's files
  on its node. Review changes as a tree with per-file diffs, then commit,
  push, pull, stash or switch branches. Look through the commit graph, and
  search the project's text with ⌘⇧F.
- **GitHub, Forgejo and Gitea.** The forge the project's origin is on,
  with the login the node already has there: GitHub through its API,
  natively, with the login `gh auth login` left (gh itself need not be
  installed), `GH_TOKEN`, or git's credentials; a Forgejo or Gitea through
  its API with the credentials git keeps for the host. List the repository's pull requests, issues and
  Actions runs; read one in full, with its checks, files and diffs, and
  its conversation; comment, review, check out, open a pull request of
  the branch, or on GitHub run a workflow again. Hand an issue, a review
  or a failed run's logs to an agent, or start a session on a pull
  request in a worktree of its own. The Git tab shows the branch's pull
  request and checks, and a desktop notification says when its runs
  finish.
- **Files on other machines.** A node other than this one has a file
  browser window of mako's own, as Finder or Explorer is for this one:
  browse its folders, open a file in a tab, rename, delete, and upload by
  dropping files on the window or download to this machine. Files dropped
  from the desktop onto a folder of the Files tab are copied into it,
  folders and all, and a file dragged within the tab moves. A file dropped
  anywhere else opens in a tab.
- **Editors.** Open a file or folder of this machine in VS Code, Cursor,
  Windsurf, Zed, Sublime Text or a JetBrains IDE, from the Files tab, or
  from a file's or a diff's tab at its line.
- **Command palette** (⌘K), **go to file** (⌘P), and English and Korean
  interfaces with light and dark themes.
- **Remote SSH hosts.** Connect to any Linux or macOS server over OpenSSH
  directly from the New Session **Environment** picker (**Add SSH host…**) or
  **Settings ▸ Nodes**. Mako reads your `~/.ssh/config` aliases (including
  `Include`, `ProxyJump`, and your SSH agent), tests connectivity and detects
  installed agents, and automatically tunnels to a remote `makod` when
  installed — or falls back to **Direct SSH** mode with zero remote
  installation required (supporting remote sessions, folder browsing, file
  editing, project search, git status/diff/history, and `localhost` port
  forwarding). You can also click **Install makod** right inside the SSH host
  configuration dialog (or run `mako ssh install <host>`) to cross-compile and
  deploy `makod` to `~/.mako/bin/makod` on the remote server.
- **Prompt templates.** Type `//` in Chat's composer to pick one of your
  [prompt templates](#prompt-templates) by name; Tab completes it, and
  Enter sends the prompt it makes from the arguments after it.
  `//template` and a description has the agent write one for you.

## The Android app

[`app`](../app) is mako for Android: a node of the mesh in your pocket. It
lists the sessions of every computer that lets the phone in, notifies you
when an agent waits for an approval, which you can allow from the
notification, or has finished a turn, and opens a session's chat, with a
composer, and its terminal. It starts agents on your computers too. The
phone joins the mesh as any machine does, with a key of its own, found on
the same Wi-Fi or linked by an address; **Ask to join** asks a computer to
let it in. See [app/README.md](../app/README.md) to build it.

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

## Devices

An Android device or emulator attached to one machine can be used from
another, by adb and by agents: the machine it is attached to lends it,
once its `config.json` shares adb devices (`"devices": {"share":
["adb"]}`), and the other borrows it, with `mako borrow` or the window's
devices menu, or for a session with `mako -device`. adb on the borrowing
machine sees that one device, as if it were attached there, and its
`adb forward` and `adb reverse` listen and reach there too. See
[docs/devices.md](devices.md) for how it works, what to know, and the
kinds of device to come.

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
        {"name": "opus", "description": "Reviews complex changes, concurrency and cancellation.", "model": "opus"},
        {"name": "work", "env": {"CLAUDE_CONFIG_DIR": "~/.claude-work"}}
      ]
    },
    "codex": {"profiles": [{"name": "auto", "args": ["--full-auto"]}]}
  }
}
```

**SSH key.** `ssh_key` left out is the first of `~/.ssh/id_ed25519.pub`,
`id_ecdsa.pub` and `id_rsa.pub` there is; set it to `""` to vouch with none.

**Agents.** Each agent has a `command`, default `args` and `env`, and named
`profiles` that add to them. `claude:work` runs the command with the
agent's flags, then the profile's, then any given after `--`. Values expand
`$NAME`, `${NAME}` and a leading `~`; write `$$` for a literal `$`. The
built-ins `claude`, `codex`, `agy`, `pi`, `omp`, `omo`, `opencode` and
`shell` work without any configuration. Other nodes see only your agents' and profiles' names, never
their flags or environment.

A profile can include a `description` of its specialties and when to use it,
edited in Settings ▸ Agents. The built-in `mako` MCP server includes these
descriptions in `list_agents`, so agents can choose a suitable agent and
profile before delegating with `spawn_agent`. Descriptions are optional;
existing profiles continue to work.

A profile's `model` starts its sessions on that model, picked in Settings ▸
Agents from those the agent offers. mako gives it as the agent's own
`--model` flag, for the agents that take one, after the agent's and the
profile's flags, so it wins over a model they name.

**MCP servers.** Sessions of the agents that take them (see
[Agents](#agents)) get the MCP servers of the Zed extensions installed on
their node (Settings ▸ Extensions), plus any you
list under `context_servers`. A profile can choose a subset of them.

They also get the built-in **mako** MCP server: agents can start another
agent in the same project, list project sessions, exchange messages through
durable inboxes, and stop agents they spawned. New sessions appear in the
app as ordinary sessions. See [Project agents over MCP](agent-mcp.md)
for the tools, message workflow and configuration.
The right sidebar's **Agents** tab monitors their status, parent and child
relationships, pending messages and recent communication without consuming
their inboxes.

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

**Worktree bootstrap (`.mako/worktree.toml`).** A Git repository can include
`.mako/worktree.toml` at its root so every new worktree (from the Launch tab,
`-worktree`, `spawn_agent`, or an isolated team) automatically links shared
directories, copies local env files, and runs setup/teardown hooks:

```toml
base_ref = "origin/main"
shared_paths = ["node_modules", ".cache"]
copy_paths = [".env", ".env.local"]
setup_cmd = "npm ci --prefer-offline"
teardown_cmd = ""
```

A path in `shared_paths` is a link to the same folder in the main
repository, so every worktree and the main repository write into it.
Mako makes and removes the worktrees of one repository one at a time, so
two `setup_cmd` runs do not change a shared folder at the same time. An
agent in a worktree can still change a shared folder while it works.

A path in `copy_paths` is copied into the worktree. Mako copies only
folders, regular files, and links that lead to files of the repository,
also through the links they lead on to. Mako refuses a link that leads
out of the repository, and leaves out a link that leads nowhere. Mako also
leaves out a `.git` in a copied folder, so that git does not use the
config or hooks of a repository inside it in the worktree.

When you remove a worktree, Mako first refuses it if it has changes.
Then Mako runs `teardown_cmd`, and takes out the links and the unchanged
copies that it made. If `teardown_cmd` fails, Mako removes the worktree
anyway and shows the failure. Mako removes only a worktree that git lists
for the repository.

Mako gives the two commands the worktree only in environment variables:
`MAKO_WORKTREE_PATH`, `MAKO_WORKTREE_REPO`, `MAKO_WORKTREE_BRANCH`, and
`MAKO_WORKTREE_BASE`, the branch that the worktree came off. Both commands
get the same base. Each command can run for two minutes. When the time
is up, Mako stops the command and, on macOS and Linux, all that it
started. A removal then continues without the command.

On Windows, the commands run with `cmd /C`, which puts in the value of
each `%NAME%` before it reads the line. So Mako does not run them for a
branch or base whose name has one of `& | < > ^ % ! " ( )`. Put quotes
around the paths in a command, as in `"%MAKO_WORKTREE_PATH%"`.

Mako reads the file from the main work tree of the repository, also
when you start in a folder of it or in another worktree. `base_ref` is
where a new worktree starts when you do not name a base or a start.

If `.mako/worktree.toml` is absent and `orca.yaml` defines
`worktree.sharedDirectories`, Mako honors those shared directories
automatically. Mako reads each file only if it is a regular file of at
most 256 KiB in the repository.

`setup_cmd` and `teardown_cmd` run with your permissions, so Mako runs
them only after you trust the file as it reads (see Repository commands
below). Until then, Mako still makes and removes the worktree, but skips
the two commands. The app, `mako run` and `spawn_agent` then say so and
tell you what to run. When the file changes, Mako skips them again until
you trust it again. Set `MAKO_NO_WORKTREE_HOOKS=1` in the daemon's
environment to never run them, trusted or not.

**Networking.** The CLI and the app talk to their daemon on a Unix socket
in the configuration folder, and daemons talk to each other on port 4243
(UDP and TCP), which `p2p.listen` can change. When a machine
is behind NAT that no hole can be punched through, run a relay on a node
with a public address: set `"p2p": {"relay": true}` there, run `mako addrs`
on it, and add the address it prints to the other nodes' `p2p.relays`.

### Prompt templates

Prompt templates are edited in Settings ▸ Templates, and kept as Markdown
files in `templates/` beside `config.json`: the window's own, which fills
a template in and sends the prompt to whichever node the session is on.
The page checks a template as Chat would read it before saving it; saving
rewrites the file, so comments in a file written by hand are not kept.

mako also comes with **built-in templates**, listed in Chat and in
Settings ▸ Templates as built in. A file of yours of the same name stands
in for one: **Copy to edit** on its page makes that file, and deleting it
brings the built-in back. There is one so far, `//template`, which has the
session's agent write a template from what you describe after it, no
quotes needed:

```
//template review a Go file for data races, with how strict as a choice
```

The agent saves it with the mako MCP server's
[`save_template`](agent-mcp.md#prompt-templates), which checks it as this
page does. Chat shows you the template, and replacing one of yours shows
what changes; it is saved among your templates, in the folder of the
window you allow it in, only once you do, and is in `//` at once. It is
offered to the agents that get the mako MCP server. What it tells the
agent of the language is the reference below, word for word.

<!-- prompttmpl:reference: the built-in //template's own, in internal/prompttmpl/builtin/maker.md; go test ./internal/prompttmpl -run TestReferenceInDocs -update writes it here -->
A template is a Markdown file named for it: `review.md` is `//review`. A
name has no spaces, slashes, backslashes or colons, and does not begin
with a dot. YAML front matter says what a template is for, which agents
it is offered to and the arguments it takes; the rest is the prompt,
written as Svelte writes markup, its values in double braces so that the
braces of JSON and code in a prompt stay text.

```markdown
---
description: Review a file
agents: [claude, codex:fast]   # by agent, or agent:profile; left out, every agent
args:
  - name: file
    type: string
    required: true
    description: The file to review
  - name: depth
    type: integer
    default: 2
  - name: tone
    type: select
    options: [kind, blunt]
    default: kind
---
Review {{file}} {{depth}} levels deep, in a {{tone}} tone.
You are {{agent}} ({{model}}) working in {{project}} on {{branch}}.
```

The front matter has `description`, `agents` and `args`, and an argument
`name`, `type`, `description`, `required`, `default`, `options` and
`rest`; a field of another name is refused. An argument's name is letters,
digits, `_` and `-`, beginning with a letter or `_`, and is not a built-in
variable's. Its `type` is `string` (one line, the default), `text` (lines
and all), `integer`, `number`, `boolean` (`true`/`false`, `yes`/`no`) or
`select` (one of its `options`; `choice` is accepted as an alias). It can be `required`, or have a
`default`; otherwise it is empty when left out. The last argument, if it
is a `string` or `text`, can take the `rest` of a call as it is typed,
spaces and quotes and all, once those before it are given: with `rest:
true` on an argument `ask` after `file`, `//explain main.go why it leaks`
gives `ask` the text `why it leaks`.

The built-in variables are `agent`, `profile`, `model` (as the agent's
latest reply reports it), `project`, `branch`, `repo` and `date`.

`{{name}}` is an argument's value, as the call gave it, or a built-in
variable's; `{{expression}}` is a JavaScript expression's, which reads the
arguments by name (numbers and booleans as their types say) or as `args`,
and the built-in variables by name or as `vars`. Text is put in as it is,
`undefined` and `null` as nothing, anything else as JSON. A name that is
none of these, nor defined before it, is refused; a `{{…}}` that holds no
JavaScript, as another template language's `{{.Field}}`, is text.

The blocks are Svelte's:

```svelte
{#if args.depth > 2}
Read every line.
{:else if args.depth === 2}
Read it.
{:else}
Skim it.
{/if}

{#each files as file, i}
  {@const name = file.split("/").pop()}
  {{i + 1}}. {{name}}
{:else}
No files.
{/each}

{#snippet rules(strict)}
- Keep functions short.
{#if strict}
- No TODOs.
{/if}
{/snippet}
{@render rules(true)}
```

`{#each}` takes anything `Array.from` does, or a number for that many
indexes; `{@const}` names a value within the block it is in; a snippet is
a function its `{@render}` calls, known throughout the block it is in. A
line of tags alone goes with them, so blocks over lines of their own leave
no blank line; a `{` that begins no tag, as JSON's, is text.

`{#match subject}` picks text by a value as Rust's `match` does: each
`{:pattern}` begins an arm for the values its pattern matches, `{:else}`
one for the rest. The subject is a name, matched by its value as text, or
a JavaScript expression.

```markdown
{#match tone}
{:kind | "plain"}
Be encouraging.
{:blunt}
Be direct.
{/match}
Read it {#match args.depth}{:1..=2}quickly{:else}closely{/match}.
```

A pattern is a word, as the value is spelt; a `"quoted"` text; a range of
numbers (`1..=3` with its end, `1..3` without, `3..`, `..=10`); or `_`, any
value, as `else` is; `|` joins patterns. As in Rust, a block must cover
every value, or the template is not saved: each of a choice's options,
`true` and `false`, or the rest with `{:else}`. A pattern its argument
cannot take, as an option a choice does not have, is refused too.

A fenced ` ```js ` block runs its JavaScript where it is and gives way to
the value it ends on: its last expression, or what it returns when it
returns or awaits outside the functions it defines. A ` ```js run ` block
only runs, in the global scope, so what it defines with `var` or
`function` every later expression and block reads; what it defines with
`let`, `const` or `class` only it sees. One that returns or awaits runs as
a function, whose names are all its own: it shares what it sets as
`globalThis.name = …`. A later piece that reads a name a run block keeps to
itself is refused when the template is saved, saying how to share it.

````markdown
```js run
var lines = n => n === 1 ? "one line" : n + " lines";
```
Review {{file}}, about {{lines(depth * 40)}}.

```js run
globalThis.plan = await mako.ask("claude", `Plan a review of ${file} in three steps.`);
```
Follow this plan: {{plan}}
````

A script's `{{…}}` is not filled in: it reads the arguments and built-in
variables by name, or as `args` and `vars`. An argument whose name
JavaScript cannot spell as a variable's, as `my-file` or `default`, is
read as `args["my-file"]`.

Inside scripts, `{@const ...}`, `{#if ...}`, `{#each ...}`, `{#match ...}`, and
`{{...}}` expressions, a frozen `mako` host object is available for multi-agent
and decision workflows (with `await`). It works when the template is sent
to a session; Settings ▸ Templates' preview has none to call. An agent is
named as `agent`, `profile` and `target` (`"agent:profile"`), and a call
takes the target and the prompt as two strings for short:
`mako.ask("claude:fast", "…")`.

- `await mako.agents(agentOrOpts?)` — lists offered agents (`agy`, `claude`,
  `codex`, `pi`, `omp`, `omo`, `opencode`, etc.), their profiles, models, and
  effort levels (`{ nodeId, agents }`). Pass `"claude"` or
  `{ agent: "claude", refresh: true }` to inspect a specific agent's models.
- `await mako.spawn(optsOrPrompt)` — spawns a new Mako sub-agent session and
  returns `{ sessionId, agent, profile, name, project }`. Pass
  `{ agent, model, effort, yolo, name, project, prompt }` or
  `{ profile, target: "claude:reviewer", prompt }`.
- `await mako.ask(optsOrPrompt)` (alias `mako.prompt`) — runs a one-off
  non-interactive prompt without keeping a session alive. Accepts
  `{ agent, profile, target, model, effort, yolo, project, prompt, format, schema }`.
  Set `format: "json"` (or provide `schema`) to get a parsed JSON value back,
  or `"text"` (default) for a string.
- `await mako.decide(opts)` — calls the configured decision model (TypeSafe AI,
  OpenRouter, OpenAI, or Google AI Studio) with `{ state, type, options, criteria, levels, questions, provider, model }`.
  It answers `{ provider, model, answers, choice, yes, score, level, label,
  probability, confidence, probabilities }`, the last fields for a single
  question; `type` is `choice`, `noul` (yes or no) or `score`, or left out
  for one by what is given. Shorthand helpers:
  `await mako.decide.choice(state, optionsOrCriteria, extra?)` gives the
  option chosen, `await mako.decide.yes(state, instructions?, extra?)`
  `true` or `false`, and `await mako.decide.score(state, levelsOrCriteria, extra?)`
  the whole answer.

`{#raw}` … `{/raw}` keeps the text it holds as it is written: its `{{…}}`s,
tags and ` ```js ` fences are text, for a prompt that shows an agent
another template. A raw block in it pairs with its own end, so one can
show one too.

A prompt compiles to JavaScript, run on QuickJS in a sandbox of its own
([qngg](https://github.com/ironpark/qngg): no files, no network, nothing
printed) for at most five seconds and in 64 MiB, the time it waits on
`mako` aside, and five minutes in all; a prompt of text and names alone
is filled without it. JavaScript that does not parse is
refused when the template is saved; one that throws, or runs too long,
stops the call, saying where.

Call a template in Chat with its arguments in order, or by name, quoting
what has spaces: `//review "src/my file.go" tone=blunt`. The line over the
composer shows the arguments the template takes and what is wrong with
those given, and a wrong call is not sent. A template not for the session's
agent is not listed, and a `//` draft naming no template is sent as typed.
<!-- /prompttmpl:reference -->

## Security

- **Vouch with your SSH key.** Each machine vouches with its SSH key,
  `~/.ssh/id_ed25519.pub` (or `id_ecdsa`, `id_rsa`), unless Settings ▸
  Access names another or none; its private half must be in the SSH agent
  or beside it without a passphrase. Machines vouched for by one key let
  each other in, and a machine that lets the key in lets them all in. A
  machine whose `~/.ssh/authorized_keys` has the key, so that it logs in
  there by SSH, lets them in as well.

Letting a key in hands its holder the machine: a machine you let in can
start programs and read and write files on yours. Trust runs one way, so
letting a server's key in on your laptop is a separate decision from
letting the laptop's in on the server.

Between daemons, each side is held to its own key, and one whose key is not
let in gets nothing: no requests, no sessions, no relay. The CLI and the app
talk to their own daemon on a socket only your user can open, so nothing
but port 4243 faces the network, and it can face the internet.

## Repository commands

A repository can track a file that tells mako to run commands for it, such
as the `setup_cmd` of `.mako/worktree.toml`. Anyone who can commit to the
repository can write such a file, and so can an agent that works in it.
So mako runs these commands only after you read the file and trust it.
Each trust is for one file of one repository, as the file reads now. When
the file changes, mako stops running its commands until you trust it
again. Agents cannot trust a file for you.

```sh
mako repo-trust allow .mako/worktree.toml   # show the file, ask, and trust it as it reads
mako repo-trust show .mako/worktree.toml    # show the file, and if it is trusted
mako repo-trust                             # list the files you trust
mako repo-trust revoke ~/src/app            # forget every file of a repository
```

The desktop app shows the whole file in a dialog with a Trust button when
mako skips its commands. The trust is kept on the machine of the
repository, in `repo-trust.json` in its configuration folder.
