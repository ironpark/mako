# Mako Projects, Agent CLIs & MCP Reference

Every agent session launched by Mako receives authenticated helper CLIs in its
`PATH` (`mako-project`, `mako-plugin`, `mako-msg`) backed by the daemon's
`agent-cli.sock` using `AGENT_MAKO_SESSION` and `MAKO_AGENT_MCP_TOKEN`.

## 1. Project Management CLI (`mako-project`)

Use `mako-project` inside any agent session to inspect registered projects,
inspect this node's configured default project storage folders, or add a local
folder / Git repository URL directly into Mako's workspace (immediately visible
in the GUI sidebar and synced across paired nodes):

```bash
# List currently registered workspace projects
mako-project list
mako-project list --json

# List this node's configured default project storage directories (Settings ▸ General)
mako-project dirs
mako-project dirs --json

# Register an existing local folder (absolute, ~/..., or relative to session dir)
mako-project add /path/to/my-repo
mako-project add ./subdir --name "My Service" --space Work

# Clone a Git URL into the default project storage directory and register it
mako-project add https://github.com/owner/repo.git
mako-project add git@github.com:owner/repo.git --dir ~/Work --folder custom-name --space Work --json
```

### Behavior & Rules
- **Git URL detection**: `https://`, `http://`, `ssh://`, `git://`, `file://`, and SCP-style `user@host:owner/repo(.git)` URLs are cloned automatically via `git clone -- <url> <dest>`.
- **Destination selection**:
  - When `--dir <base-dir>` is omitted for a Git URL, Mako uses the first directory in `project_dirs` (configured in `Settings ▸ General` or `mako projects dirs add <path>`).
  - If no `project_dirs` are configured and `--dir` is omitted, `mako-project add` returns a clear error instructing you to pass `--dir <directory>`.
  - If `<dest>` already exists and is already a clone of the same remote repository, Mako reuses it cleanly instead of failing.
- **Deduplication**: If the folder (or a unique matching Git remote) is already registered in Mako's workspace, Mako links/returns the existing project instead of duplicating it.
- **Outside a session (`mako` CLI)**: Users and external scripts can also run:
  ```bash
  mako projects [list] [--json]
  mako projects dirs [add <path> | remove <path> | default <path>]
  mako projects add <path-or-git-url> [-name <name>] [-dir <base-dir>] [-folder <name>] [-space <space>] [-json]
  ```

## 2. Plugin Tools CLI (`mako-plugin`)

Contributed plugin tools are exposed via `mako-plugin` rather than inflating MCP schemas:

```bash
mako-plugin list [--json] [plugin-id]
mako-plugin help <plugin-id> [tool-id]
mako-plugin call <plugin-id> <tool-id> --param value --body -
mako-plugin call <plugin-id> <tool-id> --json '{"key":"value"}'
```

- Pass `-` as a flag value (or `--json -`) to stream multiline/Markdown input from `stdin`.
- Scope isolation (`global`, `space`, `project`, `session`) is enforced server-side from the session token.

## 3. Inter-Agent Messaging CLI (`mako-msg`)

```bash
mako-msg list                                            # list active agents in this project
mako-msg profiles [--node <node-id>]                     # list spawnable agent profiles
mako-msg send <to-session-id> <message...>               # send a message (or '-' for stdin)
mako-msg read [--after <id>] [--ack <id>] [--limit <n>]  # read inbox messages
mako-msg wait [--all-children | --session <id1,id2>] [--from <id>] [--timeout <sec>]
mako-msg inspect [--turns <n>] <session-id>             # read another agent: status, waits, latest turns
```

- Prefer native push notifications and `watch_agent` over polling loops.
- Use `mako-msg wait` in a background shell command only when you need event-driven waiting within a single turn.

## 4. Built-in `mako` MCP Server Tools

- **Agents**: `list_agents`, `list_agent_profiles`, `spawn_agent`, `send_message`, `read_messages`, `watch_agent`, `read_agent`, `stop_agent`
- **Window layout**: `get_layout`, `list_blocks` (returns `plugin_dir`), `patch_layout`, `set_layout`, `undo_layout`
- **Ephemeral UI**: `show_ui`, `update_ui`, `close_ui` (for temporary forms/views; build a plugin for persistent panels)
- **Templates**: `save_template` (validates and writes `<configDir>/templates/<name>.md`)
<!-- mako-managed-skill -->
