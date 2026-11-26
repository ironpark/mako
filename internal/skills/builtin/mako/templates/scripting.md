# Mako Prompt Template Scripting & Multi-Agent Host API

Use this reference when a prompt template needs computed helpers, dynamic
branching, decision-model triage, or multi-agent orchestration via sandboxed
JavaScript blocks.

## 1. Sandboxed JavaScript Blocks

- ` ```js ` prints its final expression (or `return` value) inline.
- ` ```js run ` executes for side effects / helper definitions:
  - **Synchronous**: `var` and `function` declarations are visible to later expressions/blocks (`let`/`const`/`class` are block-local).
  - **Async / `return` blocks**: run inside an async function wrapper — export values via `globalThis.myVar = await ...` so later `{{myVar}}` expressions can read them!
- Inside scripts, `{{...}}` is NOT interpolated; read arguments and built-in variables directly by name or via `args` / `vars`.
- Sandbox limits: QuickJS, no filesystem/network, 5s CPU & 64 MiB per run (excluding `await mako.*` wait), 5 min wall-clock max.

```markdown
```js run
var lines = n => n === 1 ? "one line" : n + " lines";
```
Review {{file}}, about {{lines(depth * 40)}}.

```js run
globalThis.plan = await mako.ask("claude", `Plan a review of ${file} in three steps.`);
```
Follow this plan: {{plan}}
```

## 2. Host `mako` Object (Live Sessions Only)

Available in ` ```js `, ` ```js run `, `{@const ...}`, `{#if ...}`, `{#each ...}`,
`{#match ...}`, and `{{...}}` expressions when expanding a template in a live
session (Settings ▸ Templates preview has no live daemon session to call):

- `await mako.agents(agentOrOpts?)` -> `{ nodeId, agents }` (profiles, models, effort levels). Pass `"claude"` or `{ agent: "claude", refresh: true }` to inspect a specific agent.
- `await mako.spawn({ agent, profile, target, model, effort, yolo, name, project, prompt })` -> `{ sessionId, agent, profile, name, project }`.
  Shorthand: `await mako.spawn("claude:worker", "Prompt...")`.
- `await mako.ask({ agent, profile, target, model, effort, yolo, project, prompt, format, schema })` (alias `mako.prompt`):
  - One-off non-interactive call without keeping a session alive.
  - Pass `format: "json"` or a JSON `schema` to get a parsed JS object directly, or `"text"` (default) for a string.
  - Shorthand: `await mako.ask("codex:fast", "Summarize...")`.
- `await mako.decide({ state, type, options, criteria, levels, questions, provider, model })`:
  - Helpers:
    - `await mako.decide.choice(state, optionsOrCriteria, extra?)`
    - `await mako.decide.yes(state, instructions?, extra?)`
    - `await mako.decide.score(state, levelsOrCriteria, extra?)`

## 3. Scripting Pitfalls

- **Using `const`/`let` in ` ```js run `** and referencing it later in `{{...}}` -> fails validation! Use `var` for sync blocks or `globalThis.x = ...` for `await`/`return` blocks.
- **Calling `mako.ask`/`mako.spawn` unnecessarily** -> slows down template invocation and fails in Settings preview; only use host calls for real orchestration/triage.
<!-- mako-managed-skill -->
