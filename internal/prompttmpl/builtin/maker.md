---
description: Have the agent write a template from your description, and save it as you allow
agents: [agy, claude, codex, pi, omo, opencode]
args:
  - name: text
    type: text
    required: true
    rest: true
    description: What the template is for, in your words
---
Write a new mako prompt template from the description below, and save it
with the `save_template` tool of the `mako` MCP server.

The description:

{{text}}

How to go about it:

- Ask nothing; decide from the description. Make one focused template
  that does what it describes, and no more.
- Name it with a few lowercase words joined by hyphens, as `review-go`:
  it is called as `//review-go`. A name is letters, digits, `.`, `_` and
  `-`, beginning with a letter or digit, at most 64 characters.
- Give it a one-line `description`, shown beside its name in Chat's list.
- Make what changes from one call to the next its arguments, few, each
  with a `description` and the type that fits: `select` (or `choice`)
  for a set of values, `integer`, `number` or `boolean` where they apply. Make
  `required` what the prompt cannot do without, and give the rest a
  sensible `default`. What the agent can find out for itself is no
  argument. When the last argument is free text, give it `rest: true`,
  so that it is called without quotes.
- Leave `agents` out unless the description says which agents it is for.
- Write the prompt as instructions to an agent, in the language the
  description is written in, using the built-in variables where they
  help, and blocks and JavaScript only where the arguments call for them.
- Call `save_template` with its `name` and the whole file as `content`,
  front matter and prompt. It checks the template as mako reads it; when
  it returns an error, fix what the error says and call it again. Set
  `replace` only when the description asks to change a template of the
  user's that has the name.
- The user sees the template and decides whether it is saved. Once it
  is, say how to call it: `//` and its name, then its arguments. If they
  decline, say so and ask what to change. With no `save_template` tool,
  show the file in your reply instead, and ask the user to add it in
  Settings ▸ Templates.

## The template language

{#raw}
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
{/raw}
