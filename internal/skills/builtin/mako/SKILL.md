---
name: mako
description: >-
  Work with Mako workspaces, agent CLIs (mako-project, mako-msg, mako-plugin),
  Mako plugins (Revision 46 QuickJS-on-Wasm blocks, scoped storage/SQLite/datafs,
  Git/Forge APIs, rich UI components), and Mako prompt templates (//name with
  Svelte-style control flow, sandboxed JS, and multi-agent workflows).
---

# Mako Unified Skill

Use this skill whenever working with Mako's project management, agent CLIs,
multi-agent orchestration, plugin development, or prompt template authoring.

Load only the reference file(s) relevant to the current task:

| Area | Reference File | When to read |
|---|---|---|
| **Projects, Agent CLIs & MCP** | `cli/projects-and-agents.md` | Adding local folders or Git URLs as Mako projects (`mako-project`, `mako projects`), messaging/watching agents (`mako-msg`), calling plugin tools (`mako-plugin`), or using Mako MCP tools (`spawn_agent`, `patch_layout`, `show_ui`, `save_template`). |
| **Plugin Authoring** | `plugins/authoring.md` | Creating, updating, debugging, or packaging a Mako plugin (`plugin.toml`, permissions, scopes, `ctx`, Git/Forge Host APIs, window intents, i18n, `mako-plugin` tools). |
| **Plugin UI Components** | `plugins/ui-components.md` | Building plugin UI trees (Revision 46: `toolbar`, `menu`/`contextMenu`, `checkbox`, `dialog`, `tree`, `dag`, `graph`, `charts`, `split`, `wrap`/`grid`, validation rules & pitfalls). |
| **Prompt Template Syntax** | `templates/syntax.md` | Creating or refining `//name` prompt templates (frontmatter schema, Svelte markup, `{#if}`, `{#each}`, exhaustive `{#match}`, `{#raw}`, `save_template`). |
| **Prompt Template Scripting** | `templates/scripting.md` | Writing sandboxed JS blocks (`js`, `js run`) and multi-agent `mako.agents` / `mako.spawn` / `mako.ask` / `mako.decide` workflows inside templates. |

## Quick Commands (Available in Every Mako Agent Session)

```bash
# Projects: list, inspect default storage locations, add local folder or clone Git URL
mako-project list [--json]
mako-project dirs [--json]
mako-project add <path-or-git-url> [--dir <base-dir>] [--folder <name>] [--name <name>] [--space <space>] [--json]

# Plugin tools: discover and invoke contributed tools scoped to your session
mako-plugin list [--json] [plugin-id]
mako-plugin help <plugin-id> [tool-id]
mako-plugin call <plugin-id> <tool-id> [--param value | --json '<json>']

# Inter-agent messaging and event-driven waiting
mako-msg list
mako-msg profiles [--node <id>]
mako-msg send <session-id> <message...>
mako-msg wait [--all-children | --session <ids>] [--from <id>] [--timeout <sec>]
```

## Key Conventions

- **Adding projects as an agent**: Run `mako-project dirs` first if cloning a Git URL and you want to check which default storage directory will be used (the first directory is used automatically unless `--dir` is passed). Adding a project updates the daemon's workspace CRDT immediately so the GUI sidebar and paired nodes reflect it live.
- **No polling loops**: Never poll `read_messages` in a loop; rely on native push delivery or `watch_agent` / `mako-msg wait`.
- **Plugin & template validation**: Always follow the strict schemas in `plugins/` and `templates/` before saving files or calling `save_template`.
<!-- mako-managed-skill -->
