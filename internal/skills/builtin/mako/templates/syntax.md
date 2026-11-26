# Mako Prompt Template Syntax & Authoring Guide

Use this reference whenever creating, updating, or troubleshooting a Mako prompt
template (`//name`) saved via the `mako` MCP server's `save_template` tool
(or in `<configDir>/templates/<name>.md`). Pair with `templates/scripting.md`
for sandboxed JS blocks and multi-agent host calls.

## 1. Authoring Workflow

1. **Decide scope and arguments first**:
   - Keep each template focused on one repeatable workflow.
   - Name with 1–3 lowercase words joined by hyphens (e.g. `review-go`, `pr-summary`).
     Valid pattern: `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$` (no spaces, slashes, colons, or leading dot).
   - Write a crisp one-line `description` shown in Chat's `//` completion list.
   - Make only genuinely variable inputs arguments; omit anything the agent can inspect in the repo.
   - Prefer `select` (with clear `options` + `default`) over free-form `string` for modes/depths/styles.
   - Use `rest: true` on the **last** `string` or `text` argument so users can type trailing prose without quotes.
   - Omit `agents` unless the template relies on a specific agent or profile (`[claude, codex:fast]`).
2. **Draft a high-signal prompt body**:
   - Match the language of the user's request (Korean request -> Korean prompt instructions).
   - Structure prompts with a clear goal, concrete constraints/steps, and expected output format.
   - Use built-in variables (`{{project}}`, `{{branch}}`, `{{repo}}`, `{{agent}}`, `{{model}}`, `{{profile}}`, `{{date}}`) when context helps.
   - Prefer plain `{{arg}}`, `{#if}`, and exhaustive `{#match}` over JS blocks unless computation or `mako.*` host calls are genuinely needed.
3. **Validate & save via `save_template`**:
   - Call `save_template({ name, content, replace })` with the full file (frontmatter + body).
   - Set `replace: true` ONLY when updating an existing template or intentionally overriding a built-in (`//template`, `//plugin`).
   - If `save_template` returns a parse/compile error, fix the exact issue and call `save_template` again immediately.
   - Once saved, show the exact `//name ...` invocation syntax to the user.

## 2. Frontmatter Schema

Allowed top-level keys ONLY: `description`, `agents`, `args`. Unknown keys are rejected.

```yaml
---
description: Review a file or diff for correctness and edge cases
agents: [claude, codex:fast]   # optional; omit to allow every agent
args:
  - name: target
    type: string               # string (default), text, integer, number, boolean, select (alias: choice)
    required: true
    description: File path or symbol to review
  - name: focus
    type: select
    options: [correctness, security, performance]
    default: correctness
    description: Review lens
  - name: notes
    type: text
    rest: true                 # only valid on the last string/text arg
    description: Optional extra context
---
```

Rules:
- Argument names match `^[a-zA-Z_][a-zA-Z0-9_-]*$` and must NOT collide with built-in variables (`agent`, `profile`, `model`, `project`, `branch`, `repo`, `date`).
- Hyphenated or JS-reserved arg names (e.g. `my-file`, `default`) are read as `args["my-file"]`.

## 3. Markup & Control Flow

- **Interpolation**: `{{name}}` or `{{expression}}` (plain text stays text, `null`/`undefined` -> empty, objects/arrays -> JSON). Unrecognized names are rejected at save time.
- **Conditionals**: `{#if cond} ... {:else if cond2} ... {:else} ... {/if}`
- **Loops**: `{#each list as item, i} ... {:else} ... {/each}` (`list` can be any iterable or an integer count).
- **Local bindings**: `{@const x = expr}` (scoped to its enclosing block).
- **Snippets**: `{#snippet name(a, b)} ... {/snippet}` invoked via `{@render name(1, 2)}`.
- **Exhaustive Match (`{#match subject}`)**:
  - Patterns: bare word, `"quoted"`, numeric ranges (`1..=3`, `1..3`, `3..`, `..=10`), `|` alternation, `_` / `{:else}`.
  - **Must be exhaustive**: every `select` option or both `true`/`false` must be covered, or include `{:else}`. Impossible patterns are rejected at save time!
- **Raw blocks**: `{#raw} ... {/raw}` preserves literal `{{...}}`, Svelte tags, and ` ```js ` fences.

## 4. Common Pitfalls to Avoid

- **Non-exhaustive `{#match}`** on a `select` or `boolean` arg -> fails validation! Cover every option or add `{:else}`.
- **Putting `rest: true` on a non-last argument** -> rejected.
- **Using unknown frontmatter keys** -> rejected; stick strictly to `description`, `agents`, `args`.
<!-- mako-managed-skill -->
